package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/guestidentity"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

const nativeAccountFence = "credential_revision_v1"
const nativeLoginBirthFence = "pam_logind_native_v1"

// Native credentials and ordinary retirement are not Agent leases. In
// particular, reconnect must not destroy a retained human desktop.
type NativeAccountLifecycle interface {
	CheckNativeAccountSupport(context.Context, pve.VM) error
	PrepareNativeAccount(context.Context, pve.VM, string) (AccountIdentity, error)
	InspectNativeAccount(context.Context, pve.VM, string) (NativeAccountObservation, error)
	ChangeNativeCredential(context.Context, pve.VM, NativeCredential, string, []byte) error
}

type NativeCredential struct {
	SchemaVersion      int             `json:"schema_version"`
	Identity           AccountIdentity `json:"identity"`
	ConnectionID       string          `json:"connection_id"`
	Revision           int64           `json:"revision"`
	ExpiresUnixSeconds int64           `json:"expires_unix_seconds"`
	Phase              string          `json:"phase"`
}

var nativeConnectionRE = regexp.MustCompile(`^conn_[A-Za-z0-9_-]{8,128}$`)

func validNativeUser(username string) bool {
	return managedSessionUserRE.MatchString(username) && strings.HasPrefix(username, "vcw")
}

func (value NativeCredential) validFor(platform string) bool {
	if value.SchemaVersion != 1 || value.Revision < 1 || !nativeConnectionRE.MatchString(value.ConnectionID) ||
		!validNativeUser(value.Identity.Username) || !guestidentity.ValidAccount(platform, value.Identity.UID, value.Identity.SID) {
		return false
	}
	switch value.Phase {
	case "revoked":
		return value.ExpiresUnixSeconds == 0
	case "issuing", "issued", "retiring", "retired":
		return value.ExpiresUnixSeconds > 0 && value.ExpiresUnixSeconds < 0xffffffff
	default:
		return false
	}
}

func parseNativeCredential(raw []byte, platform string) (NativeCredential, error) {
	var value NativeCredential
	if len(raw) == 0 || len(raw) > 4096 {
		return value, ErrUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF || !value.validFor(platform) {
		return NativeCredential{}, fmt.Errorf("%w: Native credential receipt", ErrUnavailable)
	}
	return value, nil
}

type NativeAccountObservation struct {
	Identity           AccountIdentity
	Exists             bool
	Disabled           bool
	ExpiryDay          *uint64
	ProcessesAbsent    bool
	LoginWritersAbsent *bool // unknown if Native PAM protection is not installed
	Lifecycle          *NativeCredential
}

func (o NativeAccountObservation) Matches(intent NativeCredential, phase string) bool {
	intent.Phase = phase
	return o.Identity == intent.Identity && o.Lifecycle != nil && *o.Lifecycle == intent
}

func (o NativeAccountObservation) Closed() bool {
	return o.Disabled && o.ProcessesAbsent && o.LoginWritersAbsent != nil && *o.LoginWritersAbsent
}

func (o NativeAccountObservation) Retained() bool {
	return o.Exists && !o.Disabled && o.Lifecycle != nil &&
		(o.Lifecycle.Phase == "issued" || o.Lifecycle.Phase == "retired") &&
		o.Lifecycle.ExpiresUnixSeconds > time.Now().Unix() && o.ExpiryDay != nil &&
		*o.ExpiryDay == uint64((o.Lifecycle.ExpiresUnixSeconds+86399)/86400)
}

func parseNativeAccountObservation(raw []byte, username string) (NativeAccountObservation, error) {
	var wire struct {
		Version  int             `json:"schema_version"`
		Identity AccountIdentity `json:"identity"`
		Account  *struct {
			Exists    *bool           `json:"exists"`
			Disabled  *bool           `json:"disabled"`
			ExpiryDay json.RawMessage `json:"expiry_day"`
		} `json:"account"`
		ProcessesAbsent    *bool           `json:"processes_absent"`
		LoginWritersAbsent json.RawMessage `json:"login_writers_absent"`
		Lifecycle          json.RawMessage `json:"lifecycle"`
	}
	bad := fmt.Errorf("%w: Native account observation", ErrUnavailable)
	if len(raw) == 0 || len(raw) > 4096 || !validNativeUser(username) {
		return NativeAccountObservation{}, bad
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil || d.Decode(new(any)) != io.EOF || wire.Version != 1 || wire.Identity.Username != username ||
		!guestidentity.ValidAccount("linux", wire.Identity.UID, wire.Identity.SID) || wire.Account == nil ||
		wire.Account.Exists == nil || wire.Account.Disabled == nil || len(wire.Account.ExpiryDay) == 0 ||
		wire.ProcessesAbsent == nil || len(wire.LoginWritersAbsent) == 0 || len(wire.Lifecycle) == 0 {
		return NativeAccountObservation{}, bad
	}
	value := NativeAccountObservation{Identity: wire.Identity, Exists: *wire.Account.Exists,
		Disabled: *wire.Account.Disabled, ProcessesAbsent: *wire.ProcessesAbsent}
	if json.Unmarshal(wire.Account.ExpiryDay, &value.ExpiryDay) != nil ||
		json.Unmarshal(wire.LoginWritersAbsent, &value.LoginWritersAbsent) != nil {
		return NativeAccountObservation{}, bad
	}
	if !bytes.Equal(wire.Lifecycle, []byte("null")) {
		credential, err := parseNativeCredential(wire.Lifecycle, "linux")
		if err != nil || credential.Identity != value.Identity {
			return NativeAccountObservation{}, bad
		}
		value.Lifecycle = &credential
	}
	if !value.Exists && (!value.Disabled || value.ExpiryDay != nil) {
		return NativeAccountObservation{}, bad
	}
	return value, nil
}

func (e *PVEExecutor) nativeAccountProtocol(ctx context.Context, machine pve.VM, login bool) error {
	if err := e.requireLinuxSession(ctx, machine); err != nil {
		return err
	}
	result, err := e.sessionCommand(ctx, machine, "capabilities")
	if err != nil {
		return fmt.Errorf("%w: Native capability observation", ErrUnavailable)
	}
	var value struct {
		Version int    `json:"schema_version"`
		Fence   string `json:"native_account_fence"`
		Birth   string `json:"native_login_birth_fence"`
		Agent   string `json:"login_birth_fence"`
	}
	if len(result.Stdout) > 4096 || json.Unmarshal([]byte(result.Stdout), &value) != nil ||
		value.Version != 2 || value.Fence != nativeAccountFence ||
		(login && (value.Birth != nativeLoginBirthFence || value.Agent != loginBirthFence)) {
		return fmt.Errorf("%w: Native Guest offline upgrade required", ErrUnavailable)
	}
	return nil
}

func (e *PVEExecutor) CheckNativeAccountSupport(ctx context.Context, machine pve.VM) error {
	return e.nativeAccountProtocol(ctx, machine, true)
}

func (e *PVEExecutor) PrepareNativeAccount(ctx context.Context, machine pve.VM, username string) (AccountIdentity, error) {
	if !validNativeUser(username) {
		return AccountIdentity{}, ErrInvalid
	}
	if err := e.CheckNativeAccountSupport(ctx, machine); err != nil {
		return AccountIdentity{}, err
	}
	if _, err := e.sessionCommand(ctx, machine, "native-account-provision", "--guest-user", username); err != nil {
		return AccountIdentity{}, fmt.Errorf("%w: Native owned account provisioning", ErrUnavailable)
	}
	observed, err := e.InspectNativeAccount(ctx, machine, username)
	if err != nil || !observed.Exists {
		return AccountIdentity{}, ErrUnavailable
	}
	return observed.Identity, nil
}

func (e *PVEExecutor) InspectNativeAccount(ctx context.Context, machine pve.VM, username string) (NativeAccountObservation, error) {
	if !validNativeUser(username) {
		return NativeAccountObservation{}, ErrInvalid
	}
	if err := e.nativeAccountProtocol(ctx, machine, false); err != nil {
		return NativeAccountObservation{}, err
	}
	result, err := e.sessionCommand(ctx, machine, "native-account-inspect", "--guest-user", username)
	if err != nil {
		return NativeAccountObservation{}, fmt.Errorf("%w: Native account observation", ErrUnavailable)
	}
	return parseNativeAccountObservation([]byte(result.Stdout), username)
}

func (e *PVEExecutor) ChangeNativeCredential(ctx context.Context, machine pve.VM, desired NativeCredential, operation string, password []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	phase := map[string]string{"issue": "issued", "retire": "retired", "revoke": "revoked"}[operation]
	if phase == "" || desired.Phase != "" {
		return ErrInvalid
	}
	desired.Phase = phase
	if !desired.validFor("linux") || (operation != "revoke" &&
		(desired.ExpiresUnixSeconds <= time.Now().Unix() || desired.ExpiresUnixSeconds > time.Now().Unix()+28800)) {
		return ErrInvalid
	}
	if operation == "issue" {
		if len(password) < 24 || len(password) > 256 {
			return ErrInvalid
		}
		for _, b := range password {
			if b < 33 || b > 126 {
				return ErrInvalid
			}
		}
	} else if len(password) != 0 {
		return ErrInvalid
	}
	if err := e.nativeAccountProtocol(ctx, machine, operation != "revoke"); err != nil {
		return err
	}
	channel, ok := e.guest.(guestInputChannel)
	if !ok {
		return ErrUnavailable
	}
	input, err := pve.MarshalGuestJSON(struct {
		SchemaVersion      int             `json:"schema_version"`
		Identity           AccountIdentity `json:"identity"`
		ConnectionID       string          `json:"connection_id"`
		Revision           int64           `json:"revision"`
		ExpiresUnixSeconds int64           `json:"expires_unix_seconds"`
		Operation          string          `json:"operation"`
		Password           string          `json:"password,omitempty"`
	}{1, desired.Identity, desired.ConnectionID, desired.Revision, desired.ExpiresUnixSeconds, operation, string(password)})
	if err != nil || len(input) > 4096 {
		return ErrInvalid
	}
	defer clear(input)
	if err := ctx.Err(); err != nil {
		return err
	}
	// Dispatch once. Neither a missing receipt nor QGA failure authorizes a
	// retry of a credential write or a username-only password fallback.
	result, err := channel.ExecGuestWithInput(ctx, machine.Node, machine.VMID,
		[]string{sessionBinary, "computer-v2-native-account-credential", "--guest-user", desired.Identity.Username}, input)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("%w: Native credential write", ErrUnavailable)
	}
	receipt, err := parseNativeCredential([]byte(result.Stdout), "linux")
	if err != nil || receipt != desired {
		return fmt.Errorf("%w: exact Native credential receipt missing", ErrUnavailable)
	}
	observed, err := e.InspectNativeAccount(ctx, machine, desired.Identity.Username)
	if err != nil || !observed.Matches(desired, phase) ||
		(operation == "revoke" && !observed.Closed()) || (operation != "revoke" && !observed.Retained()) {
		return fmt.Errorf("%w: Native account did not converge", ErrUnavailable)
	}
	return ctx.Err()
}

var _ NativeAccountLifecycle = (*PVEExecutor)(nil)
