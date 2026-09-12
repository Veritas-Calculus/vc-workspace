package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

type loginOrderExecutor struct {
	controlledComputerExecutor
	db                                                *store.Store
	t                                                 *testing.T
	starts                                            int
	loseReceipt, missingHelper, noProcesses, failStop bool
	stopped                                           computer.AccountLease
	rootWriter                                        bool
	rootWriterPolls                                   int
}

func (e *loginOrderExecutor) StartAgentSession(ctx context.Context, m pve.VM, intent computer.AccountLease) (computer.SessionTarget, error) {
	rows, err := e.db.AgentGuestSessions(ctx, m.VMID)
	if err != nil || len(rows) != 1 {
		e.t.Fatal("login dispatched without registered identity", err)
	}
	row := rows[0]
	if row.State != "provisioning" || row.SessionID != "" || row.InstanceID != "" || agentAccountLease(row) != intent || row.GuestUID != 1001 || row.LoginGeneration < 1 {
		e.t.Fatal("credentials preceded immutable identity/login reservation", row, intent)
	}
	e.starts++
	target, err := e.controlledComputerExecutor.StartAgentSession(ctx, m, intent)
	e.missingHelper, e.noProcesses = false, false
	if e.loseReceipt {
		return computer.SessionTarget{}, computer.ErrUnavailable
	}
	return target, err
}
func (e *loginOrderExecutor) DiscoverSession(ctx context.Context, m pve.VM, u string) (computer.SessionTarget, error) {
	if e.missingHelper {
		return computer.SessionTarget{}, computer.ErrUnavailable
	}
	return e.controlledComputerExecutor.DiscoverSession(ctx, m, u)
}
func (e *loginOrderExecutor) InspectAgentAccount(ctx context.Context, m pve.VM, u string) (computer.LinuxAccountObservation, error) {
	value, err := e.controlledComputerExecutor.InspectAgentAccount(ctx, m, u)
	if e.noProcesses {
		absent := true
		value.ProcessesAbsent = &absent
		value.LoginWritersAbsent = &absent
	}
	if e.rootWriter {
		present := false
		value.LoginWritersAbsent = &present
		if e.rootWriterPolls > 0 {
			e.rootWriterPolls--
			e.rootWriter = e.rootWriterPolls != 0
		}
	}
	return value, err
}
func (e *loginOrderExecutor) StopAgentSession(ctx context.Context, m pve.VM, intent computer.AccountLease) error {
	e.stopped = intent
	if e.failStop {
		return computer.ErrUnavailable
	}
	return e.controlledComputerExecutor.StopAgentSession(ctx, m, intent)
}

func loginOrderFixture(t *testing.T) (*Server, *loginOrderExecutor, store.DesktopLease, pve.VM) {
	t.Helper()
	db, _ := regressionDB(t)
	machine := pve.VM{VMID: 9001, Node: "test"}
	if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: 9001, Node: "test", OSFamily: "linux", DisplayName: "Login order"}); err != nil {
		t.Fatal(err)
	}
	policy, err := db.EnsureDesktopAccessPolicy(t.Context(), 9001)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkDesktopAccessPolicyApplied(t.Context(), 9001, policy.DesiredRevision, "linux"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAgentPrincipal(t.Context(), store.AgentPrincipal{ID: "agent-a", DisplayName: "A"}, []byte("unused")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(t.Context(), store.DesktopAssignment{SubjectType: "agent", SubjectID: "agent-a", DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	lease, err := db.CreateDesktopLease(t.Context(), store.DesktopLease{ID: "lease_order", AgentID: "agent-a", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/agent/exec") {
			_, _ = w.Write([]byte(`{"data":{"pid":1}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/agent/exec-status") {
			_, _ = w.Write([]byte(`{"data":{"exited":1,"exitcode":0}}`))
			return
		}
		http.Error(w, "unexpected fixture request", 500)
	}))
	t.Cleanup(server.Close)
	pveClient, err := pve.New(pve.Config{Endpoint: server.URL, TokenID: "test@pve!test", TokenSecret: "unused", HTTPClient: server.Client(), MutationsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	executor := &loginOrderExecutor{db: db, t: t}
	return New(Dependencies{Store: db, PVE: pveClient, Computer: executor}), executor, lease, machine
}

func TestDefaultAgentLoginPinsBeforeCredentialAndReusesLiveSession(t *testing.T) {
	s, e, lease, machine := loginOrderFixture(t)
	prepare := func() error {
		return s.store.WithDesktopConnectionLock(t.Context(), machine.VMID, func() error { _, err := s.prepareAgentGuestSession(t.Context(), machine, lease); return err })
	}
	if err := prepare(); err != nil {
		t.Fatal(err)
	}
	if err := prepare(); err != nil || e.starts != 1 {
		t.Fatal("healthy session unnecessarily reopened", err, e.starts)
	}
	e.missingHelper, e.noProcesses = true, true
	if err := prepare(); err != nil || e.starts != 2 {
		t.Fatal("verified absent desktop did not reconnect", err, e.starts)
	}
	rows, err := s.store.AgentGuestSessions(t.Context(), machine.VMID)
	if err != nil || rows[0].LoginGeneration != 2 || rows[0].ControlEpoch != lease.ControlEpoch || rows[0].LeaseID != lease.ID || rows[0].State != "ready" {
		t.Fatal("reconnect changed lease or lost version", rows, err)
	}
	e.missingHelper, e.noProcesses = true, false
	if err := prepare(); err == nil || e.starts != 2 {
		t.Fatal("transient discovery failure replaced running applications", err, e.starts)
	}
}

func TestDefaultAgentDoesNotReopenWhileRootLoginWriterRemains(t *testing.T) {
	s, e, lease, machine := loginOrderFixture(t)
	if _, err := s.prepareAgentGuestSession(t.Context(), machine, lease); err != nil {
		t.Fatal(err)
	}
	e.missingHelper, e.noProcesses, e.rootWriter = true, true, true
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := s.prepareAgentGuestSession(ctx, machine, lease); err == nil || e.starts != 1 {
		t.Fatal("root session creator was mistaken for a fully exited login", err, e.starts)
	}
	rows, err := s.store.AgentGuestSessions(t.Context(), machine.VMID)
	if err != nil || len(rows) != 1 || rows[0].LoginGeneration != 1 || rows[0].State != "revoking" {
		t.Fatal("unresolved login lost its original cleanup version", rows, err)
	}
}

func TestDefaultAgentWaitsForOldLoginJobsBeforeReservingReconnect(t *testing.T) {
	s, e, lease, machine := loginOrderFixture(t)
	if _, err := s.prepareAgentGuestSession(t.Context(), machine, lease); err != nil {
		t.Fatal(err)
	}
	e.missingHelper, e.noProcesses, e.rootWriter, e.rootWriterPolls = true, true, true, 2
	if _, err := s.prepareAgentGuestSession(t.Context(), machine, lease); err != nil || e.starts != 2 || e.rootWriter {
		t.Fatal("settled login jobs did not permit one reconnect", err, e.starts)
	}
	rows, err := s.store.AgentGuestSessions(t.Context(), machine.VMID)
	if err != nil || len(rows) != 1 || rows[0].LoginGeneration != 2 || rows[0].State != "ready" ||
		rows[0].GuestUID != 1001 || rows[0].LeaseID != lease.ID || rows[0].ControlEpoch != lease.ControlEpoch || !rows[0].ExpiresAt.Equal(lease.ExpiresAt) {
		t.Fatal("read-only wait changed lease, identity or login version", rows, err)
	}
}

func TestDefaultAgentLostLoginReceiptClosesExactAttemptAndRetainsFailedCleanup(t *testing.T) {
	s, e, lease, machine := loginOrderFixture(t)
	e.loseReceipt = true
	_, err := s.prepareAgentGuestSession(t.Context(), machine, lease)
	if err == nil || e.starts != 1 {
		t.Fatal("lost receipt acknowledged", err, e.starts)
	}
	rows, err := s.store.AgentGuestSessions(t.Context(), 9001)
	if err != nil || rows[0].State != "revoking" || rows[0].LoginGeneration != 1 || rows[0].GuestUID != 1001 {
		t.Fatal("failed attempt lost its pinned cleanup", rows, err)
	}
	if _, err := s.prepareAgentGuestSession(t.Context(), machine, lease); !errors.Is(err, store.ErrConflict) || e.starts != 1 {
		t.Fatal("lost login was replayed", err, e.starts)
	}
	e.failStop = true
	if err := s.revokeAgentGuestSessions(t.Context(), machine); err == nil {
		t.Fatal("failed Guest cleanup acknowledged")
	}
	rows, _ = s.store.AgentGuestSessions(t.Context(), 9001)
	if rows[0].State != "revoking" || e.stopped.LoginGeneration != 1 || e.stopped.ControlEpoch != lease.ControlEpoch {
		t.Fatal("wrong cleanup version", rows, e.stopped)
	}
	e.failStop = false
	if err := s.revokeAgentGuestSessions(t.Context(), machine); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.store.AgentGuestSessions(t.Context(), 9001)
	if rows[0].State != "disabled" || rows[0].GuestUID != 1001 || rows[0].LoginGeneration != 1 {
		t.Fatal("cleanup lost account/version", rows)
	}
}
