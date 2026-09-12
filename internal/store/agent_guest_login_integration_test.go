package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAgentLoginRequiresPreboundIdentityAndDurableVersion(t *testing.T) {
	db, lease := agentSessionFixture(t)
	initial, err := db.BeginAgentGuestSession(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	candidate := readyAgentBinding(initial)
	if _, err := db.BeginAgentGuestLogin(t.Context(), candidate); !errors.Is(err, ErrConflict) {
		t.Fatal("unbound account reserved login", err)
	}
	candidate.LoginGeneration = 1
	if err := db.MarkAgentGuestSessionReady(t.Context(), candidate); !errors.Is(err, ErrConflict) {
		t.Fatal("late Helper first-bound identity", err)
	}
	candidate.LoginGeneration = 0
	if err := db.BindAgentGuestAccount(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	rows, err := db.AgentGuestSessions(t.Context(), 9001)
	if err != nil || rows[0].GuestUID != 1001 || rows[0].LoginGeneration != 0 || rows[0].State != "provisioning" {
		t.Fatal("pre-login binding missing", rows, err)
	}
	start := make(chan struct{})
	done := make(chan error, 2)
	results := make(chan AgentGuestSession, 2)
	for range 2 {
		go func() {
			<-start
			value, err := db.BeginAgentGuestLogin(t.Context(), candidate)
			results <- value
			done <- err
		}()
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		err := <-done
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("login CAS did not choose one attempt", success, conflict)
	}
	var current AgentGuestSession
	for range 2 {
		value := <-results
		if value.LoginGeneration != 0 {
			current = value
		}
	}
	if current.LoginGeneration != 1 || current.Generation != initial.Generation || current.ControlEpoch != initial.ControlEpoch {
		t.Fatal("login changed lease identity", current)
	}
	current.SessionID, current.InstanceID = "linux:1001:123::10", strings.Repeat("a", 64)
	if err := db.MarkAgentGuestSessionReady(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	next, err := db.BeginAgentGuestLogin(t.Context(), current)
	if err != nil || next.LoginGeneration != 2 || next.LeaseID != current.LeaseID || next.SessionID != "" || next.InstanceID != "" || !next.ExpiresAt.Equal(current.ExpiresAt) {
		t.Fatal("same-lease reconnect version", next, err)
	}
	if err := db.QueueAgentGuestSessionRevocation(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	actual, err := db.DesktopLeaseByID(t.Context(), lease.ID)
	if err != nil || actual.State != "active" {
		t.Fatal("old login cleanup revoked new attempt", actual, err)
	}
	if err := db.MarkAgentGuestSessionReady(t.Context(), current); !errors.Is(err, ErrConflict) {
		t.Fatal("late ready overwrote reconnect", err)
	}
	for _, update := range []string{"login_generation=0", "login_generation=1", "login_generation=4", "login_generation=3,state='ready'"} {
		_, err := db.pool.Exec(t.Context(), `UPDATE agent_guest_sessions SET `+update+` WHERE desktop_vmid=9001`)
		var denied *pgconn.PgError
		if !errors.As(err, &denied) || denied.Code != "23514" {
			t.Fatal("SQL bypassed login ordering", update, err)
		}
	}
	if err := db.QueueAgentGuestSessionRevocation(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginAgentGuestLogin(t.Context(), next); !errors.Is(err, ErrConflict) {
		t.Fatal("closed lease issued login", err)
	}
	if err := db.CompleteAgentGuestSessionRevocation(t.Context(), current); !errors.Is(err, ErrConflict) {
		t.Fatal("old cleanup acknowledged current login", err)
	}
	if err := db.CompleteAgentGuestSessionRevocation(t.Context(), next); err != nil {
		t.Fatal(err)
	}
}

func TestAgentFailedProvisioningCanPinIdentityForRevocationButNotLogin(t *testing.T) {
	db, lease := agentSessionFixture(t)
	initial, err := db.BeginAgentGuestSession(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueueAgentGuestSessionRevocation(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	observed := initial
	observed.GuestUID = 1001
	if err := db.BindAgentGuestAccount(t.Context(), observed); !errors.Is(err, ErrConflict) {
		t.Fatal("closed lease authorized binding", err)
	}
	if err := db.BindRevokingAgentGuestAccount(t.Context(), observed); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginAgentGuestLogin(t.Context(), observed); !errors.Is(err, ErrConflict) {
		t.Fatal("cleanup identity opened login", err)
	}
	if err := db.CompleteAgentGuestSessionRevocation(t.Context(), initial); !errors.Is(err, ErrConflict) {
		t.Fatal("unbound cleanup receipt accepted after identity pin", err)
	}
	if err := db.CompleteAgentGuestSessionRevocation(t.Context(), observed); err != nil {
		t.Fatal(err)
	}
}
