package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/guestidentity"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

const accountFenceTransport = "lease_epoch_login_generation_v1"
const loginBirthFence = "pam_logind_jobs_v3"

type LinuxAccountObservation struct {
	Identity           *AccountIdentity
	Exists             bool
	Disabled           bool
	ExpiryDay          *uint64
	ProcessesAbsent    *bool // nil only for an account which has never been bound
	LoginWritersAbsent *bool
	Lifecycle          *AccountLease
}

func (value LinuxAccountObservation) LoginStopped() bool {
	return value.Identity != nil && value.Disabled && value.ProcessesAbsent != nil && *value.ProcessesAbsent &&
		value.LoginWritersAbsent != nil && *value.LoginWritersAbsent &&
		value.Lifecycle != nil && value.Lifecycle.Phase == "revoked"
}

func (value LinuxAccountObservation) Matches(lease AccountLease, phase string) bool {
	if value.Identity == nil || *value.Identity != lease.Identity || value.Lifecycle == nil {
		return false
	}
	lease.Phase = phase
	return *value.Lifecycle == lease
}

func parseLinuxAccountObservation(raw []byte, username string) (LinuxAccountObservation, error) {
	bad := fmt.Errorf("%w: Linux account observation", ErrUnavailable)
	var wire struct {
		Version  int             `json:"schema_version"`
		Username string          `json:"username"`
		Identity json.RawMessage `json:"identity"`
		Account  *struct {
			Exists    *bool           `json:"exists"`
			Disabled  *bool           `json:"disabled"`
			ExpiryDay json.RawMessage `json:"expiry_day"`
		} `json:"account"`
		ProcessesAbsent    json.RawMessage `json:"processes_absent"`
		LoginWritersAbsent json.RawMessage `json:"login_writers_absent"`
		Lifecycle          json.RawMessage `json:"lifecycle"`
	}
	if len(raw) == 0 || len(raw) > 4096 {
		return LinuxAccountObservation{}, bad
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Version != 1 || wire.Username != username ||
		wire.Account == nil || wire.Account.Exists == nil || wire.Account.Disabled == nil || len(wire.Identity) == 0 ||
		len(wire.Account.ExpiryDay) == 0 || len(wire.ProcessesAbsent) == 0 || len(wire.LoginWritersAbsent) == 0 || len(wire.Lifecycle) == 0 {
		return LinuxAccountObservation{}, bad
	}
	var value LinuxAccountObservation
	value.Exists, value.Disabled = *wire.Account.Exists, *wire.Account.Disabled
	identityDecoder := json.NewDecoder(bytes.NewReader(wire.Identity))
	identityDecoder.DisallowUnknownFields()
	if identityDecoder.Decode(&value.Identity) != nil || json.Unmarshal(wire.Account.ExpiryDay, &value.ExpiryDay) != nil ||
		json.Unmarshal(wire.ProcessesAbsent, &value.ProcessesAbsent) != nil || json.Unmarshal(wire.LoginWritersAbsent, &value.LoginWritersAbsent) != nil {
		return LinuxAccountObservation{}, bad
	}
	if !bytes.Equal(wire.Lifecycle, []byte("null")) {
		lease, err := parseAccountLease(wire.Lifecycle, "linux")
		if err != nil {
			return LinuxAccountObservation{}, bad
		}
		value.Lifecycle = &lease
	}
	if value.Identity == nil {
		if value.Exists || !value.Disabled || value.ExpiryDay != nil || value.ProcessesAbsent != nil || value.LoginWritersAbsent != nil || value.Lifecycle != nil {
			return LinuxAccountObservation{}, bad
		}
		return value, nil
	}
	if value.Identity.Username != username || !guestidentity.ValidAccount("linux", value.Identity.UID, value.Identity.SID) || value.ProcessesAbsent == nil || value.LoginWritersAbsent == nil ||
		(value.Lifecycle != nil && value.Lifecycle.Identity != *value.Identity) || (!value.Exists && (!value.Disabled || value.ExpiryDay != nil)) {
		return LinuxAccountObservation{}, bad
	}
	return value, nil
}

func (e *PVEExecutor) requireAccountFence(ctx context.Context, machine pve.VM) error {
	if err := e.requireLinuxSession(ctx, machine); err != nil {
		return err
	}
	result, err := e.sessionCommand(ctx, machine, "capabilities")
	if err != nil {
		return err
	}
	var value struct {
		Version int    `json:"schema_version"`
		Fence   string `json:"experimental_account_fence"`
		Birth   string `json:"login_birth_fence"`
	}
	if len(result.Stdout) > 4096 || json.Unmarshal([]byte(result.Stdout), &value) != nil || value.Version != 2 || value.Fence != accountFenceTransport || value.Birth != loginBirthFence {
		return fmt.Errorf("%w: Guest account protocol upgrade required", ErrUnavailable)
	}
	return nil
}

func (e *PVEExecutor) InspectAgentAccount(ctx context.Context, machine pve.VM, username string) (LinuxAccountObservation, error) {
	if !validAgentUser(username) {
		return LinuxAccountObservation{}, ErrInvalid
	}
	if err := e.requireAccountFence(ctx, machine); err != nil {
		return LinuxAccountObservation{}, err
	}
	result, err := e.sessionCommand(ctx, machine, "account-inspect", "--guest-user", username)
	if err != nil {
		return LinuxAccountObservation{}, err
	}
	return parseLinuxAccountObservation([]byte(result.Stdout), username)
}

func (e *PVEExecutor) changeLinuxAccount(ctx context.Context, machine pve.VM, desired AccountLease, login bool, password []byte) (AccountLease, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	operation, command, phase := "revoke", "account-lease", "revoked"
	if login {
		operation, command, phase = "open", "account-login", "sealed"
	}
	if desired.Phase != "" {
		return AccountLease{}, ErrInvalid
	}
	desired.Phase = phase
	if !desired.validFor("linux") || (login && (desired.ExpiresUnixSeconds <= time.Now().Unix() || desired.ExpiresUnixSeconds > time.Now().Unix()+28800)) {
		return AccountLease{}, ErrInvalid
	}
	if login {
		if len(password) < 24 || len(password) > 256 {
			return AccountLease{}, ErrInvalid
		}
		for _, b := range password {
			if b < 33 || b > 126 {
				return AccountLease{}, ErrInvalid
			}
		}
	} else if len(password) != 0 {
		return AccountLease{}, ErrInvalid
	}
	input, err := pve.MarshalGuestJSON(struct {
		SchemaVersion      int             `json:"schema_version"`
		Identity           AccountIdentity `json:"identity"`
		LeaseID            string          `json:"lease_id"`
		ControlEpoch       int64           `json:"control_epoch"`
		LoginGeneration    int64           `json:"login_generation"`
		ExpiresUnixSeconds int64           `json:"expires_unix_seconds"`
		Operation          string          `json:"operation"`
		Password           string          `json:"password,omitempty"`
	}{1, desired.Identity, desired.LeaseID, desired.ControlEpoch, desired.LoginGeneration, desired.ExpiresUnixSeconds, operation, string(password)})
	if err != nil {
		return AccountLease{}, ErrInvalid
	}
	defer clear(input)
	if err := e.requireAccountFence(ctx, machine); err != nil {
		return AccountLease{}, err
	}
	channel, ok := e.guest.(guestInputChannel)
	if !ok {
		return AccountLease{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return AccountLease{}, err
	}
	// One immutable Guest write. A lost receipt is not permission to resend a
	// password or fall back to unversioned QGA SetGuestUserPassword/sesrun.
	result, err := channel.ExecGuestWithInput(ctx, machine.Node, machine.VMID, []string{sessionBinary, "computer-v2-" + command, "--guest-user", desired.Identity.Username}, input)
	if err != nil || result.ExitCode != 0 {
		return AccountLease{}, fmt.Errorf("%w: bound Linux account write", ErrUnavailable)
	}
	receipt, err := parseAccountLease([]byte(result.Stdout), "linux")
	if err != nil || receipt != desired {
		return AccountLease{}, fmt.Errorf("%w: exact Linux account receipt missing", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return AccountLease{}, err
	}
	return receipt, nil
}
