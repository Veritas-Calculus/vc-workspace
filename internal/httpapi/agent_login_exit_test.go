package httpapi

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
)

func exitObservation(intent computer.AccountLease, writersAbsent bool) computer.LinuxAccountObservation {
	identity, lease := intent.Identity, intent
	lease.Phase = "sealed"
	absent, day := true, uint64((intent.ExpiresUnixSeconds+86399)/86400)
	return computer.LinuxAccountObservation{Identity: &identity, Exists: true, ExpiryDay: &day,
		ProcessesAbsent: &absent, LoginWritersAbsent: &writersAbsent, Lifecycle: &lease}
}

func TestAgentLoginExitWaitIsReadOnlyBoundedAndVersionPinned(t *testing.T) {
	for _, mode := range []string{"closed", "settled", "timeout", "cancel", "expired", "error", "uid", "generation", "revoked", "deleted", "disabled", "process", "unknown", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				intent := computer.AccountLease{SchemaVersion: 1,
					Identity: computer.AccountIdentity{Username: "vca123456789abc", UID: 1001},
					LeaseID:  "lease_exit", ControlEpoch: 1, LoginGeneration: 2, ExpiresUnixSeconds: time.Now().Add(time.Minute).Unix()}
				if mode == "expired" {
					intent.ExpiresUnixSeconds = time.Now().Add(time.Second).Unix()
				}
				ctx := t.Context()
				if mode == "cancel" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
					defer cancel()
				}
				calls, started := 0, time.Now()
				failure := errors.New("inspection failed")
				err := awaitAgentLoginExit(ctx, intent, exitObservation(intent, mode == "closed"), func(read context.Context) (computer.LinuxAccountObservation, error) {
					calls++
					if deadline, ok := read.Deadline(); !ok || deadline.After(started.Add(15*time.Second)) {
						t.Fatal("inspection lost the bounded caller context")
					}
					value := exitObservation(intent, true)
					switch mode {
					case "closed", "cancel":
						t.Fatal("unexpected inspection")
					case "settled":
						*value.LoginWritersAbsent = calls >= 2
					case "timeout", "expired":
						*value.LoginWritersAbsent = false
					case "error":
						return value, failure
					case "uid":
						value.Identity.UID++
					case "generation":
						value.Lifecycle.LoginGeneration++
					case "revoked":
						value.Lifecycle.Phase = "revoked"
					case "deleted":
						value.Exists = false
					case "disabled":
						value.Disabled = true
					case "process":
						*value.ProcessesAbsent = false
					case "unknown":
						value.LoginWritersAbsent = nil
					case "expiry":
						*value.ExpiryDay++
					}
					return value, nil
				})
				if mode == "closed" || mode == "settled" {
					if err != nil || (mode == "closed" && calls != 0) || (mode == "settled" && calls != 2) {
						t.Fatal("confirmed closure did not settle", err, calls)
					}
				} else if err == nil {
					t.Fatal("uncertain/changed login was admitted")
				}
				if mode == "timeout" && (!errors.Is(err, context.DeadlineExceeded) || time.Since(started) != 15*time.Second) {
					t.Fatal("internal timeout did not bound recovery", err, time.Since(started))
				}
				if mode == "cancel" && (!errors.Is(err, context.DeadlineExceeded) || time.Since(started) != 100*time.Millisecond) {
					t.Fatal("caller deadline was extended", err)
				}
				if mode == "error" && !errors.Is(err, failure) {
					t.Fatal("inspection error was hidden", err)
				}
			})
		})
	}
}
