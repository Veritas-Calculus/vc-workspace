package httpapi

import (
	"context"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

// Caller holds the distributed desktop control lock. A fresh queue read keeps
// a stale worker from revoking authority that was issued after its snapshot.
func (s *Server) synchronizeComputerRevocation(ctx context.Context, machine pve.VM, force bool) error {
	if err := s.store.QueueExpiredComputerLeases(ctx, machine.VMID); err != nil {
		return err
	}
	items, err := s.store.PendingComputerRevocations(ctx, machine.VMID)
	if err != nil {
		return err
	}
	if len(items) == 0 && !force {
		return nil
	}
	if _, ok := s.computer.(computer.AuthorityController); !ok {
		return computer.ErrUnavailable
	}
	if err := s.revokeAllComputerAuthority(ctx, machine); err != nil {
		return err
	}
	if err := s.revokeAgentGuestSessions(ctx, machine); err != nil {
		return err
	}
	for _, item := range items {
		if err := s.store.CompleteComputerRevocation(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) revokeDueComputerAuthorities(ctx context.Context) {
	if err := s.store.QueueExpiredComputerLeases(ctx, 0); err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("queue expired computer leases", "error", err)
		}
		return
	}
	items, err := s.store.PendingComputerRevocations(ctx, 0)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("read computer revocation queue", "error", err)
		}
		return
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := s.store.WithDesktopConnectionLock(attempt, item.DesktopVMID, func() error {
			desktop, err := s.store.ManagedDesktopByVMID(attempt, item.DesktopVMID)
			if err != nil {
				return err
			}
			return s.synchronizeComputerRevocation(attempt, pve.VM{VMID: item.DesktopVMID, Node: desktop.Node}, false)
		})
		cancel()
		if err != nil && ctx.Err() == nil {
			s.store.DeferComputerRevocation(ctx, item)
			s.logger.Warn("Guest computer revocation remains pending", "vmid", item.DesktopVMID, "error", err)
		}
	}
}
