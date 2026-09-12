// Package testguest contains exact-identity cleanup for opt-in disposable Guest
// fixtures. Production control-plane code must never import these helpers.
package testguest

import (
	"context"
	"fmt"

	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

// Caller already closed the fixture's leases in its disposable DB and proved
// each exact account name absent before creation. No prefix/shell cleanup here.
func StopAgentAccount(ctx context.Context, db *store.Store, executor *computer.PVEExecutor, machine pve.VM, username string) error {
	rows, err := db.AgentGuestSessions(ctx, machine.VMID)
	if err != nil {
		return err
	}
	observed, err := executor.InspectAgentAccount(ctx, machine, username)
	if err != nil {
		return err
	}
	var binding *store.AgentGuestSession
	for i := range rows {
		if rows[i].GuestUsername == username {
			binding = &rows[i]
			break
		}
	}
	if observed.Identity == nil {
		if binding == nil || (binding.LoginGeneration == 0 && binding.GuestUID == 0 && binding.GuestSID == "") {
			return nil
		}
		return fmt.Errorf("fixture account ownership record disappeared")
	}
	if binding != nil && binding.State == "disabled" {
		generation := binding.LoginGeneration
		if generation == 0 {
			generation = 1
		}
		expected := computer.AccountLease{SchemaVersion: 1, Identity: computer.AccountIdentity{Username: binding.GuestUsername, UID: binding.GuestUID, SID: binding.GuestSID}, LeaseID: binding.LeaseID, ControlEpoch: binding.ControlEpoch, LoginGeneration: generation}
		if observed.LoginStopped() && observed.Matches(expected, "revoked") {
			return nil
		}
		return fmt.Errorf("disabled fixture binding lacks exact Guest cleanup")
	}
	if binding == nil || binding.State != "revoking" {
		return fmt.Errorf("fixture account lacks a revoking registration")
	}
	binding.GuestUID, binding.GuestSID = observed.Identity.UID, observed.Identity.SID
	if err := db.BindRevokingAgentGuestAccount(ctx, *binding); err != nil {
		return err
	}
	generation := binding.LoginGeneration
	if generation == 0 {
		generation = 1
	}
	return executor.StopAgentSession(ctx, machine, computer.AccountLease{SchemaVersion: 1, Identity: *observed.Identity,
		LeaseID: binding.LeaseID, ControlEpoch: binding.ControlEpoch, LoginGeneration: generation})
}
