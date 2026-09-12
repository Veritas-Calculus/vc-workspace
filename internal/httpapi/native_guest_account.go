package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/gateway"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func nativeCredential(a store.NativeGuestAccount) computer.NativeCredential {
	value := computer.NativeCredential{SchemaVersion: 1, Identity: computer.AccountIdentity{
		Username: a.GuestUsername, UID: a.GuestUID, SID: a.GuestSID},
		ConnectionID: a.ConnectionID, Revision: a.Revision}
	if a.ExpiresAt != nil {
		value.ExpiresUnixSeconds = a.ExpiresAt.Unix()
	}
	return value
}

func (s *Server) nativeLifecycle(a store.NativeGuestAccount) (computer.NativeAccountLifecycle, error) {
	lifecycle, ok := s.computer.(computer.NativeAccountLifecycle)
	if !ok || a.OSFamily != "linux" {
		return nil, computer.ErrUnavailable
	}
	return lifecycle, nil
}

// Called under the distributed VM lock after owned provisioning, before any
// credential. A username or successful login never establishes UID provenance.
func (s *Server) bindNativeGuestAccount(ctx context.Context, machine pve.VM, user store.User, digest []byte) (store.NativeGuestAccount, error) {
	a := store.NativeGuestAccount{DesktopVMID: machine.VMID, UserID: user.ID,
		GuestUsername: store.NativeGuestUsername(user.ID), OSFamily: "linux"}
	lifecycle, err := s.nativeLifecycle(a)
	if err != nil {
		return a, err
	}
	observed, err := lifecycle.InspectNativeAccount(ctx, machine, a.GuestUsername)
	if err != nil || observed.Identity.Username != a.GuestUsername || !observed.Exists {
		return a, computer.ErrUnavailable
	}
	a.GuestUID, a.GuestSID = observed.Identity.UID, observed.Identity.SID
	a, err = s.store.BindNativeGuestAccount(ctx, a, digest)
	if err != nil {
		return a, err
	}
	// Do not adopt unversioned live accounts, expired retained desktops, or an
	// interrupted credential operation. Recovery must finish before new issue.
	switch {
	case a.State == "idle":
		if observed.Lifecycle != nil || !observed.Closed() {
			return a, computer.ErrUnavailable
		}
	case a.State == "applied" && a.Operation == "revoke":
		if !observed.Matches(nativeCredential(a), "revoked") || !observed.Closed() {
			return a, computer.ErrUnavailable
		}
	case a.State == "applied" && a.Operation == "retire":
		if !observed.Matches(nativeCredential(a), "retired") || !observed.Retained() {
			return a, computer.ErrUnavailable
		}
	default:
		return a, store.ErrConflict
	}
	return a, nil
}

func (s *Server) issueNativeGuestConnection(ctx context.Context, machine pve.VM, a store.NativeGuestAccount, digest []byte, gatewayRequest *store.NativeGatewayTicketRequest) (connection store.DesktopConnectionSession, password string, ticket store.NativeGatewayTicket, err error) {
	lifecycle, err := s.nativeLifecycle(a)
	if err != nil {
		return connection, "", ticket, err
	}
	id, err := auth.OpaqueToken(18)
	if err != nil {
		return connection, "", ticket, err
	}
	secret, err := auth.OpaqueToken(24)
	if err != nil {
		return connection, "", ticket, err
	}
	a, err = s.store.BeginNativeGuestCredential(ctx, a, "conn_"+id, time.Now().Add(8*time.Hour).Truncate(time.Second), digest)
	if err != nil {
		return connection, "", ticket, err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// Preserve cleanup even after request cancellation or an uncertain DB
		// commit. The caller still owns the VM gate; never replay the password.
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		current, readErr := s.store.NativeGuestAccount(cleanup, a.DesktopVMID, a.UserID)
		if readErr == nil && current.ConnectionID == a.ConnectionID && current.Revision == a.Revision && current.Operation == "issue" {
			_, readErr = s.store.RevokeNativeGuestAccount(cleanup, current)
		}
		if readErr != nil {
			s.logger.Warn("Native credential recovery intent remains pending", "vmid", a.DesktopVMID, "user_id", a.UserID)
		}
	}()
	credential := []byte("Vcw1!" + secret)
	defer clear(credential)
	if err = lifecycle.ChangeNativeCredential(ctx, machine, nativeCredential(a), "issue", credential); err != nil {
		return connection, "", ticket, err
	}
	if gatewayRequest == nil {
		connection, err = s.store.CompleteNativeGuestConnection(ctx, a)
	} else {
		request := *gatewayRequest
		request.ConnectionID, request.NativeDigest = a.ConnectionID, digest
		connection, ticket, err = s.store.CompleteNativeGuestGatewayConnection(ctx, a, request)
		if err != nil {
			// Do not log raw SQL diagnostics that can include bound ticket or
			// credential information in the outer preparation error handler.
			err = gateway.ErrAuthorization
		}
	}
	if err != nil {
		return connection, "", store.NativeGatewayTicket{}, err
	}
	committed = true
	return connection, string(credential), ticket, nil
}

// Both explicit access revocation and orphan recovery use this path. The
// database intent is read under the VM lock; exact revoked retries are safe.
func (s *Server) revokeNativeGuestAccount(ctx context.Context, machine pve.VM, a store.NativeGuestAccount) error {
	lifecycle, err := s.nativeLifecycle(a)
	if err != nil {
		return err
	}
	if a.State == "idle" {
		observed, err := lifecycle.InspectNativeAccount(ctx, machine, a.GuestUsername)
		if err != nil || observed.Identity != nativeCredential(a).Identity || observed.Lifecycle != nil || !observed.Closed() {
			return computer.ErrUnavailable
		}
		return nil
	}
	if a.Operation != "revoke" {
		a, err = s.store.RevokeNativeGuestAccount(ctx, a)
		if err != nil {
			return err
		}
	}
	if err := lifecycle.ChangeNativeCredential(ctx, machine, nativeCredential(a), "revoke", nil); err != nil {
		return err
	}
	if a.State == "pending" {
		return s.store.CompleteNativeGuestAccountOperation(ctx, a)
	}
	return nil
}

func (s *Server) retireNativeGuestConnection(ctx context.Context, machine pve.VM, a store.NativeGuestAccount, connection store.DesktopConnectionSession, terminate bool) error {
	if a.ConnectionID != connection.ID || a.GuestUsername != connection.GuestUsername || a.UserID != connection.UserID || a.DesktopVMID != connection.DesktopVMID {
		return store.ErrConflict
	}
	lifecycle, err := s.nativeLifecycle(a)
	if err != nil {
		return err
	}
	if terminate || a.Operation == "revoke" || a.ExpiresAt == nil || !a.ExpiresAt.After(time.Now()) {
		return s.revokeNativeGuestAccount(ctx, machine, a)
	}
	if a.Operation == "issue" && a.State == "applied" {
		a, err = s.store.RetireNativeGuestCredential(ctx, a)
		if err != nil {
			return err
		}
		if err := lifecycle.ChangeNativeCredential(ctx, machine, nativeCredential(a), "retire", nil); err != nil {
			return err
		}
		return s.store.CompleteNativeGuestAccountOperation(ctx, a)
	}
	if a.Operation == "retire" {
		observed, err := lifecycle.InspectNativeAccount(ctx, machine, a.GuestUsername)
		if err != nil {
			return err
		}
		if observed.Matches(nativeCredential(a), "retired") && observed.Retained() {
			if a.State == "pending" {
				return s.store.CompleteNativeGuestAccountOperation(ctx, a)
			}
			return s.store.MarkDesktopConnectionClosed(ctx, connection.ID, "revoked")
		}
	}
	// An interrupted retirement cannot be blindly reissued: a still-running
	// writer could apply after the next connection. Close that version instead.
	return s.revokeNativeGuestAccount(ctx, machine, a)
}

func (s *Server) recoverNativeAccountsForDesktop(ctx context.Context, vmid int) error {
	accounts, err := s.store.NativeGuestAccountsForDesktop(ctx, vmid)
	if err != nil || len(accounts) == 0 {
		return err
	}
	desktop, err := s.store.ManagedDesktopByVMID(ctx, vmid)
	if err != nil {
		return err
	}
	machine := pve.VM{VMID: vmid, Node: desktop.Node}
	for _, a := range accounts {
		due := a.State == "pending" || (a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now()))
		if !due && a.Operation == "issue" && a.State == "applied" {
			connection, err := s.store.DesktopConnectionByID(ctx, a.ConnectionID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			due = errors.Is(err, store.ErrNotFound) || (connection.State != "active" && connection.State != "revoking")
		}
		if !due {
			continue
		}
		if a.State == "pending" && a.Operation == "retire" && a.ExpiresAt != nil && a.ExpiresAt.After(time.Now()) {
			lifecycle, err := s.nativeLifecycle(a)
			if err != nil {
				return err
			}
			observed, err := lifecycle.InspectNativeAccount(ctx, machine, a.GuestUsername)
			if err != nil {
				return err
			}
			if observed.Matches(nativeCredential(a), "retired") && observed.Retained() {
				if err := s.store.CompleteNativeGuestAccountOperation(ctx, a); err != nil {
					return err
				}
				continue
			}
		}
		if err := s.revokeNativeGuestAccount(ctx, machine, a); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) recoverDueNativeAccounts(ctx context.Context) {
	items, err := s.store.NativeGuestAccountsNeedingRecovery(ctx, 100)
	if err != nil {
		s.logger.Warn("read Native credential recovery queue")
		return
	}
	seen := make(map[int]bool)
	for _, item := range items {
		if seen[item.DesktopVMID] {
			continue
		}
		seen[item.DesktopVMID] = true
		if err := s.store.WithDesktopConnectionLock(ctx, item.DesktopVMID, func() error {
			return s.recoverNativeAccountsForDesktop(ctx, item.DesktopVMID)
		}); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Warn("Native credential recovery remains pending", "vmid", item.DesktopVMID)
		}
	}
}
