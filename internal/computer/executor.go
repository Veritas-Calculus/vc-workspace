package computer

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

type GuestChannel interface {
	VMConfiguration(context.Context, string, int) (pve.VMConfiguration, error)
	WriteGuestFile(context.Context, string, int, string, string) error
	ReadGuestFile(context.Context, string, int, string, int) (string, error)
	ExecGuest(context.Context, string, int, []string) (pve.GuestExecResult, error)
}

type Executor interface {
	Execute(context.Context, pve.VM, Request, time.Duration) (Response, error)
}

type AuthorityController interface {
	ActivateAuthority(context.Context, pve.VM, Authority) error
	RevokeAuthority(context.Context, pve.VM, Authority) error
}

type PVEExecutor struct {
	guest GuestChannel
}

const (
	guestTransportRetryBudget = 20 * time.Second
	guestDispatchAllowance    = 10 * time.Second
)

func NewPVEExecutor(guest GuestChannel) *PVEExecutor {
	return &PVEExecutor{guest: guest}
}

func (e *PVEExecutor) ActivateAuthority(ctx context.Context, machine pve.VM, authority Authority) error {
	authority.State = "active"
	if e == nil || e.guest == nil || !validAuthority(authority) {
		return ErrInvalid
	}
	// Another replica or a recovered Guest may have changed the file. Local
	// memory cannot prove current authority; the caller holds the DB gate.
	return e.writeAuthority(ctx, machine, authority)
}

func (e *PVEExecutor) RevokeAuthority(ctx context.Context, machine pve.VM, authority Authority) error {
	authority.State = "revoked"
	authority.ExpiresUnixMS = time.Now().UnixMilli()
	if e == nil || e.guest == nil || !validAuthority(authority) {
		return ErrInvalid
	}
	configuration, err := e.guest.VMConfiguration(ctx, machine.Node, machine.VMID)
	if err != nil {
		return fmt.Errorf("%w: read guest configuration", ErrUnavailable)
	}
	if configuration.OSType == "l26" {
		payload, err := json.Marshal(authority)
		if err != nil {
			return err
		}
		result, err := e.guest.ExecGuest(ctx, machine.Node, machine.VMID, []string{"/usr/bin/python3", "-c", revokeLegacyLinux, string(payload)})
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("%w: legacy Guest authority revocation failed", ErrUnavailable)
		}
		return nil
	}
	return e.writeAuthority(ctx, machine, authority)
}

//go:embed revoke_legacy_linux.py
var revokeLegacyLinux string

func (e *PVEExecutor) writeAuthority(ctx context.Context, machine pve.VM, authority Authority) error {
	if e == nil || e.guest == nil || !validAuthority(authority) {
		return ErrInvalid
	}
	configuration, err := e.guest.VMConfiguration(ctx, machine.Node, machine.VMID)
	if err != nil {
		return fmt.Errorf("%w: read guest configuration: %v", ErrUnavailable, err)
	}
	paths, err := e.resolveGuestPaths(ctx, machine, configuration.OSType, "action_abcdefghijklmnopqrst")
	if err != nil {
		return err
	}
	payload, err := json.Marshal(authority)
	if err != nil {
		return err
	}
	if err := e.writeGuestFileAtomically(ctx, machine, configuration.OSType, paths.authority, string(payload)); err != nil {
		return fmt.Errorf("%w: write guest authority: %v", ErrUnavailable, err)
	}
	return nil
}

func validAuthority(authority Authority) bool {
	return authority.SchemaVersion == SchemaVersion &&
		strings.HasPrefix(authority.LeaseID, "lease_") &&
		authority.ControlEpoch >= 1 &&
		(authority.State == "active" || authority.State == "revoked")
}

func (e *PVEExecutor) Execute(ctx context.Context, machine pve.VM, request Request, timeout time.Duration) (Response, error) {
	if e == nil || e.guest == nil {
		return Response{}, ErrUnavailable
	}
	if timeout < 250*time.Millisecond || timeout > 15*time.Second {
		return Response{}, fmt.Errorf("%w: timeout is outside 250-15000ms", ErrInvalid)
	}
	now := time.Now()
	if err := request.Validate(now); err != nil {
		return Response{}, err
	}
	if time.UnixMilli(request.ExpiresUnixMS).After(now.Add(timeout + 250*time.Millisecond)) {
		return Response{}, fmt.Errorf("%w: request expiry exceeds its action timeout", ErrInvalid)
	}
	configuration, err := e.guest.VMConfiguration(ctx, machine.Node, machine.VMID)
	if err != nil {
		return Response{}, fmt.Errorf("%w: read guest configuration: %v", ErrUnavailable, err)
	}
	paths, err := e.resolveGuestPaths(ctx, machine, configuration.OSType, request.RequestID)
	if err != nil {
		return Response{}, err
	}
	// The caller timeout bounds time spent inside the interactive worker. PVE
	// transport retries are separate and idempotent, so keep the staged request
	// valid long enough to survive a slow or temporarily unavailable QGA call.
	request.ExpiresUnixMS = time.Now().Add(timeout + guestTransportRetryBudget).UnixMilli()
	payload, err := json.Marshal(request)
	if err != nil {
		return Response{}, err
	}
	if len(payload) > 60*1024 {
		return Response{}, fmt.Errorf("%w: encoded request exceeds the QGA limit", ErrInvalid)
	}
	stagedRequest := paths.request + ".tmp"
	if err := retryGuestOperation(ctx, guestTransportRetryBudget, func() error {
		return e.guest.WriteGuestFile(ctx, machine.Node, machine.VMID, stagedRequest, string(payload))
	}); err != nil {
		return Response{}, fmt.Errorf("%w: stage request: %v", ErrUnavailable, err)
	}
	cleanupNeeded := true
	defer func() {
		if !cleanupNeeded {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = e.guest.ExecGuest(cleanupCtx, machine.Node, machine.VMID, []string{paths.executable, "computer-cleanup", "--request-id", request.RequestID})
	}()

	waitContext, cancel := context.WithTimeout(ctx, timeout+guestDispatchAllowance)
	defer cancel()
	var waitResult pve.GuestExecResult
	err = retryGuestOperation(waitContext, guestDispatchAllowance, func() error {
		var err error
		waitResult, err = e.guest.ExecGuest(waitContext, machine.Node, machine.VMID, []string{
			paths.executable, "computer-dispatch", "--request-id", request.RequestID, "--timeout-ms", fmt.Sprintf("%d", timeout.Milliseconds()),
		})
		return err
	})
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return Response{}, ErrTimeout
	}
	if err != nil {
		return Response{}, fmt.Errorf("%w: wait for helper: %v", ErrUnavailable, err)
	}
	if waitResult.ExitCode == 124 {
		return Response{}, ErrTimeout
	}
	if waitResult.ExitCode != 0 {
		return Response{}, fmt.Errorf("%w: helper wait failed with exit code %d", ErrUnavailable, waitResult.ExitCode)
	}
	if len(waitResult.Stdout) > 16*1024*1024 {
		return Response{}, fmt.Errorf("%w: helper response exceeds the transport limit", ErrUnavailable)
	}
	cleanupNeeded = false
	raw := waitResult.Stdout
	var response Response
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return Response{}, fmt.Errorf("%w: invalid helper response", ErrUnavailable)
	}
	if response.SchemaVersion != SchemaVersion || response.RequestID != request.RequestID {
		return Response{}, fmt.Errorf("%w: helper response identity mismatch", ErrUnavailable)
	}
	if !response.OK {
		if strings.TrimSpace(response.Error) == "" {
			response.Error = "computer operation failed"
		}
		return response, fmt.Errorf("computer operation failed: %s", response.Error)
	}
	return response, nil
}

// writeGuestFileAtomically prevents the interactive helper from observing a
// partially written QGA file. QGA writes to a sibling temporary path first;
// the final name becomes visible only after a fixed, bounded guest command
// renames it in the same directory.
func (e *PVEExecutor) writeGuestFileAtomically(ctx context.Context, machine pve.VM, osType, destination, payload string) error {
	temporary := destination + ".tmp"
	if err := retryGuestOperation(ctx, guestTransportRetryBudget, func() error {
		return e.guest.WriteGuestFile(ctx, machine.Node, machine.VMID, temporary, payload)
	}); err != nil {
		return err
	}
	command := []string{"/bin/sh", "-c", fmt.Sprintf("if test -f '%s'; then mv -f '%s' '%s'; else test -f '%s'; fi", temporary, temporary, destination, destination)}
	if strings.HasPrefix(strings.ToLower(osType), "win") {
		escape := func(value string) string { return strings.ReplaceAll(value, "'", "''") }
		script := fmt.Sprintf("if (Test-Path -LiteralPath '%s') { Move-Item -LiteralPath '%s' -Destination '%s' -Force } elseif (-not (Test-Path -LiteralPath '%s')) { throw 'staged file is unavailable' }", escape(temporary), escape(temporary), escape(destination), escape(destination))
		command = []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
	}
	return retryGuestOperation(ctx, guestTransportRetryBudget, func() error {
		result, err := e.guest.ExecGuest(ctx, machine.Node, machine.VMID, command)
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("atomic guest rename failed with exit code %d", result.ExitCode)
		}
		return nil
	})
}

func retryGuestOperation(ctx context.Context, maximum time.Duration, operation func() error) error {
	deadline := time.Now().Add(maximum)
	for {
		err := operation()
		if err == nil || !transientGuestOperationError(err) || !time.Now().Before(deadline) {
			return err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func transientGuestOperationError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "qemu guest agent is not running") ||
		(strings.Contains(message, "qga command") && strings.Contains(message, "timeout")) ||
		strings.Contains(message, "pve returned 596")
}

type paths struct {
	executable string
	authority  string
	request    string
	response   string
}

// resolveGuestPaths makes the VC Workspace names authoritative while keeping
// existing desktops built with the former agent layout usable during rolling
// upgrades. New templates never write to the legacy layout.
func (e *PVEExecutor) resolveGuestPaths(ctx context.Context, machine pve.VM, osType, requestID string) (paths, error) {
	current, err := guestPaths(osType, requestID)
	if err != nil {
		return paths{}, err
	}
	if executableExists(ctx, e.guest, machine, osType, current.executable) {
		return current, nil
	}
	legacy, err := legacyGuestPaths(osType, requestID)
	if err != nil {
		return paths{}, err
	}
	if executableExists(ctx, e.guest, machine, osType, legacy.executable) {
		return legacy, nil
	}
	// Prefer an error referencing the current product path when neither probe
	// succeeds; the following dispatch will return the transport's full detail.
	return current, nil
}

func executableExists(ctx context.Context, guest GuestChannel, machine pve.VM, osType, executable string) bool {
	command := []string{"/usr/bin/test", "-x", executable}
	if strings.HasPrefix(strings.ToLower(osType), "win") {
		escape := strings.ReplaceAll(executable, "'", "''")
		command = []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "if (Test-Path -LiteralPath '" + escape + "' -PathType Leaf) { exit 0 } else { exit 1 }"}
	}
	result, err := guest.ExecGuest(ctx, machine.Node, machine.VMID, command)
	return err == nil && result.ExitCode == 0
}

func guestPaths(osType, requestID string) (paths, error) {
	if !requestIDRE.MatchString(requestID) {
		return paths{}, ErrInvalid
	}
	if strings.HasPrefix(strings.ToLower(osType), "win") {
		base := `C:\ProgramData\VC Workspace\Agent\computer`
		return paths{
			executable: `C:\Program Files\VC Workspace\Agent\vc-workspace-guest-agent.exe`,
			authority:  base + `\authority.json`,
			request:    base + `\requests\` + requestID + `.json`,
			response:   base + `\responses\` + requestID + `.json`,
		}, nil
	}
	if osType == "l26" {
		base := "/var/lib/vc-workspace/computer"
		return paths{
			executable: "/usr/local/sbin/vc-workspace-guest-agent",
			authority:  base + "/authority.json",
			request:    base + "/requests/" + requestID + ".json",
			response:   base + "/responses/" + requestID + ".json",
		}, nil
	}
	return paths{}, fmt.Errorf("%w: guest OS is unsupported", ErrUnavailable)
}

func legacyGuestPaths(osType, requestID string) (paths, error) {
	if !requestIDRE.MatchString(requestID) {
		return paths{}, ErrInvalid
	}
	if strings.HasPrefix(strings.ToLower(osType), "win") {
		base := `C:\ProgramData\VC Workspace\Agent\computer`
		return paths{
			executable: `C:\Program Files\VC Workspace\Agent\vc-vdi-guest-agent.exe`,
			authority:  base + `\authority.json`,
			request:    base + `\requests\` + requestID + `.json`,
			response:   base + `\responses\` + requestID + `.json`,
		}, nil
	}
	if osType == "l26" {
		base := "/var/lib/vc-vdi/computer"
		return paths{
			executable: "/usr/local/sbin/vc-vdi-guest-agent",
			authority:  base + "/authority.json",
			request:    base + "/requests/" + requestID + ".json",
			response:   base + "/responses/" + requestID + ".json",
		}, nil
	}
	return paths{}, fmt.Errorf("%w: guest OS is unsupported", ErrUnavailable)
}
