package gateway

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

const (
	ControlRedeemPath = "/internal/gateway/v1/redeem"
	ControlRenewPath  = "/internal/gateway/v1/renew"
	ControlClosePath  = "/internal/gateway/v1/close"
	ControlReadyPath  = "/internal/gateway/v1/ready"
)

type ControlBackend interface {
	Authority
	Ping(context.Context) error
}

type controlHandler struct {
	backend  ControlBackend
	pins     PeerPins
	slots    chan struct{}
	identity func(*tls.ConnectionState) string
}

func NewControlHandler(backend ControlBackend, pins PeerPins) (http.Handler, error) {
	if backend == nil || len(pins) == 0 || len(pins) > 256 {
		return nil, ErrConfiguration
	}
	copyPins := make(PeerPins, len(pins))
	for pin, id := range pins {
		if !gatewayID.MatchString(id) {
			return nil, ErrConfiguration
		}
		copyPins[pin] = id
	}
	return &controlHandler{backend: backend, pins: copyPins, slots: make(chan struct{}, 64)}, nil
}

type wireGrant struct {
	ConnectionID      string `json:"connection_id"`
	GatewayID         string `json:"gateway_id"`
	TunnelID          string `json:"tunnel_id"`
	Target            string `json:"target"`
	CertificateSHA256 string `json:"certificate_sha256"`
	PolicyRevision    int64  `json:"policy_revision"`
	ValidForMS        int64  `json:"valid_for_ms"`
}

// The internal service is deliberately not mounted on the public API mux. It
// requires direct, verified mTLS; forwarded certificate/identity headers have
// no authority. It cannot mint a ticket or accept a target/port from a Gateway.
func (h *controlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id := peerIdentity(r.TLS, h.pins)
	if h.identity != nil {
		id = h.identity(r.TLS)
	}
	if id == "" {
		http.Error(w, "Gateway identity rejected", http.StatusForbidden)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
		http.NotFound(w, r)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(w, "Gateway authority unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()
	if r.URL.Path == ControlReadyPath && r.Method == http.MethodGet && r.ContentLength == 0 {
		if h.backend.Ping(ctx) != nil {
			http.Error(w, "Gateway authority unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost || (r.URL.Path != ControlRedeemPath && r.URL.Path != ControlRenewPath && r.URL.Path != ControlClosePath) {
		http.NotFound(w, r)
		return
	}
	field := "tunnel_id"
	if r.URL.Path == ControlRedeemPath {
		field = "ticket"
	}
	value, err := singleStringBody(w, r, field)
	if err != nil || (field == "ticket" && !validTicket(value)) || (field == "tunnel_id" && !tunnelID.MatchString(value)) {
		http.Error(w, "Invalid Gateway request", http.StatusBadRequest)
		return
	}
	var grant store.NativeGatewayGrant
	switch r.URL.Path {
	case ControlRedeemPath:
		grant, err = h.backend.RedeemNativeGatewayTicket(ctx, id, value)
	case ControlRenewPath:
		grant, err = h.backend.RenewNativeGatewayGrant(ctx, id, value)
	case ControlClosePath:
		err = h.backend.CloseNativeGatewayGrant(ctx, id, value)
	}
	if err != nil || ctx.Err() != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			status = http.StatusForbidden
		}
		http.Error(w, "Gateway authorization unavailable", status)
		return
	}
	if r.URL.Path == ControlClosePath {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if grant.GatewayID != id || grant.ValidFor.Milliseconds() < 1 || grant.ValidFor > store.GatewayAuthorizationWindow {
		http.Error(w, "Gateway grant unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(wireGrant{grant.ConnectionID, grant.GatewayID, grant.TunnelID, grant.Target.String(),
		hex.EncodeToString(grant.CertificateSHA256), grant.PolicyRevision, grant.ValidFor.Milliseconds()})
}

func singleStringBody(w http.ResponseWriter, r *http.Request, field string) (string, error) {
	if r.Header.Get("Content-Type") != "application/json" || r.ContentLength > 1024 {
		return "", ErrAuthorization
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return "", ErrAuthorization
	}
	if token, err := decoder.Token(); err != nil || token != field {
		return "", ErrAuthorization
	}
	var value string
	if err := decoder.Decode(&value); err != nil || decoder.More() {
		return "", ErrAuthorization // rejects unknown and duplicate fields
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return "", ErrAuthorization
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", ErrAuthorization
	}
	return value, nil
}
