package store

import (
	"context"
	"encoding/base64"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const GatewayAuthorizationWindow = 5 * time.Second

var gatewayIDPattern = regexp.MustCompile(`^gw_[a-z0-9_-]{8,64}$`)
var tunnelIDPattern = regexp.MustCompile(`^tun_[A-Za-z0-9_-]{24,128}$`)

// Target is populated by the trusted broker after pinning the managed Guest's
// address and RDP certificate. It must never be accepted from a client body.
type NativeGatewayTicketRequest struct {
	ConnectionID      string
	NativeDigest      []byte
	GatewayID         string
	Target            netip.Addr
	CertificateSHA256 []byte
	PolicyRevision    int64 // exact applied snapshot placed in the client's descriptor
}
type NativeGatewayTicket struct {
	Token     string    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
}
type NativeGatewayGrant struct {
	ConnectionID      string
	GatewayID         string
	TunnelID          string
	Target            netip.Addr
	CertificateSHA256 []byte
	PolicyRevision    int64
	// Gateway must subtract RPC time using its monotonic request start. It must
	// close on renewal failure/timeout, never extend authority from cached data.
	ValidFor time.Duration
}

// Re-evaluated at mint, redemption and every renewal. statement_timestamp is
// deliberately not transaction now(): the access lock may have waited beyond
// credential/connection expiry before this query begins.
const gatewayAccess = `
 SELECT c.id, c.user_id, c.native_session_digest, policy.desired_revision AS policy_revision,
   LEAST(c.expires_at,n.expires_at) AS deadline
 FROM desktop_connection_sessions c
 JOIN native_sessions n ON n.token_digest=c.native_session_digest AND n.user_id=c.user_id
 JOIN effective_user_desktop_access e ON e.user_id=c.user_id AND e.desktop_vmid=c.desktop_vmid
 JOIN managed_desktops d ON d.vmid=c.desktop_vmid
 JOIN guest_identity_bindings b ON b.desktop_vmid=c.desktop_vmid AND b.user_id=c.user_id AND b.guest_username=c.guest_username
 JOIN identity_profiles p ON p.id=COALESCE(b.profile_id,'managed-local-'||d.os_family)
 JOIN desktop_access_policies policy ON policy.vmid=c.desktop_vmid
 WHERE c.state='active' AND c.closed_at IS NULL AND NOT c.termination_required
 AND c.expires_at>statement_timestamp() AND n.expires_at>statement_timestamp()
 AND b.state IN ('ready','provisioning')
 AND p.enabled AND p.platform=d.os_family AND p.id=COALESCE(d.identity_profile_id,'managed-local-'||d.os_family)
 AND policy.state='applied' AND policy.desired_revision=policy.applied_revision AND policy.os_family=d.os_family
 AND NOT EXISTS(SELECT 1 FROM guest_identity_revocations q WHERE q.desktop_vmid=c.desktop_vmid AND q.requested_revision>q.completed_revision)
 AND NOT EXISTS(SELECT 1 FROM native_guest_accounts a WHERE a.desktop_vmid=c.desktop_vmid AND
   (a.state='pending' OR (a.user_id=c.user_id AND (a.connection_id<>c.id OR a.operation<>'issue' OR a.state<>'applied'))))
`

func gatewayError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrConflict
	}
	return err
}
func (s *Store) CreateNativeGatewayTicket(ctx context.Context, request NativeGatewayTicketRequest) (NativeGatewayTicket, error) {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return NativeGatewayTicket{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ticket, err := createNativeGatewayTicket(ctx, tx, request)
	if err != nil {
		return NativeGatewayTicket{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NativeGatewayTicket{}, err
	}
	return ticket, nil
}

func createNativeGatewayTicket(ctx context.Context, tx pgx.Tx, request NativeGatewayTicketRequest) (NativeGatewayTicket, error) {
	if len(request.NativeDigest) != 32 || !gatewayIDPattern.MatchString(request.GatewayID) ||
		!request.Target.Is4() || !request.Target.IsPrivate() || len(request.CertificateSHA256) != 32 || len(request.ConnectionID) > 140 || request.ConnectionID == "" || request.PolicyRevision <= 0 {
		return NativeGatewayTicket{}, ErrNotFound
	}
	secret, err := auth.OpaqueToken(32)
	if err != nil {
		return NativeGatewayTicket{}, err
	}
	token := "gwt_" + secret
	ticket := NativeGatewayTicket{Token: token}
	err = tx.QueryRow(ctx, `WITH access AS (`+gatewayAccess+`)
 INSERT INTO gateway_session_tickets(token_digest,connection_id,gateway_id,target_host,target_port,certificate_sha256,policy_revision,created_at,expires_at,session_deadline)
 SELECT $1,a.id,$3,$4::inet,3389,$5,a.policy_revision,statement_timestamp(),LEAST(a.deadline,statement_timestamp()+interval '30 seconds'),a.deadline
 FROM access a WHERE a.id=$2 AND a.native_session_digest=$6 AND a.policy_revision=$7 RETURNING expires_at`,
		auth.TokenDigest(token), request.ConnectionID, request.GatewayID, request.Target.String(), request.CertificateSHA256, request.NativeDigest, request.PolicyRevision).Scan(&ticket.ExpiresAt)
	if err != nil {
		return NativeGatewayTicket{}, gatewayError(err)
	}
	if err = auditGatewayTicket(ctx, tx, request.ConnectionID, request.GatewayID, "", "gateway.ticket_created"); err != nil {
		return NativeGatewayTicket{}, err
	}
	return ticket, nil
}

func gatewayTokenDigest(token string) ([]byte, error) {
	if !strings.HasPrefix(token, "gwt_") || len(token) != 47 {
		return nil, ErrNotFound
	}
	value, err := base64.RawURLEncoding.Strict().DecodeString(token[4:])
	if err != nil || len(value) != 32 {
		return nil, ErrNotFound
	}
	return auth.TokenDigest(token), nil
}

func scanGatewayGrant(row pgx.Row) (NativeGatewayGrant, error) {
	var grant NativeGatewayGrant
	var host string
	var micros int64
	err := row.Scan(&grant.ConnectionID, &grant.GatewayID, &grant.TunnelID, &host, &grant.CertificateSHA256, &grant.PolicyRevision, &micros)
	if err != nil {
		return grant, gatewayError(err)
	}
	grant.Target, err = netip.ParseAddr(host)
	grant.ValidFor = time.Duration(micros) * time.Microsecond
	if err != nil || !grant.Target.Is4() || !grant.Target.IsPrivate() || len(grant.CertificateSHA256) != 32 || grant.ValidFor <= 0 || grant.ValidFor > GatewayAuthorizationWindow {
		return NativeGatewayGrant{}, ErrNotFound
	}
	return grant, nil
}

const gatewayGrantColumns = `g.connection_id,g.gateway_id,g.tunnel_id,host(g.target_host),g.certificate_sha256,g.policy_revision,
 floor(extract(epoch FROM (g.lease_until-statement_timestamp()))*1000000)::bigint`

// GatewayID must come from the authenticated Gateway service identity, not the
// desktop client. Redemption commits before any TCP dial. An uncertain commit
// burns the ticket: there is no replay path that returns the consumed grant.
func (s *Store) RedeemNativeGatewayTicket(ctx context.Context, gatewayID, token string) (NativeGatewayGrant, error) {
	digest, err := gatewayTokenDigest(token)
	if err != nil || !gatewayIDPattern.MatchString(gatewayID) {
		return NativeGatewayGrant{}, ErrNotFound
	}
	random, err := auth.OpaqueToken(24)
	if err != nil {
		return NativeGatewayGrant{}, err
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return NativeGatewayGrant{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	grant, err := scanGatewayGrant(tx.QueryRow(ctx, `WITH access AS (`+gatewayAccess+`)
 UPDATE gateway_session_tickets g SET tunnel_id=$3,consumed_at=statement_timestamp(),
 lease_until=LEAST(g.session_deadline,a.deadline,statement_timestamp()+interval '5 seconds')
 FROM access a WHERE g.token_digest=$1 AND g.gateway_id=$2 AND a.id=g.connection_id
 AND g.consumed_at IS NULL AND g.expires_at>statement_timestamp() AND g.session_deadline>statement_timestamp()
 AND g.policy_revision=a.policy_revision
 RETURNING `+gatewayGrantColumns, digest, gatewayID, "tun_"+random))
	if err != nil {
		return NativeGatewayGrant{}, err
	}
	if err = auditGatewayTicket(ctx, tx, grant.ConnectionID, gatewayID, grant.TunnelID, "gateway.tunnel_authorized"); err != nil {
		return NativeGatewayGrant{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NativeGatewayGrant{}, err
	}
	return grant, nil
}

func (s *Store) RenewNativeGatewayGrant(ctx context.Context, gatewayID, tunnelID string) (NativeGatewayGrant, error) {
	if !gatewayIDPattern.MatchString(gatewayID) || !tunnelIDPattern.MatchString(tunnelID) {
		return NativeGatewayGrant{}, ErrNotFound
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return NativeGatewayGrant{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	grant, err := scanGatewayGrant(tx.QueryRow(ctx, `WITH access AS (`+gatewayAccess+`)
 UPDATE gateway_session_tickets g SET lease_until=LEAST(g.session_deadline,a.deadline,statement_timestamp()+interval '5 seconds')
 FROM access a WHERE g.tunnel_id=$1 AND g.gateway_id=$2 AND a.id=g.connection_id
 AND g.closed_at IS NULL AND g.lease_until>statement_timestamp() AND g.session_deadline>statement_timestamp()
 AND g.policy_revision=a.policy_revision
 RETURNING `+gatewayGrantColumns, tunnelID, gatewayID))
	if err != nil {
		return NativeGatewayGrant{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NativeGatewayGrant{}, err
	}
	return grant, nil
}

func (s *Store) CloseNativeGatewayGrant(ctx context.Context, gatewayID, tunnelID string) error {
	if !gatewayIDPattern.MatchString(gatewayID) || !tunnelIDPattern.MatchString(tunnelID) {
		return ErrNotFound
	}
	// Idempotent closure does not retire credentials or terminate the OS desktop.
	// The normal broker lifecycle remains responsible for those operations.
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `WITH closed AS (
 UPDATE gateway_session_tickets SET closed_at=statement_timestamp()
 WHERE gateway_id=$1 AND tunnel_id=$2 AND consumed_at IS NOT NULL AND closed_at IS NULL RETURNING connection_id
 ) INSERT INTO audit_events(actor_id,event_type,outcome,target_type,target_id,detail)
 SELECT c.user_id,'gateway.tunnel_closed','success','desktop_connection',c.id,
 jsonb_build_object('gateway_id',$1::text,'tunnel_id',$2::text)
 FROM closed JOIN desktop_connection_sessions c ON c.id=closed.connection_id`, gatewayID, tunnelID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func auditGatewayTicket(ctx context.Context, tx pgx.Tx, connectionID, gatewayID, tunnelID, event string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(actor_id,event_type,outcome,target_type,target_id,detail)
 SELECT user_id,$4,'success','desktop_connection',id,jsonb_build_object('gateway_id',$2::text,'tunnel_id',$3::text)
 FROM desktop_connection_sessions WHERE id=$1`, connectionID, gatewayID, tunnelID, event)
	return err
}
