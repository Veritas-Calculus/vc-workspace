package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/guestidentity"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

const SessionSchemaVersion = 2

// Includes first-login bootstrap and lock/QGA waits; the individual input
// worker retains its separate 250–15000 ms bound.
const APIActionTimeout = 65 * time.Second
const sessionBinary = "/usr/local/sbin/vc-workspace-guest-agent"
const sessionBase = "/var/lib/vc-workspace/computer-v2"
const authorityTransport = "stdin_epoch_account_v2"

type guestInputChannel interface {
	ExecGuestWithInput(context.Context, string, int, []string, []byte) (pve.GuestExecResult, error)
}

var managedSessionUserRE = regexp.MustCompile(`^(vca|vcw)[a-f0-9]{12}$`)

// SessionTarget is discovered from the mutually authenticated Guest endpoint,
// not from an MCP argument, display name, foreground window or cached DISPLAY.
type SessionTarget struct {
	Username   string `json:"username"`
	UID        uint32 `json:"uid"`
	SID        string `json:"sid,omitempty"`
	SessionID  string `json:"session_id"`
	InstanceID string `json:"instance_id"`
}

func (target SessionTarget) Validate() error {
	platform := "linux"
	if target.SID != "" {
		platform = "windows"
	}
	if !guestidentity.ValidSession(platform, target.Username, target.UID, target.SID, target.SessionID, target.InstanceID) {
		return fmt.Errorf("%w: invalid interactive session identity", ErrInvalid)
	}
	return nil
}

func validWindowsAccountSID(sid string) bool {
	return guestidentity.ValidWindowsAccountSID(sid)
}

// SessionExecutor is deliberately separate from the legacy Executor. Its
// caller must provision an actual interactive OS session and hold the VM's
// distributed control lock. No method falls back to the shared vdi Helper.
type SessionExecutor interface {
	DiscoverSession(context.Context, pve.VM, string) (SessionTarget, error)
	InitializeSessionTransport(context.Context, pve.VM, string) error
	ActivateSessionAuthority(context.Context, pve.VM, SessionTarget, Authority) error
	RevokeSessionAuthority(context.Context, pve.VM, Authority) error
	ExecuteForSession(context.Context, pve.VM, SessionTarget, Request, time.Duration) (Response, error)
}

type boundAction struct {
	SchemaVersion int           `json:"schema_version"`
	Target        SessionTarget `json:"target"`
	TimeoutMS     int64         `json:"timeout_ms"`
	Request       Request       `json:"request"`
}

type boundAuthority struct {
	SchemaVersion int            `json:"schema_version"`
	Target        *SessionTarget `json:"target"`
	Authority     Authority      `json:"authority"`
}

func (e *PVEExecutor) requireLinuxSession(ctx context.Context, machine pve.VM) error {
	if e == nil || e.guest == nil {
		return ErrUnavailable
	}
	configuration, err := e.guest.VMConfiguration(ctx, machine.Node, machine.VMID)
	if err != nil || configuration.OSType != "l26" {
		return fmt.Errorf("%w: session-bound transport requires a Linux Guest", ErrUnavailable)
	}
	return nil
}

func (e *PVEExecutor) sessionCommand(ctx context.Context, machine pve.VM, operation string, options ...string) (pve.GuestExecResult, error) {
	result, err := e.guest.ExecGuest(ctx, machine.Node, machine.VMID,
		append([]string{sessionBinary, "computer-v2-" + operation}, options...))
	if err != nil {
		return result, fmt.Errorf("%w: session transport: %v", ErrUnavailable, err)
	}
	if result.ExitCode != 0 {
		// Never copy Guest stdout/stderr (which can contain desktop content)
		// into HTTP errors, audit records or retry logs.
		return result, fmt.Errorf("%w: session %s failed with exit code %d", ErrUnavailable, operation, result.ExitCode)
	}
	return result, nil
}

func (e *PVEExecutor) InitializeSessionTransport(ctx context.Context, machine pve.VM, username string) error {
	if !managedSessionUserRE.MatchString(username) {
		return ErrInvalid
	}
	if err := e.requireLinuxSession(ctx, machine); err != nil {
		return err
	}
	_, err := e.sessionCommand(ctx, machine, "init", "--guest-user", username)
	return err
}

func (e *PVEExecutor) DiscoverSession(ctx context.Context, machine pve.VM, username string) (SessionTarget, error) {
	if !managedSessionUserRE.MatchString(username) {
		return SessionTarget{}, ErrInvalid
	}
	if err := e.requireLinuxSession(ctx, machine); err != nil {
		return SessionTarget{}, err
	}
	result, err := e.sessionCommand(ctx, machine, "session", "--guest-user", username)
	if err != nil {
		return SessionTarget{}, err
	}
	var response struct {
		SchemaVersion      int           `json:"schema_version"`
		Target             SessionTarget `json:"target"`
		AuthorityTransport string        `json:"authority_transport"`
	}
	if len(result.Stdout) > 4096 || json.Unmarshal([]byte(result.Stdout), &response) != nil ||
		response.SchemaVersion != SessionSchemaVersion || response.AuthorityTransport != authorityTransport || response.Target.Username != username || response.Target.Validate() != nil || response.Target.SID != "" {
		return SessionTarget{}, fmt.Errorf("%w: Helper session identity mismatch", ErrUnavailable)
	}
	return response.Target, nil
}

func (e *PVEExecutor) ActivateSessionAuthority(ctx context.Context, machine pve.VM, target SessionTarget, authority Authority) error {
	authority.State = "active"
	if target.Validate() != nil || target.SID != "" || !validAuthority(authority) || authority.ExpiresUnixMS <= time.Now().UnixMilli() {
		return ErrInvalid
	}
	return e.writeSessionAuthority(ctx, machine, boundAuthority{SessionSchemaVersion, &target, authority})
}

func (e *PVEExecutor) RevokeSessionAuthority(ctx context.Context, machine pve.VM, authority Authority) error {
	// Revocation must not depend on the control-plane/Guest wall-clock offset.
	// Zero is the canonical expired deadline; the epoch still fences ordering.
	authority.State, authority.ExpiresUnixMS = "revoked", 0
	if !validAuthority(authority) {
		return ErrInvalid
	}
	return e.writeSessionAuthority(ctx, machine, boundAuthority{SessionSchemaVersion, nil, authority})
}

func (e *PVEExecutor) writeSessionAuthority(ctx context.Context, machine pve.VM, authority boundAuthority) error {
	if err := e.requireLinuxSession(ctx, machine); err != nil {
		return err
	}
	channel, ok := e.guest.(guestInputChannel)
	if !ok {
		return ErrUnavailable
	}
	capability, err := e.sessionCommand(ctx, machine, "capabilities")
	if err != nil {
		return err
	}
	var supported struct {
		SchemaVersion      int    `json:"schema_version"`
		AuthorityTransport string `json:"authority_transport"`
	}
	if len(capability.Stdout) > 4096 || json.Unmarshal([]byte(capability.Stdout), &supported) != nil || supported.SchemaVersion != SessionSchemaVersion || supported.AuthorityTransport != authorityTransport {
		return fmt.Errorf("%w: Guest authority fencing upgrade required", ErrUnavailable)
	}
	encoded, err := pve.MarshalGuestJSON(authority)
	if err != nil {
		return err
	}
	// One bounded input, no shared path or authorization auto-retry. A lost
	// result is uncertain; the caller's durable cleanup closes this lease.
	result, err := channel.ExecGuestWithInput(ctx, machine.Node, machine.VMID, []string{sessionBinary, "computer-v2-authority"}, encoded)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("%w: fenced authority publication failed", ErrUnavailable)
	}
	return nil
}

func (e *PVEExecutor) ExecuteForSession(ctx context.Context, machine pve.VM, target SessionTarget, request Request, timeout time.Duration) (Response, error) {
	if target.Validate() != nil || target.SID != "" || timeout < 250*time.Millisecond || timeout > 15*time.Second {
		return Response{}, ErrInvalid
	}
	if err := request.Validate(time.Now()); err != nil {
		return Response{}, err
	}
	if request.ExpiresUnixMS > time.Now().Add(timeout+250*time.Millisecond).UnixMilli() {
		return Response{}, ErrInvalid
	}
	if err := e.requireLinuxSession(ctx, machine); err != nil {
		return Response{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	request.ExpiresUnixMS = time.Now().Add(timeout + 5*time.Second).UnixMilli()
	encoded, err := json.Marshal(boundAction{SessionSchemaVersion, target, timeout.Milliseconds(), request})
	if err != nil || len(encoded) > 60*1024 {
		return Response{}, ErrInvalid
	}
	options := []string{"--guest-user", target.Username, "--request-id", request.RequestID}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = e.sessionCommand(cleanup, machine, "cleanup", options...)
	}()
	// Re-stage identical bytes on an uncertain publish so the root dispatcher
	// compares the existing immutable request. Reusing an ID for new data fails.
	err = retryGuestOperation(ctx, 5*time.Second, func() error {
		if _, err := e.sessionCommand(ctx, machine, "stage", options...); err != nil {
			return err
		}
		if err := e.guest.WriteGuestFile(ctx, machine.Node, machine.VMID, sessionBase+"/inbox/"+request.RequestID+".json.tmp", string(encoded)); err != nil {
			return err
		}
		_, err := e.sessionCommand(ctx, machine, "publish", options...)
		return err
	})
	if err != nil {
		return Response{}, fmt.Errorf("%w: immutable session request unavailable", ErrUnavailable)
	}
	var result pve.GuestExecResult
	err = retryGuestOperation(ctx, 5*time.Second, func() error {
		var err error
		result, err = e.sessionCommand(ctx, machine, "dispatch", options...)
		return err
	})
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return Response{}, ErrTimeout
	}
	if err != nil {
		return Response{}, err
	}
	var response Response
	if len(result.Stdout) > 16*1024*1024 || json.Unmarshal([]byte(result.Stdout), &response) != nil ||
		response.SchemaVersion != SchemaVersion || response.RequestID != request.RequestID {
		return Response{}, fmt.Errorf("%w: invalid session response", ErrUnavailable)
	}
	if !response.OK {
		return Response{}, errors.New("interactive session rejected the action")
	}
	return response, nil
}

var _ SessionExecutor = (*PVEExecutor)(nil)
