package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCloneFenceGuardsNativeBindingAndCredentialReservation(t *testing.T) {
	for _, state := range []string{"accepted", "running", "failed", "succeeded"} {
		t.Run(state, func(t *testing.T) {
			db, identity, digest := nativeAccountFixture(t, "linux")
			// Pin the independently verified identity before introducing the
			// clone fence, so reservation cannot bypass it via an existing row.
			bound, err := db.BindNativeGuestAccount(t.Context(), identity, digest)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = db.CreateJob(t.Context(), Job{ID: "native-clone", IdempotencyKey: "native-clone", Operation: "pve.template_clone", State: state, SourceVMID: 9000, TargetVMID: 9001, Request: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			_, bindErr := db.BindNativeGuestAccount(t.Context(), identity, digest)
			_, issueErr := db.BeginNativeGuestCredential(t.Context(), bound, "conn_clone_fence_test", time.Now().Add(time.Hour).Truncate(time.Second), digest)
			if state == "succeeded" {
				if bindErr != nil || issueErr != nil {
					t.Fatalf("completed clone refused: bind=%v issue=%v", bindErr, issueErr)
				}
			} else {
				if !errors.Is(bindErr, ErrConflict) || !errors.Is(issueErr, ErrConflict) {
					t.Fatalf("unfinished clone admitted: bind=%v issue=%v", bindErr, issueErr)
				}
				unchanged, err := db.NativeGuestAccount(t.Context(), identity.DesktopVMID, identity.UserID)
				if err != nil || unchanged.Revision != 0 || unchanged.Operation != "none" || unchanged.State != "idle" {
					t.Fatalf("denial reserved credentials: %+v %v", unchanged, err)
				}
			}
		})
	}
}

func TestCloneAccessFenceSurvivesInventoryAndOpensOnlyOnSuccess(t *testing.T) {
	db, a, _ := nativeAccountFixture(t, "linux")
	if _, err := db.CreateAgentPrincipal(t.Context(), AgentPrincipal{ID: "clone-agent", DisplayName: "Clone agent"}, []byte("clone-agent-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(t.Context(), DesktopAssignment{SubjectType: "agent", SubjectID: "clone-agent", DesktopVMID: 9001}); err != nil {
		t.Fatal(err)
	}
	job, _, err := db.CreateJob(t.Context(), Job{ID: "clone-access", IdempotencyKey: "clone-access", Operation: "pve.template_clone", State: "accepted", SourceVMID: 9000, TargetVMID: 9001, Request: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"accepted", "running", "failed", "succeeded"} {
		if err := db.UpdateJobTask(t.Context(), job.ID, state, "fixture-task", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.ReconcileManagedDesktops(t.Context(), []ManagedDesktop{{VMID: 9001, DisplayName: "Clone", Node: "test", OSFamily: "linux"}}); err != nil {
			t.Fatal(err)
		}
		want := state == "succeeded"
		if got, err := db.UserCanAccessDesktop(t.Context(), a.UserID, 9001); err != nil || got != want {
			t.Fatalf("%s user access=%v error=%v", state, got, err)
		}
		if got, err := db.AgentCanAccessDesktop(t.Context(), "clone-agent", 9001); err != nil || got != want {
			t.Fatalf("%s agent access=%v error=%v", state, got, err)
		}
		if ids, err := db.AgentAssignedDesktopVMIDs(t.Context(), "clone-agent"); err != nil || (len(ids) == 1) != want {
			t.Fatalf("%s agent listing=%v error=%v", state, ids, err)
		}
		_, err := db.CreateDesktopLease(t.Context(), DesktopLease{ID: "clone-lease-" + state, AgentID: "clone-agent", DesktopID: "9001", ExpiresAt: time.Now().Add(time.Hour)})
		if (err == nil) != want {
			t.Fatalf("%s lease error=%v", state, err)
		}
	}
}
