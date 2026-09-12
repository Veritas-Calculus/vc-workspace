package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

type ControlClient struct {
	base      string
	id        string
	client    *http.Client
	transport interface{ CloseIdleConnections() }
}

func NewControlClient(endpoint, id string, config *tls.Config) (*ControlClient, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" || !gatewayID.MatchString(id) || config == nil || config.InsecureSkipVerify ||
		config.RootCAs == nil || len(config.RootCAs.Subjects()) == 0 || len(config.Certificates) != 1 || config.KeyLogWriter != nil ||
		len(config.Certificates[0].Certificate) == 0 || config.Certificates[0].PrivateKey == nil ||
		(config.ServerName != "" && config.ServerName != u.Hostname()) {
		return nil, ErrConfiguration
	}
	tlsConfig := config.Clone()
	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.ServerName = u.Hostname()
	tlsConfig.ClientSessionCache = nil
	transport := newControlTransport(tlsConfig)
	client := &http.Client{Transport: transport, Timeout: controlTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &ControlClient{strings.TrimRight(endpoint, "/"), id, client, transport}, nil
}

func newControlTransport(config *tls.Config) *http.Transport {
	return &http.Transport{TLSClientConfig: config, TLSHandshakeTimeout: controlTimeout,
		MaxConnsPerHost: 64, MaxIdleConns: 64, MaxIdleConnsPerHost: 64, IdleConnTimeout: 30 * time.Second,
		ResponseHeaderTimeout: controlTimeout, DisableCompression: true, MaxResponseHeaderBytes: 8192}
}

// Called once, before publishing the client to the relay or readiness handler.
func (c *ControlClient) reloadFrom(files tlsFiles) error {
	cfg, generation, err := files.load(true)
	if err != nil {
		return err
	}
	u, err := url.Parse(c.base)
	if err != nil {
		return ErrConfiguration
	}
	cfg.ServerName = u.Hostname()
	transport := &reloadingControlTransport{files: files, name: cfg.ServerName, generation: generation,
		transport: newControlTransport(cfg)}
	c.transport.CloseIdleConnections()
	c.client.Transport, c.transport = transport, transport
	return nil
}

func (c *ControlClient) Close() { c.transport.CloseIdleConnections() }

func (c *ControlClient) RedeemNativeGatewayTicket(ctx context.Context, id, ticket string) (store.NativeGatewayGrant, error) {
	if id != c.id || !validTicket(ticket) {
		return store.NativeGatewayGrant{}, ErrAuthorization
	}
	return c.grant(ctx, ControlRedeemPath, "ticket", ticket)
}
func (c *ControlClient) RenewNativeGatewayGrant(ctx context.Context, id, tunnel string) (store.NativeGatewayGrant, error) {
	if id != c.id || !tunnelID.MatchString(tunnel) {
		return store.NativeGatewayGrant{}, ErrAuthorization
	}
	return c.grant(ctx, ControlRenewPath, "tunnel_id", tunnel)
}
func (c *ControlClient) CloseNativeGatewayGrant(ctx context.Context, id, tunnel string) error {
	if id != c.id || !tunnelID.MatchString(tunnel) {
		return ErrAuthorization
	}
	_, err := c.request(ctx, http.MethodPost, ControlClosePath, "tunnel_id", tunnel, http.StatusNoContent)
	return err
}
func (c *ControlClient) Ping(ctx context.Context) error {
	_, err := c.request(ctx, http.MethodGet, ControlReadyPath, "", "", http.StatusNoContent)
	return err
}

func (c *ControlClient) grant(ctx context.Context, path, field, value string) (store.NativeGatewayGrant, error) {
	data, err := c.request(ctx, http.MethodPost, path, field, value, http.StatusOK)
	if err != nil {
		return store.NativeGatewayGrant{}, err
	}
	var wire wireGrant
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if !uniqueObject(data) || decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF {
		return store.NativeGatewayGrant{}, ErrAuthorization
	}
	target, err := netip.ParseAddr(wire.Target)
	pin, pinErr := hex.DecodeString(wire.CertificateSHA256)
	if err != nil || !target.Is4() || !target.IsPrivate() || pinErr != nil || len(pin) != 32 ||
		wire.GatewayID != c.id || !tunnelID.MatchString(wire.TunnelID) || wire.ConnectionID == "" || len(wire.ConnectionID) > 140 ||
		wire.PolicyRevision <= 0 || wire.ValidForMS <= 0 || wire.ValidForMS > store.GatewayAuthorizationWindow.Milliseconds() {
		return store.NativeGatewayGrant{}, ErrAuthorization
	}
	return store.NativeGatewayGrant{ConnectionID: wire.ConnectionID, GatewayID: wire.GatewayID, TunnelID: wire.TunnelID,
		Target: target, CertificateSHA256: pin, PolicyRevision: wire.PolicyRevision, ValidFor: time.Duration(wire.ValidForMS) * time.Millisecond}, nil
}

func (c *ControlClient) request(ctx context.Context, method, path, field, value string, expected int) ([]byte, error) {
	var body []byte
	if field != "" {
		body, _ = json.Marshal(map[string]string{field: value})
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrAuthorization
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, ErrAuthorization // never expose transport errors or URLs
	}
	defer res.Body.Close()
	if res.StatusCode != expected {
		return nil, ErrAuthorization // no retries and no redirect/body reflection
	}
	if expected == http.StatusNoContent {
		return nil, nil
	}
	if res.Header.Get("Content-Type") != "application/json" || res.ContentLength > 4096 {
		return nil, ErrAuthorization
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 4097))
	if err != nil || len(data) > 4096 {
		return nil, ErrAuthorization
	}
	return data, nil
}
