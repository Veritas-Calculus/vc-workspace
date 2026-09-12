package httpapi

import (
	"encoding/hex"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/gateway"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

type nativeGatewayConnection struct {
	URL               string    `json:"url"`
	Subprotocol       string    `json:"subprotocol"`
	Ticket            string    `json:"ticket"`
	ExpiresAt         time.Time `json:"expires_at"`
	CertificateSHA256 string    `json:"certificate_sha256"`
}

func nativeGatewayDescriptor(route *gateway.BrokerRoute, ticket store.NativeGatewayTicket, certificate [32]byte) *nativeGatewayConnection {
	return &nativeGatewayConnection{URL: route.URL(), Subprotocol: gateway.TunnelSubprotocol,
		Ticket: ticket.Token, ExpiresAt: ticket.ExpiresAt, CertificateSHA256: hex.EncodeToString(certificate[:])}
}
