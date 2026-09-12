package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

const windowsTestCaps = `{"schema_version":2,"experimental_session_transport":"named_pipe_sid","session_identity":"sid_wts_luid","authority_store":"protected_registry","experimental_account_lifecycle":"system_owned_sam_v1","computer_actions":false,"unattended_login":false}`

type windowsExecutorGuest struct {
	fakeGuestChannel
	target                 SessionTarget
	caps, session, account string
	fail                   string
	input                  [][]byte
	lost, denied, wrongID  bool
	dispatches             int
	lostRead               string
	readAttempts           map[string]int
}

func newWindowsExecutorGuest() *windowsExecutorGuest {
	return &windowsExecutorGuest{fakeGuestChannel: fakeGuestChannel{configuration: pve.VMConfiguration{OSType: "win11"}}, target: SessionTarget{"vca0123456789ab", 0, "S-1-5-21-1-2-3-1001", "windows:2:0000000000000001", strings.Repeat("a", 64)}, caps: windowsTestCaps}
}
func (g *windowsExecutorGuest) ExecGuest(_ context.Context, _ string, _ int, args []string) (pve.GuestExecResult, error) {
	g.commands = append(g.commands, slices.Clone(args))
	if len(args) < 2 || args[0] != windowsSessionBinary || !strings.HasPrefix(args[1], "computer-v2-") {
		return pve.GuestExecResult{}, errors.New("unexpected command")
	}
	op := strings.TrimPrefix(args[1], "computer-v2-")
	if op == g.lostRead {
		if g.readAttempts == nil {
			g.readAttempts = map[string]int{}
		}
		g.readAttempts[op]++
		if g.readAttempts[op] == 1 {
			return pve.GuestExecResult{}, errors.New("PVE returned 500: Agent error: PID lld does not exist private-sentinel")
		}
	}
	if op == g.fail {
		return pve.GuestExecResult{ExitCode: 1, Stderr: "private-desktop-sentinel"}, nil
	}
	var data any
	switch op {
	case "capabilities":
		return pve.GuestExecResult{Stdout: g.caps}, nil
	case "session":
		if g.session != "" {
			return pve.GuestExecResult{Stdout: g.session}, nil
		}
		data = map[string]any{"schema_version": 2, "target": g.target, "input_ready": true}
	case "account-inspect":
		if g.account != "" {
			return pve.GuestExecResult{Stdout: g.account}, nil
		}
		data = map[string]any{"schema_version": 1, "username": g.target.Username, "sid": g.target.SID, "disabled": true}
	case "init":
		return pve.GuestExecResult{}, nil
	default:
		return pve.GuestExecResult{}, errors.New("unexpected operation")
	}
	raw, _ := json.Marshal(data)
	return pve.GuestExecResult{Stdout: string(raw)}, nil
}
func (g *windowsExecutorGuest) ExecGuestWithInput(_ context.Context, _ string, _ int, args []string, payload []byte) (pve.GuestExecResult, error) {
	g.commands = append(g.commands, slices.Clone(args))
	g.input = append(g.input, slices.Clone(payload))
	if len(args) < 2 || args[0] != windowsSessionBinary {
		return pve.GuestExecResult{}, errors.New("wrong binary")
	}
	if args[1] == "computer-v2-authority" {
		if g.lost {
			return pve.GuestExecResult{}, errors.New("QGA command timeout private-sentinel")
		}
		return pve.GuestExecResult{}, nil
	}
	if args[1] != "computer-v2-dispatch" {
		return pve.GuestExecResult{}, errors.New("wrong command")
	}
	g.dispatches++
	if g.lost && g.dispatches == 1 {
		payload[0] = '!'
		return pve.GuestExecResult{}, errors.New("PVE returned 500: Agent error: PID 123 does not exist")
	}
	if g.denied {
		return pve.GuestExecResult{ExitCode: 1, Stderr: "private-desktop-sentinel"}, nil
	}
	var action boundAction
	if json.Unmarshal(payload, &action) != nil {
		return pve.GuestExecResult{}, errors.New("bad action")
	}
	id := action.Request.RequestID
	if g.wrongID {
		id += "wrong"
	}
	raw, _ := json.Marshal(Response{SchemaVersion: 1, RequestID: id, OK: true, Input: &InputResult{Applied: true}})
	return pve.GuestExecResult{Stdout: string(raw)}, nil
}

func TestWindowsExecutorProvesProtocolAndAccountThenDiscoversIdentity(t *testing.T) {
	g := newWindowsExecutorGuest()
	e := NewWindowsSessionExecutor(g)
	vm := pve.VM{Node: "fixture", VMID: 9113}
	if err := e.InitializeSessionTransport(t.Context(), vm, g.target.Username); err != nil {
		t.Fatal(err)
	}
	if len(g.commands) != 3 || g.commands[1][1] != "computer-v2-account-inspect" || g.commands[2][1] != "computer-v2-init" {
		t.Fatal("initialization bypassed account proof")
	}
	target, err := e.DiscoverSession(t.Context(), vm, g.target.Username)
	if err != nil || target != g.target {
		t.Fatal("wrong discovery", err)
	}
	if len(g.writtenPaths) != 0 {
		t.Fatal("shared file transport used")
	}
	for _, cmd := range g.commands {
		if slices.Contains(cmd, "computer-v2-account-provision") {
			t.Fatal("data plane silently provisioned an account")
		}
	}
}

func TestWindowsExecutorRefusesIncompatibleAndUnreadyGuests(t *testing.T) {
	for _, mode := range []string{"linux", "legacy", "caps_malformed", "caps_large", "locked", "wrong_sid", "wrong_user", "wrong_schema", "account_unowned", "account_wrong_sid", "account_missing_status"} {
		t.Run(mode, func(t *testing.T) {
			g := newWindowsExecutorGuest()
			e := NewWindowsSessionExecutor(g)
			initialize := false
			switch mode {
			case "linux":
				g.configuration.OSType = "l26"
			case "legacy":
				g.caps = `{"schema_version":1}`
			case "caps_malformed":
				g.caps = "invalid"
			case "caps_large":
				g.caps = strings.Repeat(" ", 4097) + windowsTestCaps
			case "locked":
				raw, _ := json.Marshal(map[string]any{"schema_version": 2, "target": g.target, "input_ready": false})
				g.session = string(raw)
			case "wrong_sid":
				g.target.SID = "S-1-5-18"
			case "wrong_user":
				g.target.Username = "vca111111111111"
			case "wrong_schema":
				g.session = `{"schema_version":1}`
			case "account_unowned":
				initialize = true
				g.fail = "account-inspect"
			case "account_wrong_sid":
				initialize = true
				g.target.SID = "S-1-5-18"
			case "account_missing_status":
				initialize = true
				g.account = `{"schema_version":1,"username":"vca0123456789ab","sid":"S-1-5-21-1-2-3-1001"}`
			}
			var err error
			if initialize {
				err = e.InitializeSessionTransport(t.Context(), pve.VM{}, "vca0123456789ab")
			} else {
				_, err = e.DiscoverSession(t.Context(), pve.VM{}, "vca0123456789ab")
			}
			if err == nil || strings.Contains(err.Error(), "sentinel") {
				t.Fatal("unsafe rejection", err)
			}
			for _, cmd := range g.commands {
				if slices.Contains(cmd, "computer-v2-init") {
					t.Fatal("unproven identity initialized")
				}
			}
		})
	}
}

func TestWindowsExecutorAuthorityAndImmutableReplay(t *testing.T) {
	g := newWindowsExecutorGuest()
	e := NewWindowsSessionExecutor(g)
	vm := pve.VM{}
	authority := Authority{SchemaVersion: 1, LeaseID: "lease_test_fixture", ControlEpoch: 2, State: "active", ExpiresUnixMS: time.Now().Add(time.Minute).UnixMilli()}
	if err := e.ActivateSessionAuthority(t.Context(), vm, g.target, authority); err != nil {
		t.Fatal(err)
	}
	var grant boundAuthority
	if json.Unmarshal(g.input[0], &grant) != nil || grant.Target == nil || *grant.Target != g.target || grant.Authority != authority {
		t.Fatal("wrong bound authority")
	}
	if err := e.RevokeSessionAuthority(t.Context(), vm, authority); err != nil {
		t.Fatal(err)
	}
	var revoked boundAuthority
	if json.Unmarshal(g.input[1], &revoked) != nil || revoked.Target != nil || revoked.Authority.State != "revoked" || revoked.Authority.ControlEpoch != 2 || revoked.Authority.ExpiresUnixMS != 0 {
		t.Fatal("wrong tombstone")
	}
	g.input = nil
	g.lost = true
	r := validRequest(time.Now())
	r.Operation = OperationTypeText
	r.Screenshot = nil
	r.Text = &Text{Value: "private-input-sentinel 中文🙂"}
	r.ExpiresUnixMS = time.Now().Add(5 * time.Second).UnixMilli()
	result, err := e.ExecuteForSession(t.Context(), vm, g.target, r, 5*time.Second)
	if err != nil || !result.OK || g.dispatches != 2 || !bytes.Equal(g.input[0], g.input[1]) {
		t.Fatal("exact action replay failed", err)
	}
	var action boundAction
	if json.Unmarshal(g.input[0], &action) != nil || action.Target != g.target || action.Request.ExpiresUnixMS != r.ExpiresUnixMS || action.TimeoutMS != 5000 {
		t.Fatal("action binding/deadline changed")
	}
	if bytes.Contains(g.input[0], []byte("中文")) {
		t.Fatal("QGA envelope must escape non-ASCII safely")
	}
	for _, cmd := range g.commands {
		if strings.Contains(strings.Join(cmd, " "), "private-input-sentinel") {
			t.Fatal("input leaked into argv")
		}
	}
	g.input = nil
	if err := e.ActivateSessionAuthority(t.Context(), vm, g.target, authority); err == nil || len(g.input) != 1 || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("uncertain authority retried or exposed", err)
	}
}

func TestWindowsExecutorRejectsActionsWithoutFallback(t *testing.T) {
	for _, mode := range []string{"linux_target", "human_target", "deadline", "timeout", "denied", "wrong_id"} {
		t.Run(mode, func(t *testing.T) {
			g := newWindowsExecutorGuest()
			e := NewWindowsSessionExecutor(g)
			target := g.target
			r := validRequest(time.Now())
			r.ExpiresUnixMS = time.Now().Add(5 * time.Second).UnixMilli()
			timeout := 5 * time.Second
			switch mode {
			case "linux_target":
				target = testSessionTarget()
			case "human_target":
				target.Username = "vcw0123456789ab"
			case "deadline":
				r.ExpiresUnixMS = time.Now().Add(-time.Second).UnixMilli()
			case "timeout":
				timeout = 16 * time.Second
			case "denied":
				g.denied = true
			case "wrong_id":
				g.wrongID = true
			}
			_, err := e.ExecuteForSession(t.Context(), pve.VM{}, target, r, timeout)
			if err == nil || strings.Contains(err.Error(), "sentinel") {
				t.Fatal("unsafe action accepted", err)
			}
			if g.dispatches > 1 {
				t.Fatal("explicit rejection retried")
			}
			if len(g.writtenPaths) != 0 {
				t.Fatal("fell back to shared spool")
			}
		})
	}
}

func TestWindowsExecutorRequiresMatchingSuccessPayload(t *testing.T) {
	for _, operation := range []Operation{OperationScreenshot, OperationAccessibility, OperationMouse, OperationKey, OperationTypeText} {
		t.Run(string(operation), func(t *testing.T) {
			request := Request{RequestID: "action_fixture", Operation: operation}
			good := func() Response {
				r := Response{SchemaVersion: 1, RequestID: request.RequestID, OK: true}
				switch operation {
				case OperationScreenshot:
					r.Screenshot = &ScreenshotResult{ContentType: "image/jpeg", Data: "not-yet-decoded", Width: 640, Height: 360}
				case OperationAccessibility:
					r.Accessibility = &AccessibilityResult{Source: "windows_uia"}
				default:
					r.Input = &InputResult{Applied: true}
				}
				return r
			}
			if !validWindowsActionResponse(request, good()) {
				t.Fatal("matching payload rejected")
			}
			if operation == OperationAccessibility {
				r := good()
				r.Accessibility.Source = "window_enumeration"
				if !validWindowsActionResponse(request, r) {
					t.Fatal("documented reduced fallback rejected")
				}
			}
			for _, change := range []func(*Response){
				func(r *Response) { r.SchemaVersion = 2 },
				func(r *Response) { r.RequestID += "wrong" },
				func(r *Response) { r.OK = false },
				func(r *Response) { r.Error = "private-sentinel" },
				func(r *Response) { r.Screenshot, r.Accessibility, r.Input = nil, nil, nil },
				func(r *Response) { r.Screenshot = &ScreenshotResult{}; r.Input = &InputResult{Applied: true} },
				func(r *Response) {
					if r.Input != nil {
						r.Input.Applied = false
					}
					if r.Accessibility != nil {
						r.Accessibility.Source = "at_spi"
					}
					if r.Screenshot != nil {
						r.Screenshot.ContentType = "text/html"
					}
				},
			} {
				r := good()
				change(&r)
				if validWindowsActionResponse(request, r) {
					t.Fatal("mismatched or contradictory success accepted")
				}
			}
		})
	}
}

func TestWindowsExecutorErrorsExposeStageNotGuestContent(t *testing.T) {
	for _, operation := range []string{"capabilities", "account-inspect", "init", "session"} {
		t.Run(operation, func(t *testing.T) {
			g := newWindowsExecutorGuest()
			g.fail = operation
			e := NewWindowsSessionExecutor(g)
			var err error
			if operation == "session" {
				_, err = e.DiscoverSession(t.Context(), pve.VM{}, g.target.Username)
			} else {
				err = e.InitializeSessionTransport(t.Context(), pve.VM{}, g.target.Username)
			}
			if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), operation+" exit 1") || strings.Contains(err.Error(), "sentinel") {
				t.Fatal("missing safe operation stage", err)
			}
		})
	}
}

func TestWindowsReadOnlyObservationPreservesAbsenceAndBounds(t *testing.T) {
	uncertain := errors.New("PVE returned 500: qga command 'guest-exec' failed - got timeout")
	absent := pve.GuestExecResult{ExitCode: 1, Stderr: "managed user has no logged-on session"}
	for _, test := range []struct {
		name     string
		failures int
		want     int
	}{
		{"explicit-absence", 0, 1}, {"lost-read-then-absence", 1, 2}, {"bounded-uncertain", 5, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			result, err := readWindowsGuestResult(t.Context(), func(context.Context) (pve.GuestExecResult, error) {
				calls++
				if calls <= test.failures {
					return pve.GuestExecResult{}, uncertain
				}
				return absent, nil
			})
			if calls != test.want {
				t.Fatal("unexpected read count", calls, err)
			}
			if test.failures < 3 && (err != nil || result.ExitCode != 1 || result.Stderr != absent.Stderr) {
				t.Fatal("explicit Guest absence receipt lost", result, err)
			}
			if test.failures >= 3 && !errors.Is(err, uncertain) {
				t.Fatal("transport uncertainty was treated as absence", err)
			}
		})
	}
	t.Run("caller-deadline-rejects-late-success", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		calls := 0
		_, err := readWindowsGuestResult(ctx, func(call context.Context) (pve.GuestExecResult, error) {
			calls++
			<-call.Done()
			return absent, nil
		})
		if calls != 1 || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("read extended the caller deadline or accepted a late receipt", calls, err)
		}
	})
}

func TestWindowsObservationTimeoutLeavesRoomForSameResultRecovery(t *testing.T) {
	for _, actionMode := range []bool{false, true} {
		name := "fixed-read-only"
		if actionMode {
			name = "immutable-bound-action"
		}
		t.Run(name, func(t *testing.T) {
			g := newWindowsExecutorGuest()
			request := validRequest(time.Now())
			request.ExpiresUnixMS = time.Now().Add(15 * time.Second).UnixMilli()
			action := boundAction{2, g.target, 15000, request}
			ctx, cancel := context.WithDeadline(t.Context(), time.UnixMilli(request.ExpiresUnixMS))
			defer cancel()
			var first []byte
			calls := 0
			send := func(observation context.Context, payload []byte) (pve.GuestExecResult, error) {
				calls++
				deadline, ok := observation.Deadline()
				if !ok || deadline.After(time.Now().Add(windowsGuestObservationTimeout)) || !deadline.Before(time.UnixMilli(request.ExpiresUnixMS)) {
					t.Fatal("one observation consumed the entire original authority window")
				}
				if calls == 1 {
					first = bytes.Clone(payload)
					if len(payload) > 0 {
						payload[0] = '!'
					}
					<-observation.Done()
					return pve.GuestExecResult{}, observation.Err()
				}
				if !bytes.Equal(first, payload) {
					t.Fatal("result recovery changed the immutable action")
				}
				return pve.GuestExecResult{Stdout: "same-result"}, nil
			}
			var result pve.GuestExecResult
			var err error
			if actionMode {
				result, err = replayBoundWindowsAction(ctx, action, send)
			} else {
				result, err = readWindowsGuestResult(ctx, func(call context.Context) (pve.GuestExecResult, error) {
					return send(call, nil)
				})
			}
			if err != nil || calls != 2 || result.Stdout != "same-result" || ctx.Err() != nil {
				t.Fatal("same-result recovery failed within the original deadline", calls, err)
			}
		})
	}
}

func TestWindowsActionObservationNeverReplaysAfterCallerCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		g := newWindowsExecutorGuest()
		request := validRequest(time.Now())
		request.ExpiresUnixMS = time.Now().Add(15 * time.Second).UnixMilli()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		calls := 0
		_, err := replayBoundWindowsAction(ctx, boundAction{2, g.target, 15000, request}, func(call context.Context, _ []byte) (pve.GuestExecResult, error) {
			calls++
			if cancelCaller {
				cancel()
			}
			<-call.Done()
			// A faulty transport's late success cannot resurrect a canceled call.
			return pve.GuestExecResult{Stdout: "too-late"}, nil
		})
		cancel()
		if calls != 1 || (cancelCaller && !errors.Is(err, context.Canceled)) || (!cancelCaller && !errors.Is(err, context.DeadlineExceeded)) {
			t.Fatal("canceled action replayed or late receipt accepted", calls, err)
		}
	}
}

func TestWindowsExecutorOnlyRetriesKnownReadOnlyDiscovery(t *testing.T) {
	for _, operation := range []string{"capabilities", "account-inspect", "session", "init"} {
		t.Run(operation, func(t *testing.T) {
			g := newWindowsExecutorGuest()
			g.lostRead = operation
			e := NewWindowsSessionExecutor(g)
			var err error
			if operation == "session" {
				_, err = e.DiscoverSession(t.Context(), pve.VM{}, g.target.Username)
			} else {
				err = e.InitializeSessionTransport(t.Context(), pve.VM{}, g.target.Username)
			}
			if operation == "init" {
				if !errors.Is(err, ErrUnavailable) || g.readAttempts[operation] != 1 {
					t.Fatal("uncertain initialization replayed", err)
				}
			} else if err != nil || g.readAttempts[operation] != 2 {
				t.Fatal("bounded read-only recovery failed", err)
			}
		})
	}
}

type windowsBlockingProbeGuest struct{ *windowsExecutorGuest }

func (g windowsBlockingProbeGuest) VMConfiguration(ctx context.Context, _ string, _ int) (pve.VMConfiguration, error) {
	<-ctx.Done()
	return pve.VMConfiguration{}, ctx.Err()
}

func TestWindowsExecutorProbeSharesOriginalActionDeadline(t *testing.T) {
	g := windowsBlockingProbeGuest{newWindowsExecutorGuest()}
	e := NewWindowsSessionExecutor(g)
	r := validRequest(time.Now())
	r.ExpiresUnixMS = time.Now().Add(250 * time.Millisecond).UnixMilli()
	started := time.Now()
	_, err := e.ExecuteForSession(t.Context(), pve.VM{}, g.target, r, time.Second)
	if !errors.Is(err, ErrTimeout) || time.Since(started) > 2*time.Second || len(g.commands) != 0 {
		t.Fatal("probe outlived the original action deadline or dispatched input", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r.ExpiresUnixMS = time.Now().Add(time.Second).UnixMilli()
	if _, err := e.ExecuteForSession(ctx, pve.VM{}, g.target, r, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal("caller cancellation was hidden", err)
	}
}
