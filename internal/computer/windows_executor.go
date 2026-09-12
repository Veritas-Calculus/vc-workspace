package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

const windowsSessionBinary = `C:\Program Files\VC Workspace\Agent\vc-workspace-guest-agent.exe`

// A stalled QGA result read must leave room to retrieve the same response.
// This bounds transport observation, NOT the Guest action or its authority.
const windowsGuestObservationTimeout = 5 * time.Second

// WindowsSessionExecutor is the SID-bound data plane, not an account/login
// broker. The default PVE/HTTP executor remains Linux-only until the Windows
// lease, transport ownership and installation/recovery lifecycle is accepted.
// Callers must hold the same distributed VM gate as for SessionExecutor.
type WindowsSessionExecutor struct {
	guest  GuestChannel
	binary string // fixed deployment path, never an HTTP/MCP argument
}

func NewWindowsSessionExecutor(guest GuestChannel) *WindowsSessionExecutor {
	return &WindowsSessionExecutor{guest: guest, binary: windowsSessionBinary}
}

func (e *WindowsSessionExecutor) command(ctx context.Context, machine pve.VM, operation string, payload []byte, options ...string) (pve.GuestExecResult, error) {
	args := append([]string{e.binary, "computer-v2-" + operation}, options...)
	var result pve.GuestExecResult
	var err error
	// These fixed commands only read protocol/owned identity state. A lost QGA
	// PID result permits another read, not a replay of init/authority/lifecycle.
	readOnly := payload == nil && (operation == "capabilities" || operation == "account-inspect" || operation == "session")
	send := func(ctx context.Context) (pve.GuestExecResult, error) {
		if payload == nil {
			return e.guest.ExecGuest(ctx, machine.Node, machine.VMID, args)
		}
		channel, ok := e.guest.(guestInputChannel)
		if !ok {
			return pve.GuestExecResult{}, ErrUnavailable
		}
		return channel.ExecGuestWithInput(ctx, machine.Node, machine.VMID, args, payload)
	}
	if readOnly {
		result, err = readWindowsGuestResult(ctx, send)
	} else if ctx.Err() != nil {
		err = ctx.Err()
	} else {
		result, err = send(ctx)
	}
	if err != nil {
		return result, fmt.Errorf("%w: Windows %s transport", ErrUnavailable, operation)
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("%w: Windows %s exit %d", ErrUnavailable, operation, result.ExitCode)
	}
	return result, nil
}

// Only for a separately proven read-only command. Preserve actual nonzero
// Guest receipts (including WTS absence) and the caller's shorter deadline;
// transport uncertainty is never evidence that an account/session is absent.
func readWindowsGuestResult(ctx context.Context, send func(context.Context) (pve.GuestExecResult, error)) (pve.GuestExecResult, error) {
	ctx, cancel := context.WithTimeout(ctx, guestTransportRetryBudget)
	defer cancel()
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return pve.GuestExecResult{}, err
		}
		observation, stop := context.WithTimeout(ctx, windowsGuestObservationTimeout)
		result, err := send(observation)
		observationExpired := errors.Is(observation.Err(), context.DeadlineExceeded)
		stop()
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		uncertain := windowsGuestResultUncertain(err) || (observationExpired && errors.Is(err, context.DeadlineExceeded))
		if err == nil || attempt == 2 || !uncertain {
			return result, err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}

func (e *WindowsSessionExecutor) probe(ctx context.Context, machine pve.VM) error {
	if e == nil || e.guest == nil || e.binary == "" {
		return ErrUnavailable
	}
	configuration, err := e.guest.VMConfiguration(ctx, machine.Node, machine.VMID)
	if err != nil {
		return fmt.Errorf("%w: Windows configuration transport", ErrUnavailable)
	}
	if !strings.HasPrefix(configuration.OSType, "win") {
		return fmt.Errorf("%w: Windows OS required", ErrUnavailable)
	}
	if _, ok := e.guest.(guestInputChannel); !ok {
		return ErrUnavailable
	}
	result, err := e.command(ctx, machine, "capabilities", nil)
	if err != nil {
		return err
	}
	var capabilities struct {
		Version   int    `json:"schema_version"`
		Transport string `json:"experimental_session_transport"`
		Identity  string `json:"session_identity"`
		Authority string `json:"authority_store"`
		Accounts  string `json:"experimental_account_lifecycle"`
	}
	if len(result.Stdout) > 4096 || json.Unmarshal([]byte(result.Stdout), &capabilities) != nil ||
		capabilities.Version != 2 || capabilities.Transport != "named_pipe_sid" || capabilities.Identity != "sid_wts_luid" ||
		capabilities.Authority != "protected_registry" || capabilities.Accounts != "system_owned_sam_v1" {
		return fmt.Errorf("%w: Windows session protocol upgrade required", ErrUnavailable)
	}
	return nil
}

func validWindowsAgentTarget(target SessionTarget) bool {
	return validAgentUser(target.Username) && target.SID != "" && target.Validate() == nil
}

func (e *WindowsSessionExecutor) InitializeSessionTransport(ctx context.Context, machine pve.VM, username string) error {
	if !validAgentUser(username) {
		return ErrInvalid
	}
	if err := e.probe(ctx, machine); err != nil {
		return err
	}
	// Prefix/OS username alone is not account ownership. Prove the protected
	// Agent SAM journal before creating/repairing any V2 authorization record.
	result, err := e.command(ctx, machine, "account-inspect", nil, "--guest-user", username)
	if err != nil {
		return err
	}
	var account struct {
		Version  int    `json:"schema_version"`
		Username string `json:"username"`
		SID      string `json:"sid"`
		Disabled *bool  `json:"disabled"`
	}
	if len(result.Stdout) > 4096 || json.Unmarshal([]byte(result.Stdout), &account) != nil || account.Version != 1 || account.Username != username || !validWindowsAccountSID(account.SID) || account.Disabled == nil {
		return fmt.Errorf("%w: Windows account ownership receipt", ErrUnavailable)
	}
	_, err = e.command(ctx, machine, "init", nil, "--guest-user", username)
	return err
}

func (e *WindowsSessionExecutor) DiscoverSession(ctx context.Context, machine pve.VM, username string) (SessionTarget, error) {
	if !validAgentUser(username) {
		return SessionTarget{}, ErrInvalid
	}
	if err := e.probe(ctx, machine); err != nil {
		return SessionTarget{}, err
	}
	result, err := e.command(ctx, machine, "session", nil, "--guest-user", username)
	if err != nil {
		return SessionTarget{}, err
	}
	var receipt struct {
		Version int           `json:"schema_version"`
		Target  SessionTarget `json:"target"`
		Ready   bool          `json:"input_ready"`
	}
	if len(result.Stdout) > 4096 || json.Unmarshal([]byte(result.Stdout), &receipt) != nil || receipt.Version != 2 || !receipt.Ready || receipt.Target.Username != username || !validWindowsAgentTarget(receipt.Target) {
		return SessionTarget{}, fmt.Errorf("%w: Windows session identity/readiness receipt", ErrUnavailable)
	}
	return receipt.Target, nil
}

func (e *WindowsSessionExecutor) ActivateSessionAuthority(ctx context.Context, machine pve.VM, target SessionTarget, authority Authority) error {
	authority.State = "active"
	if !validWindowsAgentTarget(target) || !validAuthority(authority) || authority.ExpiresUnixMS <= time.Now().UnixMilli() {
		return ErrInvalid
	}
	return e.writeAuthority(ctx, machine, boundAuthority{SessionSchemaVersion, &target, authority})
}

func (e *WindowsSessionExecutor) RevokeSessionAuthority(ctx context.Context, machine pve.VM, authority Authority) error {
	// A tombstone is already expired, not a timestamped grant. Wall clocks
	// differing by milliseconds must never prevent revocation. Epoch ordering
	// remains authoritative and still rejects a delayed older tombstone.
	authority.State, authority.ExpiresUnixMS = "revoked", 0
	if !validAuthority(authority) {
		return ErrInvalid
	}
	return e.writeAuthority(ctx, machine, boundAuthority{SessionSchemaVersion, nil, authority})
}

func (e *WindowsSessionExecutor) writeAuthority(ctx context.Context, machine pve.VM, authority boundAuthority) error {
	if err := e.probe(ctx, machine); err != nil {
		return err
	}
	payload, err := pve.MarshalGuestJSON(authority)
	if err != nil || len(payload) > 65536 {
		return ErrInvalid
	}
	// Lost authorization publication is uncertain, never automatically replayed.
	_, err = e.command(ctx, machine, "authority", payload)
	return err
}

func (e *WindowsSessionExecutor) ExecuteForSession(ctx context.Context, machine pve.VM, target SessionTarget, request Request, timeout time.Duration) (Response, error) {
	if !validWindowsAgentTarget(target) || timeout < 250*time.Millisecond || timeout > 15*time.Second || request.Validate(time.Now()) != nil || request.ExpiresUnixMS > time.Now().Add(timeout+250*time.Millisecond).UnixMilli() {
		return Response{}, ErrInvalid
	}
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(request.ExpiresUnixMS))
	defer cancel()
	if err := e.probe(ctx, machine); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Response{}, ErrTimeout
		}
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, err
	}
	channel := e.guest.(guestInputChannel)
	action := boundAction{SessionSchemaVersion, target, timeout.Milliseconds(), request}
	result, err := replayBoundWindowsAction(ctx, action, func(call context.Context, payload []byte) (pve.GuestExecResult, error) {
		return channel.ExecGuestWithInput(call, machine.Node, machine.VMID, []string{e.binary, "computer-v2-dispatch", "--guest-user", target.Username, "--request-id", request.RequestID}, payload)
	})
	if errors.Is(err, context.DeadlineExceeded) || time.Now().UnixMilli() >= request.ExpiresUnixMS {
		return Response{}, ErrTimeout
	}
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	if err != nil {
		return Response{}, fmt.Errorf("%w: Windows action transport", ErrUnavailable)
	}
	if result.ExitCode != 0 {
		return Response{}, fmt.Errorf("%w: Windows action exit %d", ErrUnavailable, result.ExitCode)
	}
	var response Response
	if len(result.Stdout) > 16*1024*1024 || json.Unmarshal([]byte(result.Stdout), &response) != nil || !validWindowsActionResponse(request, response) {
		return Response{}, fmt.Errorf("%w: Windows action response schema/kind", ErrUnavailable)
	}
	return response, nil
}

// A success envelope alone does not prove that the requested operation ran.
// Keep response kinds exclusive; consumers additionally validate image bytes
// and their integrity before displaying or forwarding them.
func validWindowsActionResponse(request Request, response Response) bool {
	if response.SchemaVersion != SchemaVersion || response.RequestID != request.RequestID || !response.OK || response.Error != "" {
		return false
	}
	switch request.Operation {
	case OperationScreenshot:
		return response.Screenshot != nil && response.Accessibility == nil && response.Input == nil &&
			response.Screenshot.ContentType == "image/jpeg" && response.Screenshot.Data != "" &&
			response.Screenshot.Width > 0 && response.Screenshot.Height > 0
	case OperationAccessibility:
		return response.Accessibility != nil && response.Screenshot == nil && response.Input == nil &&
			(response.Accessibility.Source == "windows_uia" || response.Accessibility.Source == "window_enumeration")
	case OperationMouse, OperationKey, OperationTypeText:
		return response.Input != nil && response.Input.Applied && response.Screenshot == nil && response.Accessibility == nil
	default:
		return false
	}
}

// Only actions have the immutable replay contract, never account, authority,
// Helper lifecycle or arbitrary QGA commands. Reuse exact bytes/target/deadline
// and do not discover a replacement session on retry or extend input authority.
func replayBoundWindowsAction(ctx context.Context, action boundAction, send func(context.Context, []byte) (pve.GuestExecResult, error)) (pve.GuestExecResult, error) {
	if action.SchemaVersion != 2 || !validWindowsAgentTarget(action.Target) || action.TimeoutMS < 250 || action.TimeoutMS > 15000 || action.Request.Validate(time.Now()) != nil {
		return pve.GuestExecResult{}, ErrInvalid
	}
	payload, err := pve.MarshalGuestJSON(action)
	if err != nil || len(payload) > 65536 {
		return pve.GuestExecResult{}, ErrInvalid
	}
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(action.Request.ExpiresUnixMS))
	defer cancel()
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return pve.GuestExecResult{}, err
		}
		observation, stop := context.WithTimeout(ctx, windowsGuestObservationTimeout)
		result, err := send(observation, append([]byte(nil), payload...))
		observationExpired := errors.Is(observation.Err(), context.DeadlineExceeded)
		stop()
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		// Only the immutable action protocol permits this recovery. A QGA
		// observation timeout does not cancel Guest input already accepted;
		// reuse the exact request/instance/deadline to retrieve its reservation.
		uncertain := windowsGuestResultUncertain(err) || (observationExpired && errors.Is(err, context.DeadlineExceeded))
		if err == nil || attempt == 2 || !uncertain {
			return result, err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}

// Classification, not permission to retry. Callers must separately prove a
// read-only operation or the bound action's authenticated replay contract.
func windowsGuestResultUncertain(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return transientGuestOperationError(err) || (strings.Contains(message, "pve returned 500") && strings.Contains(message, "agent error: pid ") && strings.Contains(message, "does not exist"))
}

var _ SessionExecutor = (*WindowsSessionExecutor)(nil)
