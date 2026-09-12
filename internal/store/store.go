package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

var (
	ErrNotFound           = errors.New("not found")
	ErrAlreadyInitialized = errors.New("already initialized")
	ErrAlreadyLeased      = errors.New("desktop already leased")
	ErrConflict           = errors.New("conflict")
)

type Store struct {
	pool         *pgxpool.Pool
	controlLocks *pgxpool.Pool
}

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"created_at"`
}

type Session struct {
	User         User
	CSRFToken    string
	ExpiresAt    time.Time
	AuthKind     string
	CredentialID string
}

type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type OIDCRequest struct {
	Nonce        string
	PKCEVerifier string
	ClientKind   string
}

type DesktopLease struct {
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	DesktopID    string    `json:"desktop_id"`
	State        string    `json:"state"`
	ControlEpoch int64     `json:"control_epoch"`
	ExpiresAt    time.Time `json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type UserAccount struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	Role         string    `json:"role"`
	Disabled     bool      `json:"disabled"`
	IdentityKind string    `json:"identity_kind"`
	CreatedAt    time.Time `json:"created_at"`
}

type ManagedDesktop struct {
	VMID                     int       `json:"vmid"`
	DisplayName              string    `json:"display_name"`
	Node                     string    `json:"node"`
	OSFamily                 string    `json:"os_family"`
	Present                  bool      `json:"present"`
	Enabled                  bool      `json:"enabled"`
	AccessMode               string    `json:"access_mode"`
	OwnerUserID              string    `json:"owner_user_id,omitempty"`
	IdentityProfileID        string    `json:"identity_profile_id,omitempty"`
	IdentityState            string    `json:"identity_state"`
	AppliedIdentityProfileID string    `json:"applied_identity_profile_id,omitempty"`
	IdentityLastError        string    `json:"identity_last_error,omitempty"`
	IdentityUpdatedAt        time.Time `json:"identity_updated_at"`
	FirstSeenAt              time.Time `json:"first_seen_at"`
	LastSeenAt               time.Time `json:"last_seen_at"`
	UpdatedAt                time.Time `json:"updated_at"`
}

type IdentityGroup struct {
	ID          string    `json:"id"`
	ProviderID  string    `json:"provider_id,omitempty"`
	ExternalID  string    `json:"external_id,omitempty"`
	DisplayName string    `json:"display_name"`
	Source      string    `json:"source"`
	Enabled     bool      `json:"enabled"`
	MemberCount int       `json:"member_count"`
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type IdentityGroupMembership struct {
	GroupID   string    `json:"group_id"`
	UserID    string    `json:"user_id"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type IdentityProfile struct {
	ID           string          `json:"id"`
	DisplayName  string          `json:"display_name"`
	Platform     string          `json:"platform"`
	Mode         string          `json:"mode"`
	Enabled      bool            `json:"enabled"`
	Experimental bool            `json:"experimental"`
	Config       json.RawMessage `json:"config"`
	CreatedBy    string          `json:"created_by,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type GuestIdentityBinding struct {
	DesktopVMID   int       `json:"desktop_vmid"`
	UserID        string    `json:"user_id"`
	ProfileID     string    `json:"profile_id,omitempty"`
	GuestUsername string    `json:"guest_username"`
	State         string    `json:"state"`
	LastError     string    `json:"last_error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type DesktopConnectionSession struct {
	ID            string     `json:"id"`
	UserID        string     `json:"user_id"`
	DesktopVMID   int        `json:"desktop_vmid"`
	GuestUsername string     `json:"guest_username"`
	State         string     `json:"state"`
	ExpiresAt     time.Time  `json:"expires_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
}

type AgentPrincipal struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	Enabled     bool       `json:"enabled"`
	CreatedBy   string     `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

type DesktopAssignment struct {
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
	DesktopVMID int       `json:"desktop_vmid"`
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type DesktopAccessPolicy struct {
	VMID                 int       `json:"vmid"`
	PrivilegeMode        string    `json:"privilege_mode"`
	ClipboardRedirection bool      `json:"clipboard_redirection"`
	DriveRedirection     bool      `json:"drive_redirection"`
	ManagedBackground    bool      `json:"managed_background"`
	DesiredRevision      int64     `json:"desired_revision"`
	AppliedRevision      int64     `json:"applied_revision"`
	State                string    `json:"state"`
	OSFamily             string    `json:"os_family"`
	LastError            string    `json:"last_error,omitempty"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type AuditEvent struct {
	ID               int64           `json:"id,string"`
	ActorID          string          `json:"actor_id,omitempty"`
	ActorUsername    string          `json:"actor_username,omitempty"`
	ActorDisplayName string          `json:"actor_display_name,omitempty"`
	EventType        string          `json:"event_type"`
	Outcome          string          `json:"outcome"`
	TargetType       string          `json:"target_type,omitempty"`
	TargetID         string          `json:"target_id,omitempty"`
	Detail           json.RawMessage `json:"detail"`
	OccurredAt       time.Time       `json:"occurred_at"`
}

type AuditEventQuery struct {
	Limit   int
	Before  int64
	Outcome string
	Search  string
}

type AuditEventPage struct {
	Events     []AuditEvent `json:"events"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type Job struct {
	RequestFingerprint string          `json:"-"`
	ID                 string          `json:"id"`
	IdempotencyKey     string          `json:"-"`
	Operation          string          `json:"operation"`
	State              string          `json:"state"`
	SourceVMID         int             `json:"source_vmid,omitempty"`
	TargetVMID         int             `json:"target_vmid,omitempty"`
	TargetNode         string          `json:"target_node,omitempty"`
	TaskNode           string          `json:"task_node,omitempty"`
	UPID               string          `json:"upid,omitempty"`
	Request            json.RawMessage `json:"request"`
	Progress           int             `json:"progress"`
	Detail             string          `json:"detail,omitempty"`
	Error              string          `json:"error,omitempty"`
	CreatedBy          string          `json:"created_by"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type ImageProfile struct {
	ID                  string    `json:"id"`
	DisplayName         string    `json:"display_name"`
	OSFamily            string    `json:"os_family"`
	OSVersion           string    `json:"os_version"`
	Architecture        string    `json:"architecture"`
	Lifecycle           string    `json:"lifecycle"`
	Enabled             bool      `json:"enabled"`
	SourceNode          string    `json:"source_node"`
	SourceISO           string    `json:"source_iso"`
	SourceISOChecksum   string    `json:"source_iso_checksum"`
	DriverISO           string    `json:"driver_iso"`
	DriverISOChecksum   string    `json:"driver_iso_checksum"`
	TemplateVMID        int       `json:"template_vmid,omitempty"`
	MirrorURL           string    `json:"mirror_url"`
	SecurityMirrorURL   string    `json:"security_mirror_url"`
	StoragePool         string    `json:"storage_pool"`
	Bridge              string    `json:"bridge"`
	WindowsImageName    string    `json:"windows_image_name"`
	DefaultCores        int       `json:"default_cores"`
	DefaultMemoryMB     int       `json:"default_memory_mb"`
	DefaultDiskGB       int       `json:"default_disk_gb"`
	Firmware            string    `json:"firmware"`
	TPMVersion          string    `json:"tpm_version"`
	AgentKind           string    `json:"agent_kind"`
	DesktopProtocol     string    `json:"desktop_protocol"`
	DefaultGPUProfileID string    `json:"default_gpu_profile_id"`
	BuildStatus         string    `json:"build_status"`
	StatusDetail        string    `json:"status_detail"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type GPUProfile struct {
	ID                 string    `json:"id"`
	DisplayName        string    `json:"display_name"`
	Mode               string    `json:"mode"`
	VendorID           string    `json:"vendor_id"`
	DeviceClass        string    `json:"device_class"`
	ResourceMapping    string    `json:"resource_mapping"`
	MDevType           string    `json:"mdev_type"`
	Enabled            bool      `json:"enabled"`
	Exclusive          bool      `json:"exclusive"`
	AllowLiveMigration bool      `json:"allow_live_migration"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	// Long-lived control gates must not exhaust connections needed by their
	// own authorization queries. Keep their bounded pool separate from SQL work.
	lockConfig := pool.Config()
	lockConfig.MinConns = 0
	lockConfig.MinIdleConns = 0
	lockConfig.MaxConns = min(lockConfig.MaxConns, 4)
	locks, err := pgxpool.NewWithConfig(ctx, lockConfig)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("open desktop control lock pool: %w", err)
	}
	return &Store{pool: pool, controlLocks: locks}, nil
}

func (s *Store) Close() { s.controlLocks.Close(); s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	// Hold a dedicated connection for the entire migration set, including the
	// first CREATE TABLE. Each replica must observe the previous one's commit.
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock(72914400)`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer releaseAdvisoryLock(connection, 72914400)
	if _, err := connection.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var applied bool
		if err := connection.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		sql, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := connection.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) IsInitialized(ctx context.Context) (bool, error) {
	var initialized bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users)`).Scan(&initialized)
	return initialized, err
}

func (s *Store) CreateInitialAdmin(ctx context.Context, user User) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(72914403)`); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users)`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrAlreadyInitialized
	}
	_, err = tx.Exec(ctx, `INSERT INTO users(id,username,username_normalized,display_name,password_hash,role) VALUES($1,$2,$3,$4,$5,'platform_admin')`,
		user.ID, user.Username, strings.ToLower(user.Username), user.DisplayName, user.PasswordHash)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `SELECT id,username,display_name,COALESCE(password_hash,''),role,disabled,created_at FROM users WHERE username_normalized=$1`, strings.ToLower(username)).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.Role, &user.Disabled, &user.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return user, err
}

func (s *Store) Users(ctx context.Context) ([]UserAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id,u.username,u.display_name,u.role,u.disabled,
		  CASE WHEN u.password_hash IS NOT NULL THEN 'local' ELSE 'oidc' END,u.created_at
		FROM users u ORDER BY lower(u.display_name),lower(u.username),u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]UserAccount, 0)
	for rows.Next() {
		var user UserAccount
		if err := rows.Scan(&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.Disabled, &user.IdentityKind, &user.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) CreateLocalUser(ctx context.Context, user User) (UserAccount, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users(id,username,username_normalized,display_name,password_hash,role,disabled)
		VALUES($1,$2,$3,$4,$5,'user',false)`,
		user.ID, user.Username, strings.ToLower(user.Username), user.DisplayName, user.PasswordHash)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return UserAccount{}, ErrConflict
		}
		return UserAccount{}, err
	}
	return UserAccount{
		ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, Role: "user",
		IdentityKind: "local", CreatedAt: time.Now().UTC(),
	}, nil
}

func (s *Store) SetUserDisabled(ctx context.Context, id string, disabled bool) (UserAccount, error) {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return UserAccount{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var user UserAccount
	err = tx.QueryRow(ctx, `
		UPDATE users SET disabled=$2,updated_at=now() WHERE id=$1
		RETURNING id,username,display_name,role,disabled,
		  CASE WHEN password_hash IS NOT NULL THEN 'local' ELSE 'oidc' END,created_at`, id, disabled).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.Disabled, &user.IdentityKind, &user.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserAccount{}, ErrNotFound
	}
	if err != nil {
		return UserAccount{}, err
	}
	if disabled {
		if _, err := tx.Exec(ctx, `DELETE FROM web_sessions WHERE user_id=$1`, id); err != nil {
			return UserAccount{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM native_sessions WHERE user_id=$1`, id); err != nil {
			return UserAccount{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM native_auth_codes WHERE user_id=$1`, id); err != nil {
			return UserAccount{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE desktop_connection_sessions SET state='revoking',termination_required=true,updated_at=now() WHERE user_id=$1 AND state='active'`, id); err != nil {
			return UserAccount{}, err
		}
	}
	return user, commitAccessChange(ctx, tx)
}

// SetUserPassword replaces a local account credential and revokes every
// session except an explicitly retained Web session. A nil retained digest is
// used by administrator resets and revokes all sessions.
func (s *Store) SetUserPassword(ctx context.Context, id, passwordHash string, retainedWebSessionDigest []byte) (UserAccount, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(passwordHash) == "" {
		return UserAccount{}, errors.New("user password update is invalid")
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return UserAccount{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var user UserAccount
	err = tx.QueryRow(ctx, `
		UPDATE users SET password_hash=$2,updated_at=now()
		WHERE id=$1 AND password_hash IS NOT NULL
		RETURNING id,username,display_name,role,disabled,'local',created_at`, id, passwordHash).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.Disabled, &user.IdentityKind, &user.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if lookupErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, id).Scan(&exists); lookupErr != nil {
			return UserAccount{}, lookupErr
		}
		if exists {
			return UserAccount{}, ErrConflict
		}
		return UserAccount{}, ErrNotFound
	}
	if err != nil {
		return UserAccount{}, err
	}
	if len(retainedWebSessionDigest) == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM web_sessions WHERE user_id=$1`, id); err != nil {
			return UserAccount{}, err
		}
	} else if _, err := tx.Exec(ctx, `DELETE FROM web_sessions WHERE user_id=$1 AND token_digest<>$2`, id, retainedWebSessionDigest); err != nil {
		return UserAccount{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM native_sessions WHERE user_id=$1`, id); err != nil {
		return UserAccount{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM native_auth_codes WHERE user_id=$1`, id); err != nil {
		return UserAccount{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE desktop_connection_sessions SET state='revoking',termination_required=true,updated_at=now() WHERE user_id=$1 AND state IN ('active','revoking')`, id); err != nil {
		return UserAccount{}, err
	}
	if err := queueGuestIdentityRevocations(ctx, tx, `b.user_id=$1`, "credential_revoked", id); err != nil {
		return UserAccount{}, err
	}
	return user, commitAccessChange(ctx, tx)
}

func (s *Store) ManagedDesktops(ctx context.Context) ([]ManagedDesktop, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT vmid,display_name,node,os_family,present,enabled,access_mode,COALESCE(owner_user_id,''),
		       COALESCE(identity_profile_id,''),identity_state,COALESCE(applied_identity_profile_id,''),
		       identity_last_error,identity_updated_at,first_seen_at,last_seen_at,updated_at
		FROM managed_desktops ORDER BY lower(display_name),vmid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	desktops := make([]ManagedDesktop, 0)
	for rows.Next() {
		var desktop ManagedDesktop
		if err := scanManagedDesktop(rows, &desktop); err != nil {
			return nil, err
		}
		desktops = append(desktops, desktop)
	}
	return desktops, rows.Err()
}

type rowScanner interface {
	Scan(...any) error
}

func scanManagedDesktop(row rowScanner, desktop *ManagedDesktop) error {
	return row.Scan(
		&desktop.VMID, &desktop.DisplayName, &desktop.Node, &desktop.OSFamily, &desktop.Present, &desktop.Enabled,
		&desktop.AccessMode, &desktop.OwnerUserID, &desktop.IdentityProfileID,
		&desktop.IdentityState, &desktop.AppliedIdentityProfileID, &desktop.IdentityLastError, &desktop.IdentityUpdatedAt,
		&desktop.FirstSeenAt, &desktop.LastSeenAt, &desktop.UpdatedAt,
	)
}

func (s *Store) ManagedDesktopByVMID(ctx context.Context, vmid int) (ManagedDesktop, error) {
	var desktop ManagedDesktop
	err := scanManagedDesktop(s.pool.QueryRow(ctx, `
		SELECT vmid,display_name,node,os_family,present,enabled,access_mode,COALESCE(owner_user_id,''),
		       COALESCE(identity_profile_id,''),identity_state,COALESCE(applied_identity_profile_id,''),
		       identity_last_error,identity_updated_at,first_seen_at,last_seen_at,updated_at
		FROM managed_desktops WHERE vmid=$1`, vmid), &desktop)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedDesktop{}, ErrNotFound
	}
	return desktop, err
}

func (s *Store) ReconcileManagedDesktops(ctx context.Context, desktops []ManagedDesktop) error {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(72914404)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE managed_desktops SET present=false,updated_at=now() WHERE present=true`); err != nil {
		return err
	}
	for _, desktop := range desktops {
		if desktop.VMID <= 0 || strings.TrimSpace(desktop.DisplayName) == "" {
			return errors.New("managed desktop is invalid")
		}
		desktop.OSFamily = normalizeManagedDesktopOSFamily(desktop.OSFamily)
		if _, err := tx.Exec(ctx, `
			INSERT INTO managed_desktops(vmid,display_name,node,os_family,present,last_seen_at)
			VALUES($1,$2,$3,$4,true,now())
			ON CONFLICT(vmid) DO UPDATE SET
			  display_name=excluded.display_name,node=excluded.node,
			  os_family=CASE WHEN excluded.os_family='unknown' THEN managed_desktops.os_family ELSE excluded.os_family END,
			  present=true,last_seen_at=now(),updated_at=now()`,
			desktop.VMID, strings.TrimSpace(desktop.DisplayName), strings.TrimSpace(desktop.Node), desktop.OSFamily); err != nil {
			return err
		}
	}
	return commitAccessChange(ctx, tx)
}

func (s *Store) UpsertManagedDesktop(ctx context.Context, desktop ManagedDesktop) error {
	if desktop.VMID <= 0 || strings.TrimSpace(desktop.DisplayName) == "" {
		return errors.New("managed desktop is invalid")
	}
	desktop.OSFamily = normalizeManagedDesktopOSFamily(desktop.OSFamily)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO managed_desktops(vmid,display_name,node,os_family,present,last_seen_at)
		VALUES($1,$2,$3,$4,true,now())
		ON CONFLICT(vmid) DO UPDATE SET
		  display_name=excluded.display_name,node=excluded.node,
		  os_family=CASE WHEN excluded.os_family='unknown' THEN managed_desktops.os_family ELSE excluded.os_family END,
		  present=true,last_seen_at=now(),updated_at=now()`,
		desktop.VMID, strings.TrimSpace(desktop.DisplayName), strings.TrimSpace(desktop.Node), desktop.OSFamily)
	return err
}

// QuarantineClonedDesktop retains failed provisioning for operator inspection,
// but removes it from effective access and queues revocation if it was already
// observed by inventory reconciliation.
func (s *Store) QuarantineClonedDesktop(ctx context.Context, desktop ManagedDesktop) error {
	if desktop.VMID <= 0 || strings.TrimSpace(desktop.DisplayName) == "" {
		return errors.New("managed desktop is invalid")
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO managed_desktops(vmid,display_name,node,os_family,present,enabled,last_seen_at)
 VALUES($1,$2,$3,$4,true,false,now()) ON CONFLICT(vmid) DO UPDATE SET
 enabled=false,updated_at=now()`, desktop.VMID, strings.TrimSpace(desktop.DisplayName), strings.TrimSpace(desktop.Node), normalizeManagedDesktopOSFamily(desktop.OSFamily))
	if err != nil {
		return err
	}
	return commitAccessChange(ctx, tx)
}

func normalizeManagedDesktopOSFamily(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "linux":
		return "linux"
	case "windows":
		return "windows"
	default:
		return "unknown"
	}
}

func (s *Store) UserAssignedDesktopVMIDs(ctx context.Context, userID string) ([]int, error) {
	return s.assignedDesktopVMIDs(ctx, `SELECT desktop_vmid FROM effective_user_desktop_access WHERE user_id=$1 ORDER BY desktop_vmid`, userID)
}

func (s *Store) AgentAssignedDesktopVMIDs(ctx context.Context, agentID string) ([]int, error) {
	return s.assignedDesktopVMIDs(ctx, `
		SELECT a.desktop_vmid FROM agent_desktop_assignments a
		JOIN managed_desktops d ON d.vmid=a.desktop_vmid
		JOIN agent_principals p ON p.id=a.agent_id
		WHERE a.agent_id=$1 AND p.enabled=true AND d.present=true AND d.enabled=true AND NOT EXISTS (SELECT 1 FROM pve_jobs clone_job WHERE clone_job.target_vmid=d.vmid AND clone_job.operation='pve.template_clone' AND clone_job.state<>'succeeded') ORDER BY a.desktop_vmid`, agentID)
}

func (s *Store) assignedDesktopVMIDs(ctx context.Context, query, subjectID string) ([]int, error) {
	rows, err := s.pool.Query(ctx, query, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	vmids := make([]int, 0)
	for rows.Next() {
		var vmid int
		if err := rows.Scan(&vmid); err != nil {
			return nil, err
		}
		vmids = append(vmids, vmid)
	}
	return vmids, rows.Err()
}

func (s *Store) UserCanAccessDesktop(ctx context.Context, userID string, vmid int) (bool, error) {
	var allowed bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM effective_user_desktop_access WHERE user_id=$1 AND desktop_vmid=$2)`, userID, vmid).Scan(&allowed)
	return allowed, err
}

func (s *Store) AgentCanAccessDesktop(ctx context.Context, agentID string, vmid int) (bool, error) {
	var allowed bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM agent_desktop_assignments a
		  JOIN managed_desktops d ON d.vmid=a.desktop_vmid
		  JOIN agent_principals p ON p.id=a.agent_id
		  WHERE a.agent_id=$1 AND a.desktop_vmid=$2 AND p.enabled=true AND d.present=true AND d.enabled=true AND NOT EXISTS (SELECT 1 FROM pve_jobs clone_job WHERE clone_job.target_vmid=d.vmid AND clone_job.operation='pve.template_clone' AND clone_job.state<>'succeeded')
		)`, agentID, vmid).Scan(&allowed)
	return allowed, err
}

func (s *Store) AgentPrincipals(ctx context.Context) ([]AgentPrincipal, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,display_name,enabled,COALESCE(created_by,''),created_at,updated_at,last_used_at
		FROM agent_principals ORDER BY lower(display_name),id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := make([]AgentPrincipal, 0)
	for rows.Next() {
		var agent AgentPrincipal
		if err := rows.Scan(&agent.ID, &agent.DisplayName, &agent.Enabled, &agent.CreatedBy, &agent.CreatedAt, &agent.UpdatedAt, &agent.LastUsedAt); err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

func (s *Store) CreateAgentPrincipal(ctx context.Context, agent AgentPrincipal, tokenDigest []byte) (AgentPrincipal, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO agent_principals(id,display_name,token_digest,enabled,created_by)
		VALUES($1,$2,$3,true,NULLIF($4,''))
		RETURNING id,display_name,enabled,COALESCE(created_by,''),created_at,updated_at,last_used_at`,
		agent.ID, agent.DisplayName, tokenDigest, agent.CreatedBy).Scan(
		&agent.ID, &agent.DisplayName, &agent.Enabled, &agent.CreatedBy, &agent.CreatedAt, &agent.UpdatedAt, &agent.LastUsedAt,
	)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return AgentPrincipal{}, ErrConflict
		}
		return AgentPrincipal{}, err
	}
	return agent, nil
}

func (s *Store) RotateAgentToken(ctx context.Context, id string, tokenDigest []byte) (AgentPrincipal, error) {
	var agent AgentPrincipal
	err := s.pool.QueryRow(ctx, `
		UPDATE agent_principals SET token_digest=$2,updated_at=now() WHERE id=$1
		RETURNING id,display_name,enabled,COALESCE(created_by,''),created_at,updated_at,last_used_at`, id, tokenDigest).Scan(
		&agent.ID, &agent.DisplayName, &agent.Enabled, &agent.CreatedBy, &agent.CreatedAt, &agent.UpdatedAt, &agent.LastUsedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentPrincipal{}, ErrNotFound
	}
	return agent, err
}

func (s *Store) SetAgentEnabled(ctx context.Context, id string, enabled bool) (AgentPrincipal, error) {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return AgentPrincipal{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var agent AgentPrincipal
	err = tx.QueryRow(ctx, `
		UPDATE agent_principals SET enabled=$2,updated_at=now() WHERE id=$1
		RETURNING id,display_name,enabled,COALESCE(created_by,''),created_at,updated_at,last_used_at`, id, enabled).Scan(
		&agent.ID, &agent.DisplayName, &agent.Enabled, &agent.CreatedBy, &agent.CreatedAt, &agent.UpdatedAt, &agent.LastUsedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentPrincipal{}, ErrNotFound
	}
	if err != nil {
		return AgentPrincipal{}, err
	}
	if !enabled {
		if _, err := tx.Exec(ctx, `UPDATE desktop_leases SET state='revoked',control_epoch=control_epoch+1,updated_at=now() WHERE agent_id=$1 AND state='active'`, id); err != nil {
			return AgentPrincipal{}, err
		}
	}
	return agent, tx.Commit(ctx)
}

func (s *Store) AgentByTokenDigest(ctx context.Context, tokenDigest []byte) (AgentPrincipal, error) {
	var agent AgentPrincipal
	err := s.pool.QueryRow(ctx, `
		UPDATE agent_principals SET last_used_at=now()
		WHERE token_digest=$1 AND enabled=true
		RETURNING id,display_name,enabled,COALESCE(created_by,''),created_at,updated_at,last_used_at`, tokenDigest).Scan(
		&agent.ID, &agent.DisplayName, &agent.Enabled, &agent.CreatedBy, &agent.CreatedAt, &agent.UpdatedAt, &agent.LastUsedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentPrincipal{}, ErrNotFound
	}
	return agent, err
}

func (s *Store) DesktopAssignments(ctx context.Context) ([]DesktopAssignment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT 'user',d.owner_user_id,d.vmid,'',d.updated_at
		FROM managed_desktops d WHERE d.access_mode='personal' AND d.owner_user_id IS NOT NULL
		UNION ALL
		SELECT 'user',a.user_id,a.desktop_vmid,COALESCE(a.created_by,''),a.created_at
		FROM user_desktop_assignments a
		JOIN managed_desktops d ON d.vmid=a.desktop_vmid AND d.access_mode='shared'
		UNION ALL
		SELECT 'agent',agent_id,desktop_vmid,COALESCE(created_by,''),created_at FROM agent_desktop_assignments
		UNION ALL
		SELECT 'group',group_id,desktop_vmid,COALESCE(created_by,''),created_at FROM group_desktop_assignments
		ORDER BY 1,2,3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assignments := make([]DesktopAssignment, 0)
	for rows.Next() {
		var assignment DesktopAssignment
		if err := rows.Scan(&assignment.SubjectType, &assignment.SubjectID, &assignment.DesktopVMID, &assignment.CreatedBy, &assignment.CreatedAt); err != nil {
			return nil, err
		}
		assignments = append(assignments, assignment)
	}
	return assignments, rows.Err()
}

func (s *Store) UpdateManagedDesktopIdentity(ctx context.Context, vmid int, accessMode, ownerUserID, profileID string) (ManagedDesktop, error) {
	if accessMode != "personal" && accessMode != "shared" {
		return ManagedDesktop{}, errors.New("desktop access mode is invalid")
	}
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return ManagedDesktop{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if accessMode == "personal" && ownerUserID == "" {
		return ManagedDesktop{}, errors.New("personal desktop requires an owner")
	}
	if accessMode == "shared" {
		ownerUserID = ""
	}
	var desktop ManagedDesktop
	err = scanManagedDesktop(tx.QueryRow(ctx, `
		UPDATE managed_desktops
		SET access_mode=$2,owner_user_id=NULLIF($3,''),identity_profile_id=NULLIF($4,''),
		    identity_state='pending',identity_last_error='',identity_updated_at=now(),updated_at=now()
		WHERE vmid=$1
		RETURNING vmid,display_name,node,os_family,present,enabled,access_mode,COALESCE(owner_user_id,''),
		          COALESCE(identity_profile_id,''),identity_state,COALESCE(applied_identity_profile_id,''),
		          identity_last_error,identity_updated_at,first_seen_at,last_seen_at,updated_at`, vmid, accessMode, ownerUserID, profileID), &desktop)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedDesktop{}, ErrNotFound
	}
	if err != nil {
		if isForeignKeyViolation(err) {
			return ManagedDesktop{}, ErrNotFound
		}
		return ManagedDesktop{}, err
	}
	if accessMode == "personal" {
		if _, err := tx.Exec(ctx, `DELETE FROM user_desktop_assignments WHERE desktop_vmid=$1`, vmid); err != nil {
			return ManagedDesktop{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM group_desktop_assignments WHERE desktop_vmid=$1`, vmid); err != nil {
			return ManagedDesktop{}, err
		}
	}
	return desktop, commitAccessChange(ctx, tx)
}

func (s *Store) PutDesktopAssignment(ctx context.Context, assignment DesktopAssignment) (bool, error) {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var accessMode, ownerUserID string
	err = tx.QueryRow(ctx, `SELECT access_mode,COALESCE(owner_user_id,'') FROM managed_desktops WHERE vmid=$1 FOR UPDATE`, assignment.DesktopVMID).Scan(&accessMode, &ownerUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	var query string
	switch assignment.SubjectType {
	case "user":
		if accessMode == "personal" {
			if ownerUserID != "" && ownerUserID != assignment.SubjectID {
				return false, ErrConflict
			}
			if ownerUserID == "" {
				tag, updateErr := tx.Exec(ctx, `UPDATE managed_desktops SET owner_user_id=$2,updated_at=now() WHERE vmid=$1`, assignment.DesktopVMID, assignment.SubjectID)
				if updateErr != nil {
					if isForeignKeyViolation(updateErr) {
						return false, ErrNotFound
					}
					return false, updateErr
				}
				if err := commitAccessChange(ctx, tx); err != nil {
					return false, err
				}
				return tag.RowsAffected() == 1, nil
			}
			return false, commitAccessChange(ctx, tx)
		}
		query = `INSERT INTO user_desktop_assignments(user_id,desktop_vmid,created_by) VALUES($1,$2,NULLIF($3,'')) ON CONFLICT DO NOTHING`
	case "agent":
		query = `INSERT INTO agent_desktop_assignments(agent_id,desktop_vmid,created_by) VALUES($1,$2,NULLIF($3,'')) ON CONFLICT DO NOTHING`
	case "group":
		if accessMode != "shared" {
			return false, ErrConflict
		}
		query = `INSERT INTO group_desktop_assignments(group_id,desktop_vmid,created_by) VALUES($1,$2,NULLIF($3,'')) ON CONFLICT DO NOTHING`
	default:
		return false, errors.New("assignment subject type is invalid")
	}
	tag, err := tx.Exec(ctx, query, assignment.SubjectID, assignment.DesktopVMID, assignment.CreatedBy)
	if err != nil {
		if isForeignKeyViolation(err) {
			return false, ErrNotFound
		}
		return false, err
	}
	if err := commitAccessChange(ctx, tx); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) DeleteDesktopAssignment(ctx context.Context, subjectType, subjectID string, vmid int) (bool, error) {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var query string
	switch subjectType {
	case "user":
		var accessMode, ownerUserID string
		err := tx.QueryRow(ctx, `SELECT access_mode,COALESCE(owner_user_id,'') FROM managed_desktops WHERE vmid=$1 FOR UPDATE`, vmid).Scan(&accessMode, &ownerUserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, err
		}
		if accessMode == "personal" {
			if ownerUserID != subjectID {
				return false, commitAccessChange(ctx, tx)
			}
			query = `UPDATE managed_desktops SET owner_user_id=NULL,updated_at=now() WHERE vmid=$2 AND owner_user_id=$1`
		} else {
			query = `DELETE FROM user_desktop_assignments WHERE user_id=$1 AND desktop_vmid=$2`
		}
	case "agent":
		query = `DELETE FROM agent_desktop_assignments WHERE agent_id=$1 AND desktop_vmid=$2`
	case "group":
		query = `DELETE FROM group_desktop_assignments WHERE group_id=$1 AND desktop_vmid=$2`
	default:
		return false, errors.New("assignment subject type is invalid")
	}
	tag, err := tx.Exec(ctx, query, subjectID, vmid)
	if err != nil {
		return false, err
	}
	if subjectType == "agent" && tag.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx, `UPDATE desktop_leases SET state='revoked',control_epoch=control_epoch+1,updated_at=now() WHERE agent_id=$1 AND desktop_id=$2 AND state='active'`, subjectID, strconv.Itoa(vmid)); err != nil {
			return false, err
		}
	}
	if err := commitAccessChange(ctx, tx); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func isForeignKeyViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23503"
}

func (s *Store) IdentityProfiles(ctx context.Context) ([]IdentityProfile, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,display_name,platform,mode,enabled,experimental,config,COALESCE(created_by,''),created_at,updated_at
		FROM identity_profiles ORDER BY platform,experimental,lower(display_name),id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make([]IdentityProfile, 0)
	for rows.Next() {
		var profile IdentityProfile
		if err := rows.Scan(&profile.ID, &profile.DisplayName, &profile.Platform, &profile.Mode, &profile.Enabled, &profile.Experimental, &profile.Config, &profile.CreatedBy, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *Store) IdentityProfileByID(ctx context.Context, id string) (IdentityProfile, error) {
	var profile IdentityProfile
	err := s.pool.QueryRow(ctx, `
		SELECT id,display_name,platform,mode,enabled,experimental,config,COALESCE(created_by,''),created_at,updated_at
		FROM identity_profiles WHERE id=$1`, id).Scan(
		&profile.ID, &profile.DisplayName, &profile.Platform, &profile.Mode, &profile.Enabled,
		&profile.Experimental, &profile.Config, &profile.CreatedBy, &profile.CreatedAt, &profile.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdentityProfile{}, ErrNotFound
	}
	return profile, err
}

func (s *Store) PutIdentityProfile(ctx context.Context, profile IdentityProfile) (IdentityProfile, error) {
	if len(profile.Config) == 0 {
		profile.Config = json.RawMessage(`{}`)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return IdentityProfile{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, `
		INSERT INTO identity_profiles(id,display_name,platform,mode,enabled,experimental,config,created_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''))
		ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name,platform=excluded.platform,
		  mode=excluded.mode,enabled=excluded.enabled,experimental=excluded.experimental,
		  config=excluded.config,updated_at=now()
		RETURNING id,display_name,platform,mode,enabled,experimental,config,COALESCE(created_by,''),created_at,updated_at`,
		profile.ID, profile.DisplayName, profile.Platform, profile.Mode, profile.Enabled, profile.Experimental, profile.Config, profile.CreatedBy).Scan(
		&profile.ID, &profile.DisplayName, &profile.Platform, &profile.Mode, &profile.Enabled,
		&profile.Experimental, &profile.Config, &profile.CreatedBy, &profile.CreatedAt, &profile.UpdatedAt,
	)
	if err != nil {
		return IdentityProfile{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE managed_desktops SET identity_state='pending',identity_last_error='',identity_updated_at=now(),updated_at=now()
		WHERE identity_profile_id=$1`, profile.ID); err != nil {
		return IdentityProfile{}, err
	}
	return profile, tx.Commit(ctx)
}

func (s *Store) MarkManagedDesktopIdentityApplied(ctx context.Context, vmid int, profileID string) (ManagedDesktop, error) {
	return s.markManagedDesktopIdentity(ctx, vmid, "applied", profileID, "")
}

func (s *Store) MarkManagedDesktopIdentityRestartRequired(ctx context.Context, vmid int, profileID string) (ManagedDesktop, error) {
	return s.markManagedDesktopIdentity(ctx, vmid, "restart_required", profileID, "")
}

func (s *Store) MarkManagedDesktopIdentityFailed(ctx context.Context, vmid int, message string) (ManagedDesktop, error) {
	return s.markManagedDesktopIdentity(ctx, vmid, "failed", "", message)
}

func (s *Store) markManagedDesktopIdentity(ctx context.Context, vmid int, state, appliedProfileID, message string) (ManagedDesktop, error) {
	if len(message) > 500 {
		message = message[:500]
	}
	var desktop ManagedDesktop
	err := scanManagedDesktop(s.pool.QueryRow(ctx, `
		UPDATE managed_desktops SET identity_state=$2,applied_identity_profile_id=NULLIF($3,''),
		  identity_last_error=$4,identity_updated_at=now(),updated_at=now()
		WHERE vmid=$1
		RETURNING vmid,display_name,node,os_family,present,enabled,access_mode,COALESCE(owner_user_id,''),
		          COALESCE(identity_profile_id,''),identity_state,COALESCE(applied_identity_profile_id,''),
		          identity_last_error,identity_updated_at,first_seen_at,last_seen_at,updated_at`, vmid, state, appliedProfileID, message), &desktop)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedDesktop{}, ErrNotFound
	}
	return desktop, err
}

func (s *Store) IdentityGroups(ctx context.Context) ([]IdentityGroup, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.id,COALESCE(g.provider_id,''),COALESCE(g.external_id,''),g.display_name,g.source,g.enabled,
		       count(m.user_id),COALESCE(g.created_by,''),g.created_at,g.updated_at
		FROM identity_groups g
		LEFT JOIN identity_group_memberships m ON m.group_id=g.id
		GROUP BY g.id
		ORDER BY g.source,lower(g.display_name),g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := make([]IdentityGroup, 0)
	for rows.Next() {
		var group IdentityGroup
		if err := rows.Scan(&group.ID, &group.ProviderID, &group.ExternalID, &group.DisplayName, &group.Source, &group.Enabled, &group.MemberCount, &group.CreatedBy, &group.CreatedAt, &group.UpdatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *Store) PutIdentityGroup(ctx context.Context, group IdentityGroup) (IdentityGroup, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO identity_groups(id,provider_id,external_id,display_name,source,enabled,created_by)
		VALUES($1,NULLIF($2,''),NULLIF($3,''),$4,$5,$6,NULLIF($7,''))
		RETURNING id,COALESCE(provider_id,''),COALESCE(external_id,''),display_name,source,enabled,
		          0,COALESCE(created_by,''),created_at,updated_at`,
		group.ID, group.ProviderID, group.ExternalID, group.DisplayName, group.Source, group.Enabled, group.CreatedBy).Scan(
		&group.ID, &group.ProviderID, &group.ExternalID, &group.DisplayName, &group.Source, &group.Enabled,
		&group.MemberCount, &group.CreatedBy, &group.CreatedAt, &group.UpdatedAt,
	)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return IdentityGroup{}, ErrConflict
		}
		return IdentityGroup{}, err
	}
	return group, nil
}

func (s *Store) SetIdentityGroupEnabled(ctx context.Context, id string, enabled bool) (IdentityGroup, error) {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return IdentityGroup{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var group IdentityGroup
	err = tx.QueryRow(ctx, `
		UPDATE identity_groups SET enabled=$2,updated_at=now() WHERE id=$1
		RETURNING id,COALESCE(provider_id,''),COALESCE(external_id,''),display_name,source,enabled,
		          (SELECT count(*) FROM identity_group_memberships WHERE group_id=$1),
		          COALESCE(created_by,''),created_at,updated_at`, id, enabled).Scan(
		&group.ID, &group.ProviderID, &group.ExternalID, &group.DisplayName, &group.Source, &group.Enabled,
		&group.MemberCount, &group.CreatedBy, &group.CreatedAt, &group.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdentityGroup{}, ErrNotFound
	}
	if err != nil {
		return IdentityGroup{}, err
	}
	return group, commitAccessChange(ctx, tx)
}

func (s *Store) IdentityGroupMemberships(ctx context.Context) ([]IdentityGroupMembership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT group_id,user_id,source,created_at,updated_at
		FROM identity_group_memberships ORDER BY group_id,user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	memberships := make([]IdentityGroupMembership, 0)
	for rows.Next() {
		var membership IdentityGroupMembership
		if err := rows.Scan(&membership.GroupID, &membership.UserID, &membership.Source, &membership.CreatedAt, &membership.UpdatedAt); err != nil {
			return nil, err
		}
		memberships = append(memberships, membership)
	}
	return memberships, rows.Err()
}

func (s *Store) PutIdentityGroupMembership(ctx context.Context, membership IdentityGroupMembership) (IdentityGroupMembership, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO identity_group_memberships(group_id,user_id,source)
		SELECT id,$2,$3 FROM identity_groups WHERE id=$1 AND source <> 'oidc'
		ON CONFLICT(group_id,user_id) DO UPDATE SET source=excluded.source,updated_at=now()
		RETURNING group_id,user_id,source,created_at,updated_at`,
		membership.GroupID, membership.UserID, membership.Source).Scan(
		&membership.GroupID, &membership.UserID, &membership.Source, &membership.CreatedAt, &membership.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if queryErr := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity_groups WHERE id=$1)`, membership.GroupID).Scan(&exists); queryErr != nil {
			return IdentityGroupMembership{}, queryErr
		}
		if exists {
			return IdentityGroupMembership{}, ErrConflict
		}
		return IdentityGroupMembership{}, ErrNotFound
	}
	if err != nil {
		if isForeignKeyViolation(err) {
			return IdentityGroupMembership{}, ErrNotFound
		}
		return IdentityGroupMembership{}, err
	}
	return membership, nil
}

func (s *Store) DeleteIdentityGroupMembership(ctx context.Context, groupID, userID string) (bool, error) {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM identity_group_memberships WHERE group_id=$1 AND user_id=$2 AND source <> 'oidc'`, groupID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, commitAccessChange(ctx, tx)
}

func (s *Store) SyncOIDCGroupMemberships(ctx context.Context, providerID, userID string, groups []IdentityGroup) error {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	groupIDs := make([]string, 0, len(groups))
	for _, group := range groups {
		if group.ID == "" || group.ExternalID == "" || group.DisplayName == "" {
			return errors.New("OIDC group is invalid")
		}
		groupIDs = append(groupIDs, group.ID)
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity_groups(id,provider_id,external_id,display_name,source,enabled)
			VALUES($1,$2,$3,$4,'oidc',true)
			ON CONFLICT(provider_id,external_id) DO UPDATE SET display_name=excluded.display_name,updated_at=now()`,
			group.ID, providerID, group.ExternalID, group.DisplayName); err != nil {
			return err
		}
		var resolvedID string
		if err := tx.QueryRow(ctx, `SELECT id FROM identity_groups WHERE provider_id=$1 AND external_id=$2`, providerID, group.ExternalID).Scan(&resolvedID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity_group_memberships(group_id,user_id,source)
			VALUES($1,$2,'oidc')
			ON CONFLICT(group_id,user_id) DO UPDATE SET source='oidc',updated_at=now()`, resolvedID, userID); err != nil {
			return err
		}
		groupIDs[len(groupIDs)-1] = resolvedID
	}
	if len(groupIDs) == 0 {
		_, err = tx.Exec(ctx, `
			DELETE FROM identity_group_memberships m
			USING identity_groups g
			WHERE m.group_id=g.id AND m.user_id=$1 AND m.source='oidc' AND g.provider_id=$2`, userID, providerID)
	} else {
		_, err = tx.Exec(ctx, `
			DELETE FROM identity_group_memberships m
			USING identity_groups g
			WHERE m.group_id=g.id AND m.user_id=$1 AND m.source='oidc' AND g.provider_id=$2
			  AND NOT (m.group_id = ANY($3))`, userID, providerID, groupIDs)
	}
	if err != nil {
		return err
	}
	return commitAccessChange(ctx, tx)
}

func (s *Store) GuestIdentityBindingsForDesktop(ctx context.Context, vmid int) ([]GuestIdentityBinding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT desktop_vmid,user_id,COALESCE(profile_id,''),guest_username,state,last_error,created_at,updated_at
		FROM guest_identity_bindings WHERE desktop_vmid=$1 ORDER BY guest_username`, vmid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := make([]GuestIdentityBinding, 0)
	for rows.Next() {
		var binding GuestIdentityBinding
		if err := rows.Scan(&binding.DesktopVMID, &binding.UserID, &binding.ProfileID, &binding.GuestUsername, &binding.State, &binding.LastError, &binding.CreatedAt, &binding.UpdatedAt); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

func (s *Store) GuestIdentityBindings(ctx context.Context) ([]GuestIdentityBinding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT desktop_vmid,user_id,COALESCE(profile_id,''),guest_username,state,last_error,created_at,updated_at
		FROM guest_identity_bindings ORDER BY desktop_vmid,guest_username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := make([]GuestIdentityBinding, 0)
	for rows.Next() {
		var binding GuestIdentityBinding
		if err := rows.Scan(&binding.DesktopVMID, &binding.UserID, &binding.ProfileID, &binding.GuestUsername, &binding.State, &binding.LastError, &binding.CreatedAt, &binding.UpdatedAt); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

func (s *Store) PutGuestIdentityBinding(ctx context.Context, binding GuestIdentityBinding) (GuestIdentityBinding, error) {
	if binding.State == "" {
		binding.State = "provisioning"
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO guest_identity_bindings(desktop_vmid,user_id,profile_id,guest_username,state,last_error)
		VALUES($1,$2,NULLIF($3,''),$4,$5,$6)
		ON CONFLICT(desktop_vmid,user_id) DO UPDATE SET profile_id=excluded.profile_id,
		  guest_username=excluded.guest_username,state=excluded.state,last_error=excluded.last_error,updated_at=now()
		RETURNING desktop_vmid,user_id,COALESCE(profile_id,''),guest_username,state,last_error,created_at,updated_at`,
		binding.DesktopVMID, binding.UserID, binding.ProfileID, binding.GuestUsername, binding.State, binding.LastError).Scan(
		&binding.DesktopVMID, &binding.UserID, &binding.ProfileID, &binding.GuestUsername,
		&binding.State, &binding.LastError, &binding.CreatedAt, &binding.UpdatedAt,
	)
	return binding, err
}

func (s *Store) WithDesktopConnectionLock(ctx context.Context, vmid int, action func() error) error {
	unlock, err := s.AcquireDesktopControlLock(ctx, vmid)
	if err != nil {
		return err
	}
	defer unlock()
	return action()
}

// Shared by native connections, computer actions and lease transitions on all
// replicas. Never acquire it recursively or inside a short access transaction.
func (s *Store) AcquireDesktopControlLock(ctx context.Context, vmid int) (func(), error) {
	if vmid <= 0 {
		return nil, errors.New("desktop identifier is invalid")
	}
	connection, err := s.controlLocks.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	lockID := int64(729200000) + int64(vmid)
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		// A cancellation can race with lock acquisition on the server. Do not
		// return a connection with an uncertain session lock to the pool.
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = connection.Conn().Close(closeContext)
		cancel()
		connection.Release()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { releaseAdvisoryLock(connection, lockID); connection.Release() }) }, nil
}

// Never return a connection with an unknown session-lock state to the pool.
func releaseAdvisoryLock(connection *pgxpool.Conn, lockID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lockID); err != nil {
		_ = connection.Conn().Close(ctx)
	}
}

func (s *Store) CreateDesktopConnectionSession(ctx context.Context, session DesktopConnectionSession) (DesktopConnectionSession, error) {
	return s.createDesktopConnectionSession(ctx, session, nil)
}

func (s *Store) createDesktopConnectionSession(ctx context.Context, session DesktopConnectionSession, nativeDigest []byte) (DesktopConnectionSession, error) {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return DesktopConnectionSession{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	session, err = insertDesktopConnectionSession(ctx, tx, session, nativeDigest, false)
	if err != nil {
		return DesktopConnectionSession{}, err
	}
	return session, tx.Commit(ctx)
}

func insertDesktopConnectionSession(ctx context.Context, tx pgx.Tx, session DesktopConnectionSession, nativeDigest []byte, boundNative bool) (DesktopConnectionSession, error) {
	err := tx.QueryRow(ctx, `
		INSERT INTO desktop_connection_sessions(id,user_id,desktop_vmid,guest_username,state,expires_at,native_session_digest)
		SELECT $1,$2,$3,$4,'active',$5,$6
		WHERE EXISTS(SELECT 1 FROM effective_user_desktop_access WHERE user_id=$2 AND desktop_vmid=$3)
		  AND ($6::bytea IS NULL OR EXISTS(SELECT 1 FROM native_sessions WHERE token_digest=$6 AND user_id=$2 AND expires_at>now()))
		  AND NOT EXISTS(SELECT 1 FROM guest_identity_revocations WHERE desktop_vmid=$3 AND requested_revision>completed_revision)
		  AND ($7 OR NOT EXISTS(SELECT 1 FROM native_guest_accounts WHERE desktop_vmid=$3 AND
		    (user_id=$2 OR state='pending' OR (operation='issue' AND state='applied'))))
		RETURNING id,user_id,desktop_vmid,guest_username,state,expires_at,created_at,updated_at,closed_at`,
		session.ID, session.UserID, session.DesktopVMID, session.GuestUsername, session.ExpiresAt, nativeDigest, boundNative).Scan(
		&session.ID, &session.UserID, &session.DesktopVMID, &session.GuestUsername, &session.State,
		&session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt, &session.ClosedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DesktopConnectionSession{}, ErrNotFound
		}
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return DesktopConnectionSession{}, ErrConflict
		}
		return DesktopConnectionSession{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE guest_identity_bindings SET session_expires_at=$3,updated_at=now() WHERE desktop_vmid=$1 AND user_id=$2`, session.DesktopVMID, session.UserID, session.ExpiresAt); err != nil {
		return DesktopConnectionSession{}, err
	}
	return session, nil
}

func (s *Store) BeginRevokeDesktopConnections(ctx context.Context, vmid int) ([]DesktopConnectionSession, error) {
	return s.beginRevokeDesktopConnections(ctx, `desktop_vmid=$1`, vmid)
}

func (s *Store) DesktopConnectionByID(ctx context.Context, id string) (DesktopConnectionSession, error) {
	var session DesktopConnectionSession
	err := s.pool.QueryRow(ctx, `SELECT id,user_id,desktop_vmid,guest_username,state,expires_at,created_at,updated_at,closed_at
		FROM desktop_connection_sessions WHERE id=$1`, id).Scan(
		&session.ID, &session.UserID, &session.DesktopVMID, &session.GuestUsername, &session.State,
		&session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt, &session.ClosedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DesktopConnectionSession{}, ErrNotFound
	}
	return session, err
}

func (s *Store) BeginRevokeUserDesktopConnections(ctx context.Context, userID string) ([]DesktopConnectionSession, error) {
	return s.beginRevokeDesktopConnections(ctx, `user_id=$1`, userID)
}

func (s *Store) BeginRevokeDesktopConnection(ctx context.Context, id, userID string) (DesktopConnectionSession, error) {
	var session DesktopConnectionSession
	err := s.pool.QueryRow(ctx, `
		UPDATE desktop_connection_sessions SET state='revoking',updated_at=now()
		WHERE id=$1 AND user_id=$2 AND state IN ('active','revoking')
		RETURNING id,user_id,desktop_vmid,guest_username,state,expires_at,created_at,updated_at,closed_at`, id, userID).Scan(
		&session.ID, &session.UserID, &session.DesktopVMID, &session.GuestUsername, &session.State,
		&session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt, &session.ClosedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.pool.QueryRow(ctx, `
			SELECT id,user_id,desktop_vmid,guest_username,state,expires_at,created_at,updated_at,closed_at
			FROM desktop_connection_sessions
			WHERE id=$1 AND user_id=$2 AND state IN ('revoked','expired')`, id, userID).Scan(
			&session.ID, &session.UserID, &session.DesktopVMID, &session.GuestUsername, &session.State,
			&session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt, &session.ClosedAt,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return DesktopConnectionSession{}, ErrNotFound
		}
	}
	return session, err
}

func (s *Store) beginRevokeDesktopConnections(ctx context.Context, predicate string, value any) ([]DesktopConnectionSession, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE desktop_connection_sessions SET state='revoking',updated_at=now()
		WHERE `+predicate+` AND state IN ('active','revoking')
		RETURNING id,user_id,desktop_vmid,guest_username,state,expires_at,created_at,updated_at,closed_at`, value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := make([]DesktopConnectionSession, 0)
	for rows.Next() {
		var session DesktopConnectionSession
		if err := rows.Scan(&session.ID, &session.UserID, &session.DesktopVMID, &session.GuestUsername, &session.State, &session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt, &session.ClosedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) DesktopConnectionsDueForRevocation(ctx context.Context, limit int) ([]DesktopConnectionSession, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	// Serialize ticket expiry/closure with Gateway redemption and renewal. A
	// snapshot taken before a just-committed renewal must not retire a live
	// transport. Preserve ordinary disconnect semantics (retain OS desktop).
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		WITH due AS (
		  SELECT id FROM desktop_connection_sessions c
		  WHERE state='revoking' OR (state='active' AND (expires_at <= statement_timestamp()
		    OR EXISTS(SELECT 1 FROM gateway_session_tickets g WHERE g.connection_id=c.id AND
		      ((g.consumed_at IS NULL AND g.expires_at<=statement_timestamp()) OR
		       (g.consumed_at IS NOT NULL AND (g.closed_at IS NOT NULL OR g.lease_until<=statement_timestamp()))))))
		  ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT $1
		)
		UPDATE desktop_connection_sessions s SET state='revoking',updated_at=now()
		FROM due WHERE s.id=due.id
		RETURNING s.id,s.user_id,s.desktop_vmid,s.guest_username,s.state,s.expires_at,s.created_at,s.updated_at,s.closed_at`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := make([]DesktopConnectionSession, 0)
	for rows.Next() {
		var session DesktopConnectionSession
		if err := rows.Scan(&session.ID, &session.UserID, &session.DesktopVMID, &session.GuestUsername, &session.State, &session.ExpiresAt, &session.CreatedAt, &session.UpdatedAt, &session.ClosedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (s *Store) MarkDesktopConnectionClosed(ctx context.Context, id, state string) error {
	if state != "revoked" && state != "expired" {
		return errors.New("desktop connection terminal state is invalid")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE desktop_connection_sessions SET state=$2,closed_at=now(),updated_at=now()
		WHERE id=$1 AND state='revoking'`, id, state)
	return err
}

func (s *Store) CreateSession(ctx context.Context, tokenDigest []byte, userID, csrfToken string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO web_sessions(token_digest,user_id,csrf_token,expires_at) VALUES($1,$2,$3,$4)`, tokenDigest, userID, csrfToken, expiresAt)
	return err
}

func (s *Store) APITokens(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,name,expires_at,created_at,last_used_at
		FROM api_tokens WHERE user_id=$1 ORDER BY created_at DESC,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := make([]APIToken, 0)
	for rows.Next() {
		var token APIToken
		if err := rows.Scan(&token.ID, &token.Name, &token.ExpiresAt, &token.CreatedAt, &token.LastUsedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s *Store) CreateAPIToken(ctx context.Context, userID string, token APIToken, tokenDigest []byte) (APIToken, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO api_tokens(id,user_id,name,token_digest,expires_at)
		VALUES($1,$2,$3,$4,$5)
		RETURNING id,name,expires_at,created_at,last_used_at`, token.ID, userID, token.Name, tokenDigest, token.ExpiresAt).Scan(
		&token.ID, &token.Name, &token.ExpiresAt, &token.CreatedAt, &token.LastUsedAt,
	)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return APIToken{}, ErrConflict
		}
		return APIToken{}, err
	}
	return token, nil
}

func (s *Store) DeleteAPIToken(ctx context.Context, userID, id string) (bool, error) {
	result, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE id=$1 AND user_id=$2`, id, userID)
	return err == nil && result.RowsAffected() == 1, err
}

func (s *Store) SessionByAPIToken(ctx context.Context, tokenDigest []byte) (Session, error) {
	var session Session
	err := s.pool.QueryRow(ctx, `
		WITH matched AS (
		  UPDATE api_tokens SET last_used_at=now()
		  WHERE token_digest=$1 AND expires_at>now()
		  RETURNING id,user_id,expires_at
		)
		SELECT u.id,u.username,u.display_name,COALESCE(u.password_hash,''),u.role,u.disabled,u.created_at,m.id,m.expires_at
		FROM matched m JOIN users u ON u.id=m.user_id WHERE u.disabled=false`, tokenDigest).Scan(
		&session.User.ID, &session.User.Username, &session.User.DisplayName, &session.User.PasswordHash,
		&session.User.Role, &session.User.Disabled, &session.User.CreatedAt, &session.CredentialID, &session.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err == nil {
		session.AuthKind = "api_token"
	}
	return session, err
}

func (s *Store) SessionByToken(ctx context.Context, tokenDigest []byte) (Session, error) {
	var session Session
	err := s.pool.QueryRow(ctx, `
		SELECT u.id,u.username,u.display_name,COALESCE(u.password_hash,''),u.role,u.disabled,u.created_at,s.csrf_token,s.expires_at
		FROM web_sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_digest=$1 AND s.expires_at > now() AND u.disabled=false`, tokenDigest).Scan(
		&session.User.ID, &session.User.Username, &session.User.DisplayName, &session.User.PasswordHash, &session.User.Role,
		&session.User.Disabled, &session.User.CreatedAt, &session.CSRFToken, &session.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err == nil {
		session.AuthKind = "web"
		_, _ = s.pool.Exec(ctx, `UPDATE web_sessions SET last_seen_at=now() WHERE token_digest=$1`, tokenDigest)
	}
	return session, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenDigest []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM web_sessions WHERE token_digest=$1`, tokenDigest)
	return err
}

func (s *Store) EnsureOIDCProvider(ctx context.Context, id, name, issuer, clientID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO oidc_providers(id,name,issuer,client_id,enabled)
		VALUES($1,$2,$3,$4,true)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,issuer=excluded.issuer,client_id=excluded.client_id,enabled=true,updated_at=now()`,
		id, name, issuer, clientID)
	return err
}

func (s *Store) CreateOIDCRequest(ctx context.Context, stateDigest []byte, nonce, pkceVerifier, clientKind string, expiresAt time.Time) error {
	_, _ = s.pool.Exec(ctx, `DELETE FROM oidc_auth_requests WHERE expires_at <= now()`)
	_, err := s.pool.Exec(ctx, `INSERT INTO oidc_auth_requests(state_digest,nonce,pkce_verifier,client_kind,expires_at) VALUES($1,$2,$3,$4,$5)`, stateDigest, nonce, pkceVerifier, clientKind, expiresAt)
	return err
}

func (s *Store) ConsumeOIDCRequest(ctx context.Context, stateDigest []byte) (OIDCRequest, error) {
	var request OIDCRequest
	err := s.pool.QueryRow(ctx, `DELETE FROM oidc_auth_requests WHERE state_digest=$1 AND expires_at > now() RETURNING nonce,pkce_verifier,client_kind`, stateDigest).Scan(&request.Nonce, &request.PKCEVerifier, &request.ClientKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return OIDCRequest{}, ErrNotFound
	}
	return request, err
}

func (s *Store) CreateNativeAuthCode(ctx context.Context, codeDigest []byte, userID string, expiresAt time.Time) error {
	_, _ = s.pool.Exec(ctx, `DELETE FROM native_auth_codes WHERE expires_at <= now()`)
	_, err := s.pool.Exec(ctx, `INSERT INTO native_auth_codes(code_digest,user_id,expires_at) VALUES($1,$2,$3)`, codeDigest, userID, expiresAt)
	return err
}

func (s *Store) ConsumeNativeAuthCode(ctx context.Context, codeDigest []byte) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		WITH consumed AS (
		  DELETE FROM native_auth_codes WHERE code_digest=$1 AND expires_at > now() RETURNING user_id
		)
		SELECT u.id,u.username,u.display_name,COALESCE(u.password_hash,''),u.role,u.disabled,u.created_at
		FROM consumed c JOIN users u ON u.id=c.user_id`, codeDigest).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.Role, &user.Disabled, &user.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return user, err
}

func (s *Store) CreateNativeSession(ctx context.Context, tokenDigest []byte, userID string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO native_sessions(token_digest,user_id,expires_at) VALUES($1,$2,$3)`, tokenDigest, userID, expiresAt)
	return err
}

func (s *Store) NativeSessionByToken(ctx context.Context, tokenDigest []byte) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id,u.username,u.display_name,COALESCE(u.password_hash,''),u.role,u.disabled,u.created_at
		FROM native_sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_digest=$1 AND s.expires_at > now() AND u.disabled=false`, tokenDigest).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.Role, &user.Disabled, &user.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err == nil {
		_, _ = s.pool.Exec(ctx, `UPDATE native_sessions SET last_seen_at=now() WHERE token_digest=$1`, tokenDigest)
	}
	return user, err
}

func (s *Store) DeleteNativeSession(ctx context.Context, tokenDigest []byte) error {
	return s.RevokeNativeSession(ctx, tokenDigest)
}

func (s *Store) ResolveOIDCUser(ctx context.Context, providerID, subject string, proposed User) (User, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var user User
	err = tx.QueryRow(ctx, `
		SELECT u.id,u.username,u.display_name,COALESCE(u.password_hash,''),u.role,u.disabled,u.created_at
		FROM external_identities e JOIN users u ON u.id=e.user_id
		WHERE e.provider_id=$1 AND e.subject=$2`, providerID, subject).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.Role, &user.Disabled, &user.CreatedAt,
	)
	if err == nil {
		return user, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return User{}, err
	}
	username := proposed.Username
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE username_normalized=$1)`, strings.ToLower(username)).Scan(&taken); err != nil {
		return User{}, err
	}
	if taken {
		suffix := proposed.ID
		if len(suffix) > 8 {
			suffix = suffix[:8]
		}
		username = username + "-" + strings.ToLower(suffix)
	}
	_, err = tx.Exec(ctx, `INSERT INTO users(id,username,username_normalized,display_name,password_hash,role) VALUES($1,$2,$3,$4,NULL,'user')`,
		proposed.ID, username, strings.ToLower(username), proposed.DisplayName)
	if err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO external_identities(provider_id,subject,user_id) VALUES($1,$2,$3)`, providerID, subject, proposed.ID); err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, err
	}
	proposed.Username = username
	proposed.Role = "user"
	return proposed, nil
}

func (s *Store) CreateDesktopLease(ctx context.Context, lease DesktopLease) (DesktopLease, error) {
	tx, err := s.beginAccessChange(ctx)
	if err != nil {
		return DesktopLease{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var allowed, humanControlled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_desktop_assignments a
		JOIN agent_principals p ON p.id=a.agent_id JOIN managed_desktops d ON d.vmid=a.desktop_vmid
		WHERE a.agent_id=$1 AND d.vmid::text=$2 AND p.enabled AND d.enabled AND d.present AND NOT EXISTS (SELECT 1 FROM pve_jobs clone_job WHERE clone_job.target_vmid=d.vmid AND clone_job.operation='pve.template_clone' AND clone_job.state<>'succeeded')),
		EXISTS(SELECT 1 FROM desktop_connection_sessions WHERE desktop_vmid::text=$2 AND state IN ('active','revoking'))`, lease.AgentID, lease.DesktopID).Scan(&allowed, &humanControlled); err != nil {
		return DesktopLease{}, err
	}
	if !allowed {
		return DesktopLease{}, ErrNotFound
	}
	if humanControlled {
		return DesktopLease{}, ErrAlreadyLeased
	}
	if _, err := tx.Exec(ctx, `UPDATE desktop_leases SET state='expired',updated_at=now() WHERE state='active' AND expires_at <= now()`); err != nil {
		return DesktopLease{}, err
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO desktop_leases(id,agent_id,desktop_id,state,expires_at)
		VALUES($1,$2,$3,'active',$4)
		RETURNING id,agent_id,desktop_id,state,control_epoch,expires_at,created_at,updated_at`,
		lease.ID, lease.AgentID, lease.DesktopID, lease.ExpiresAt).Scan(
		&lease.ID, &lease.AgentID, &lease.DesktopID, &lease.State, &lease.ControlEpoch, &lease.ExpiresAt, &lease.CreatedAt, &lease.UpdatedAt,
	)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return DesktopLease{}, ErrAlreadyLeased
		}
		return DesktopLease{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DesktopLease{}, err
	}
	return lease, nil
}

func (s *Store) DesktopLeaseByID(ctx context.Context, id string) (DesktopLease, error) {
	var lease DesktopLease
	err := s.pool.QueryRow(ctx, `
		SELECT id,agent_id,desktop_id,
		  CASE WHEN state='active' AND expires_at <= now() THEN 'expired' ELSE state END,
		  control_epoch,expires_at,created_at,updated_at
		FROM desktop_leases WHERE id=$1`, id).Scan(
		&lease.ID, &lease.AgentID, &lease.DesktopID, &lease.State, &lease.ControlEpoch, &lease.ExpiresAt, &lease.CreatedAt, &lease.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DesktopLease{}, ErrNotFound
	}
	return lease, err
}

func (s *Store) ActiveDesktopLeases(ctx context.Context) ([]DesktopLease, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,agent_id,desktop_id,state,control_epoch,expires_at,created_at,updated_at
		FROM desktop_leases
		WHERE state='active' AND expires_at > now()
		ORDER BY expires_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	leases := make([]DesktopLease, 0)
	for rows.Next() {
		var lease DesktopLease
		if err := rows.Scan(
			&lease.ID, &lease.AgentID, &lease.DesktopID, &lease.State, &lease.ControlEpoch,
			&lease.ExpiresAt, &lease.CreatedAt, &lease.UpdatedAt,
		); err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	return leases, rows.Err()
}

func (s *Store) ReleaseDesktopLease(ctx context.Context, id string) (DesktopLease, error) {
	var lease DesktopLease
	err := s.pool.QueryRow(ctx, `
		UPDATE desktop_leases SET state='released',control_epoch=control_epoch+1,updated_at=now()
		WHERE id=$1 AND state='active'
		RETURNING id,agent_id,desktop_id,state,control_epoch,expires_at,created_at,updated_at`, id).Scan(
		&lease.ID, &lease.AgentID, &lease.DesktopID, &lease.State, &lease.ControlEpoch, &lease.ExpiresAt, &lease.CreatedAt, &lease.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DesktopLease{}, ErrNotFound
	}
	return lease, err
}

func (s *Store) RevokeDesktopLeases(ctx context.Context, vmid int) ([]DesktopLease, error) {
	if vmid <= 0 {
		return nil, errors.New("desktop identifier is invalid")
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE desktop_leases SET state='revoked',control_epoch=control_epoch+1,updated_at=now()
		WHERE desktop_id=$1 AND state='active'
		RETURNING id,agent_id,desktop_id,state,control_epoch,expires_at,created_at,updated_at`, strconv.Itoa(vmid))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	leases := make([]DesktopLease, 0, 1)
	for rows.Next() {
		var lease DesktopLease
		if err := rows.Scan(&lease.ID, &lease.AgentID, &lease.DesktopID, &lease.State, &lease.ControlEpoch, &lease.ExpiresAt, &lease.CreatedAt, &lease.UpdatedAt); err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	return leases, rows.Err()
}

func (s *Store) Audit(ctx context.Context, actorID, eventType, targetType, targetID string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return err
	}
	encoded, err := json.Marshal(sanitizeAuditValue(normalized, 0))
	if err != nil {
		return err
	}
	outcome := auditOutcome(eventType)
	_, err = s.pool.Exec(ctx, `INSERT INTO audit_events(actor_id,event_type,outcome,target_type,target_id,detail) VALUES(NULLIF($1,''),$2,$3,NULLIF($4,''),NULLIF($5,''),$6)`, actorID, eventType, outcome, targetType, targetID, encoded)
	return err
}

func auditOutcome(eventType string) string {
	if strings.HasSuffix(eventType, "failed") || strings.HasSuffix(eventType, "rejected") || strings.HasSuffix(eventType, "denied") {
		return "failure"
	}
	return "success"
}

func (s *Store) AuditEvents(ctx context.Context, query AuditEventQuery) (AuditEventPage, error) {
	if query.Limit < 1 || query.Limit > 100 || query.Before < 0 || (query.Outcome != "" && query.Outcome != "success" && query.Outcome != "failure") {
		return AuditEventPage{}, errors.New("audit event query is invalid")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT e.id,COALESCE(e.actor_id,''),COALESCE(u.username,a.id,''),COALESCE(u.display_name,a.display_name,''),
		       e.event_type,e.outcome,COALESCE(e.target_type,''),COALESCE(e.target_id,''),e.detail,e.occurred_at
		FROM audit_events e
		LEFT JOIN users u ON u.id=e.actor_id
		LEFT JOIN agent_principals a ON a.id=e.actor_id
		WHERE ($1='' OR e.outcome=$1)
		  AND ($2='' OR strpos(lower(concat_ws(' ',e.event_type,u.username,u.display_name,a.id,a.display_name,e.target_type,e.target_id)),lower($2))>0)
		  AND ($3::bigint=0 OR e.id<$3)
		ORDER BY e.id DESC
		LIMIT $4`, query.Outcome, query.Search, query.Before, query.Limit+1)
	if err != nil {
		return AuditEventPage{}, err
	}
	defer rows.Close()
	events := make([]AuditEvent, 0, query.Limit)
	for rows.Next() {
		var event AuditEvent
		if err := rows.Scan(&event.ID, &event.ActorID, &event.ActorUsername, &event.ActorDisplayName, &event.EventType, &event.Outcome, &event.TargetType, &event.TargetID, &event.Detail, &event.OccurredAt); err != nil {
			return AuditEventPage{}, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return AuditEventPage{}, err
	}
	page := AuditEventPage{}
	if len(events) > query.Limit {
		events = events[:query.Limit]
		page.NextCursor = strconv.FormatInt(events[len(events)-1].ID, 10)
	}
	page.Events = events
	return page, nil
}

func sanitizeAuditValue(value any, depth int) any {
	if depth >= 5 {
		return "[TRUNCATED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, min(len(typed), 64))
		count := 0
		for key, child := range typed {
			if count >= 64 {
				clean["truncated"] = true
				break
			}
			if auditKeyIsSensitive(key) {
				clean[key] = "[REDACTED]"
			} else {
				clean[key] = sanitizeAuditValue(child, depth+1)
			}
			count++
		}
		return clean
	case []any:
		limit := min(len(typed), 64)
		clean := make([]any, 0, limit)
		for _, child := range typed[:limit] {
			clean = append(clean, sanitizeAuditValue(child, depth+1))
		}
		return clean
	case string:
		if len(typed) > 1024 {
			return typed[:1024] + "…"
		}
		return typed
	default:
		return typed
	}
}

func auditKeyIsSensitive(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, fragment := range []string{"password", "secret", "token", "authorization", "cookie", "credential", "product_key", "private_key"} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func (s *Store) DesktopAccessPolicyByVMID(ctx context.Context, vmid int) (DesktopAccessPolicy, error) {
	var policy DesktopAccessPolicy
	err := s.pool.QueryRow(ctx, `
		SELECT vmid,privilege_mode,clipboard_redirection,drive_redirection,managed_background,
		       desired_revision,applied_revision,state,os_family,last_error,updated_at
		FROM desktop_access_policies WHERE vmid=$1`, vmid).Scan(
		&policy.VMID, &policy.PrivilegeMode, &policy.ClipboardRedirection, &policy.DriveRedirection, &policy.ManagedBackground,
		&policy.DesiredRevision, &policy.AppliedRevision,
		&policy.State, &policy.OSFamily, &policy.LastError, &policy.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DesktopAccessPolicy{}, ErrNotFound
	}
	return policy, err
}

func (s *Store) EnsureDesktopAccessPolicy(ctx context.Context, vmid int) (DesktopAccessPolicy, error) {
	if vmid <= 0 {
		return DesktopAccessPolicy{}, errors.New("desktop access policy VMID is invalid")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO desktop_access_policies(vmid) VALUES($1)
		ON CONFLICT(vmid) DO NOTHING`, vmid); err != nil {
		return DesktopAccessPolicy{}, err
	}
	return s.DesktopAccessPolicyByVMID(ctx, vmid)
}

func (s *Store) PutDesktopAccessPolicy(ctx context.Context, vmid int, privilegeMode string, clipboardRedirection, driveRedirection, managedBackground bool, actorID string) (DesktopAccessPolicy, error) {
	if vmid <= 0 || (privilegeMode != "standard" && privilegeMode != "local_admin") {
		return DesktopAccessPolicy{}, errors.New("desktop access policy is invalid")
	}
	var policy DesktopAccessPolicy
	err := s.pool.QueryRow(ctx, `
		INSERT INTO desktop_access_policies(vmid,privilege_mode,clipboard_redirection,drive_redirection,managed_background,updated_by)
		VALUES($1,$2,$3,$4,$5,NULLIF($6,''))
		ON CONFLICT(vmid) DO UPDATE SET
			privilege_mode=excluded.privilege_mode,
			clipboard_redirection=excluded.clipboard_redirection,
			drive_redirection=excluded.drive_redirection,
			managed_background=excluded.managed_background,
			desired_revision=CASE
				WHEN desktop_access_policies.privilege_mode<>excluded.privilege_mode
				  OR desktop_access_policies.clipboard_redirection<>excluded.clipboard_redirection
				  OR desktop_access_policies.drive_redirection<>excluded.drive_redirection
				  OR desktop_access_policies.managed_background<>excluded.managed_background
				THEN desktop_access_policies.desired_revision+1
				ELSE desktop_access_policies.desired_revision
			END,
			state='pending',last_error='',updated_by=excluded.updated_by,updated_at=now()
		RETURNING vmid,privilege_mode,clipboard_redirection,drive_redirection,managed_background,
		          desired_revision,applied_revision,state,os_family,last_error,updated_at`,
		vmid, privilegeMode, clipboardRedirection, driveRedirection, managedBackground, actorID).Scan(
		&policy.VMID, &policy.PrivilegeMode, &policy.ClipboardRedirection, &policy.DriveRedirection, &policy.ManagedBackground,
		&policy.DesiredRevision, &policy.AppliedRevision,
		&policy.State, &policy.OSFamily, &policy.LastError, &policy.UpdatedAt,
	)
	return policy, err
}

func (s *Store) MarkDesktopAccessPolicyApplied(ctx context.Context, vmid int, revision int64, osFamily string) (DesktopAccessPolicy, error) {
	var policy DesktopAccessPolicy
	err := s.pool.QueryRow(ctx, `
		UPDATE desktop_access_policies SET applied_revision=$2,state='applied',os_family=$3,last_error='',updated_at=now()
		WHERE vmid=$1 AND desired_revision=$2
		RETURNING vmid,privilege_mode,clipboard_redirection,drive_redirection,managed_background,
		          desired_revision,applied_revision,state,os_family,last_error,updated_at`,
		vmid, revision, osFamily).Scan(
		&policy.VMID, &policy.PrivilegeMode, &policy.ClipboardRedirection, &policy.DriveRedirection, &policy.ManagedBackground,
		&policy.DesiredRevision, &policy.AppliedRevision,
		&policy.State, &policy.OSFamily, &policy.LastError, &policy.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DesktopAccessPolicy{}, ErrNotFound
	}
	return policy, err
}

func (s *Store) MarkDesktopAccessPolicyFailed(ctx context.Context, vmid int, revision int64, osFamily, message string) (DesktopAccessPolicy, error) {
	if len(message) > 500 {
		message = message[:500]
	}
	var policy DesktopAccessPolicy
	err := s.pool.QueryRow(ctx, `
		UPDATE desktop_access_policies SET state='failed',os_family=$3,last_error=$4,updated_at=now()
		WHERE vmid=$1 AND desired_revision=$2
		RETURNING vmid,privilege_mode,clipboard_redirection,drive_redirection,managed_background,
		          desired_revision,applied_revision,state,os_family,last_error,updated_at`,
		vmid, revision, osFamily, message).Scan(
		&policy.VMID, &policy.PrivilegeMode, &policy.ClipboardRedirection, &policy.DriveRedirection, &policy.ManagedBackground,
		&policy.DesiredRevision, &policy.AppliedRevision,
		&policy.State, &policy.OSFamily, &policy.LastError, &policy.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DesktopAccessPolicy{}, ErrNotFound
	}
	return policy, err
}

func (s *Store) JobByIdempotencyKey(ctx context.Context, key string) (Job, error) {
	return s.jobByQuery(ctx, `SELECT id,idempotency_key,operation,state,COALESCE(source_vmid,0),COALESCE(target_vmid,0),COALESCE(target_node,''),COALESCE(task_node,''),COALESCE(upid,''),request,progress,detail,COALESCE(error,''),COALESCE(created_by,''),created_at,updated_at FROM pve_jobs WHERE idempotency_key=$1`, key)
}

func (s *Store) JobByID(ctx context.Context, id string) (Job, error) {
	return s.jobByQuery(ctx, `SELECT id,idempotency_key,operation,state,COALESCE(source_vmid,0),COALESCE(target_vmid,0),COALESCE(target_node,''),COALESCE(task_node,''),COALESCE(upid,''),request,progress,detail,COALESCE(error,''),COALESCE(created_by,''),created_at,updated_at FROM pve_jobs WHERE id=$1`, id)
}

func (s *Store) jobByQuery(ctx context.Context, query, value string) (Job, error) {
	var job Job
	err := s.pool.QueryRow(ctx, query, value).Scan(
		&job.ID, &job.IdempotencyKey, &job.Operation, &job.State, &job.SourceVMID, &job.TargetVMID,
		&job.TargetNode, &job.TaskNode, &job.UPID, &job.Request, &job.Progress, &job.Detail,
		&job.Error, &job.CreatedBy, &job.CreatedAt, &job.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return job, err
}

func (s *Store) CreateJob(ctx context.Context, job Job) (Job, bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO pve_jobs(id,idempotency_key,operation,state,source_vmid,target_vmid,target_node,task_node,request,created_by,request_fingerprint) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(idempotency_key) DO NOTHING`,
		job.ID, job.IdempotencyKey, job.Operation, job.State, nullableInt(job.SourceVMID), nullableInt(job.TargetVMID), nullableString(job.TargetNode), nullableString(job.TaskNode), job.Request, nullableString(job.CreatedBy), job.RequestFingerprint)
	if err != nil {
		return Job{}, false, err
	}
	stored, err := s.JobForRequest(ctx, job.IdempotencyKey, job.RequestFingerprint)
	return stored, tag.RowsAffected() == 1, err
}

// A fingerprint is immutable once a job has claimed its scoped key. Check both
// on replay and after INSERT's conflict branch to close the concurrent race.
func (s *Store) JobForRequest(ctx context.Context, key, fingerprint string) (Job, error) {
	var saved string
	err := s.pool.QueryRow(ctx, `SELECT request_fingerprint FROM pve_jobs WHERE idempotency_key=$1`, key).Scan(&saved)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if saved != fingerprint {
		return Job{}, ErrConflict
	}
	return s.JobByIdempotencyKey(ctx, key)
}

func (s *Store) UpdateJobTask(ctx context.Context, id, state, upid, errorMessage string) error {
	_, err := s.pool.Exec(ctx, `UPDATE pve_jobs SET state=$2,upid=NULLIF($3,''),error=NULLIF($4,''),updated_at=now() WHERE id=$1`, id, state, upid, errorMessage)
	return err
}

func (s *Store) CompleteJobTask(ctx context.Context, id, state, upid, errorMessage string) (bool, error) {
	result, err := s.pool.Exec(ctx, `UPDATE pve_jobs SET state=$2,upid=NULLIF($3,''),error=NULLIF($4,''),updated_at=now() WHERE id=$1 AND state='running'`, id, state, upid, errorMessage)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

func (s *Store) UpdateJobProgress(ctx context.Context, id, state string, progress int, detail, errorMessage string) error {
	_, err := s.pool.Exec(ctx, `UPDATE pve_jobs SET state=$2,progress=$3,detail=$4,error=NULLIF($5,''),updated_at=now() WHERE id=$1`, id, state, progress, detail, errorMessage)
	return err
}

func (s *Store) ActiveImageBuildJob(ctx context.Context, profileID string) (Job, error) {
	return s.jobByQuery(ctx, `SELECT id,idempotency_key,operation,state,COALESCE(source_vmid,0),COALESCE(target_vmid,0),COALESCE(target_node,''),COALESCE(task_node,''),COALESCE(upid,''),request,progress,detail,COALESCE(error,''),COALESCE(created_by,''),created_at,updated_at FROM pve_jobs WHERE operation='image.build' AND state IN ('accepted','running') AND request->>'image_profile_id'=$1 ORDER BY created_at DESC LIMIT 1`, profileID)
}

func (s *Store) JobsByOperation(ctx context.Context, operation string, limit int) ([]Job, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `SELECT id,idempotency_key,operation,state,COALESCE(source_vmid,0),COALESCE(target_vmid,0),COALESCE(target_node,''),COALESCE(task_node,''),COALESCE(upid,''),request,progress,detail,COALESCE(error,''),COALESCE(created_by,''),created_at,updated_at FROM pve_jobs WHERE operation=$1 ORDER BY created_at DESC LIMIT $2`, operation, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]Job, 0)
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.IdempotencyKey, &job.Operation, &job.State, &job.SourceVMID, &job.TargetVMID,
			&job.TargetNode, &job.TaskNode, &job.UPID, &job.Request, &job.Progress, &job.Detail,
			&job.Error, &job.CreatedBy, &job.CreatedAt, &job.UpdatedAt); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) Jobs(ctx context.Context, limit int) ([]Job, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id,idempotency_key,operation,state,COALESCE(source_vmid,0),COALESCE(target_vmid,0),COALESCE(target_node,''),COALESCE(task_node,''),COALESCE(upid,''),request,progress,detail,COALESCE(error,''),COALESCE(created_by,''),created_at,updated_at FROM pve_jobs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]Job, 0)
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.IdempotencyKey, &job.Operation, &job.State, &job.SourceVMID, &job.TargetVMID,
			&job.TargetNode, &job.TaskNode, &job.UPID, &job.Request, &job.Progress, &job.Detail,
			&job.Error, &job.CreatedBy, &job.CreatedAt, &job.UpdatedAt); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) ImageProfiles(ctx context.Context) ([]ImageProfile, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,display_name,os_family,os_version,architecture,lifecycle,enabled,
		  source_node,source_iso,source_iso_checksum,driver_iso,driver_iso_checksum,COALESCE(template_vmid,0),mirror_url,
		  security_mirror_url,storage_pool,bridge,windows_image_name,
		  default_cores,default_memory_mb,default_disk_gb,firmware,tpm_version,agent_kind,
		  desktop_protocol,default_gpu_profile_id,build_status,status_detail,updated_at
		FROM image_profiles ORDER BY CASE os_family WHEN 'linux' THEN 0 ELSE 1 END,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []ImageProfile
	for rows.Next() {
		var profile ImageProfile
		if err := rows.Scan(
			&profile.ID, &profile.DisplayName, &profile.OSFamily, &profile.OSVersion, &profile.Architecture,
			&profile.Lifecycle, &profile.Enabled, &profile.SourceNode, &profile.SourceISO, &profile.SourceISOChecksum,
			&profile.DriverISO, &profile.DriverISOChecksum, &profile.TemplateVMID, &profile.MirrorURL,
			&profile.SecurityMirrorURL, &profile.StoragePool, &profile.Bridge, &profile.WindowsImageName,
			&profile.DefaultCores, &profile.DefaultMemoryMB,
			&profile.DefaultDiskGB, &profile.Firmware, &profile.TPMVersion, &profile.AgentKind, &profile.DesktopProtocol,
			&profile.DefaultGPUProfileID, &profile.BuildStatus, &profile.StatusDetail, &profile.UpdatedAt,
		); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *Store) ImageProfileByID(ctx context.Context, id string) (ImageProfile, error) {
	var profile ImageProfile
	err := s.pool.QueryRow(ctx, `
		SELECT id,display_name,os_family,os_version,architecture,lifecycle,enabled,
		  source_node,source_iso,source_iso_checksum,driver_iso,driver_iso_checksum,COALESCE(template_vmid,0),mirror_url,
		  security_mirror_url,storage_pool,bridge,windows_image_name,
		  default_cores,default_memory_mb,default_disk_gb,firmware,tpm_version,agent_kind,
		  desktop_protocol,default_gpu_profile_id,build_status,status_detail,updated_at
		FROM image_profiles WHERE id=$1`, id).Scan(
		&profile.ID, &profile.DisplayName, &profile.OSFamily, &profile.OSVersion, &profile.Architecture,
		&profile.Lifecycle, &profile.Enabled, &profile.SourceNode, &profile.SourceISO, &profile.SourceISOChecksum,
		&profile.DriverISO, &profile.DriverISOChecksum, &profile.TemplateVMID, &profile.MirrorURL,
		&profile.SecurityMirrorURL, &profile.StoragePool, &profile.Bridge, &profile.WindowsImageName,
		&profile.DefaultCores, &profile.DefaultMemoryMB,
		&profile.DefaultDiskGB, &profile.Firmware, &profile.TPMVersion, &profile.AgentKind, &profile.DesktopProtocol,
		&profile.DefaultGPUProfileID, &profile.BuildStatus, &profile.StatusDetail, &profile.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ImageProfile{}, ErrNotFound
	}
	return profile, err
}

// Duplicate references are allowed in existing databases, but cannot safely
// determine clone eligibility, OS identity or default GPU placement. Count in
// the same statement snapshot rather than selecting an arbitrary first row.
func (s *Store) ImageProfileByTemplateVMID(ctx context.Context, vmid int) (ImageProfile, error) {
	var profile ImageProfile
	var matches int
	err := s.pool.QueryRow(ctx, `
		SELECT id,display_name,os_family,os_version,architecture,lifecycle,enabled,
		  source_node,source_iso,source_iso_checksum,driver_iso,driver_iso_checksum,COALESCE(template_vmid,0),mirror_url,
		  security_mirror_url,storage_pool,bridge,windows_image_name,
		  default_cores,default_memory_mb,default_disk_gb,firmware,tpm_version,agent_kind,
		  desktop_protocol,default_gpu_profile_id,build_status,status_detail,updated_at,count(*) OVER ()
		FROM image_profiles WHERE template_vmid=$1 AND template_vmid > 0`, vmid).Scan(
		&profile.ID, &profile.DisplayName, &profile.OSFamily, &profile.OSVersion, &profile.Architecture,
		&profile.Lifecycle, &profile.Enabled, &profile.SourceNode, &profile.SourceISO, &profile.SourceISOChecksum,
		&profile.DriverISO, &profile.DriverISOChecksum, &profile.TemplateVMID, &profile.MirrorURL,
		&profile.SecurityMirrorURL, &profile.StoragePool, &profile.Bridge, &profile.WindowsImageName,
		&profile.DefaultCores, &profile.DefaultMemoryMB,
		&profile.DefaultDiskGB, &profile.Firmware, &profile.TPMVersion, &profile.AgentKind, &profile.DesktopProtocol,
		&profile.DefaultGPUProfileID, &profile.BuildStatus, &profile.StatusDetail, &profile.UpdatedAt, &matches,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ImageProfile{}, ErrNotFound
	}
	if err == nil && matches != 1 {
		return ImageProfile{}, ErrConflict
	}
	return profile, err
}

func (s *Store) UpdateImageProfile(ctx context.Context, profile ImageProfile) (ImageProfile, error) {
	var updated ImageProfile
	err := s.pool.QueryRow(ctx, `
		UPDATE image_profiles SET display_name=$2,enabled=$3,source_node=$4,source_iso=$5,
		  source_iso_checksum=$6,driver_iso=$7,driver_iso_checksum=$8,template_vmid=$9,mirror_url=$10,
		  security_mirror_url=$11,storage_pool=$12,bridge=$13,windows_image_name=$14,
		  default_gpu_profile_id=$15,build_status=$16,status_detail=$17,default_cores=$18,
		  default_memory_mb=$19,default_disk_gb=$20,firmware=$21,tpm_version=$22,updated_at=now()
		WHERE id=$1
		RETURNING id,display_name,os_family,os_version,architecture,lifecycle,enabled,
		  source_node,source_iso,source_iso_checksum,driver_iso,driver_iso_checksum,COALESCE(template_vmid,0),mirror_url,
		  security_mirror_url,storage_pool,bridge,windows_image_name,
		  default_cores,default_memory_mb,default_disk_gb,firmware,tpm_version,agent_kind,
		  desktop_protocol,default_gpu_profile_id,build_status,status_detail,updated_at`,
		profile.ID, profile.DisplayName, profile.Enabled, profile.SourceNode, profile.SourceISO,
		profile.SourceISOChecksum, profile.DriverISO, profile.DriverISOChecksum, nullableInt(profile.TemplateVMID),
		profile.MirrorURL, profile.SecurityMirrorURL, profile.StoragePool, profile.Bridge, profile.WindowsImageName,
		profile.DefaultGPUProfileID, profile.BuildStatus, profile.StatusDetail, profile.DefaultCores,
		profile.DefaultMemoryMB, profile.DefaultDiskGB, profile.Firmware, profile.TPMVersion,
	).Scan(
		&updated.ID, &updated.DisplayName, &updated.OSFamily, &updated.OSVersion, &updated.Architecture,
		&updated.Lifecycle, &updated.Enabled, &updated.SourceNode, &updated.SourceISO, &updated.SourceISOChecksum,
		&updated.DriverISO, &updated.DriverISOChecksum, &updated.TemplateVMID, &updated.MirrorURL,
		&updated.SecurityMirrorURL, &updated.StoragePool, &updated.Bridge, &updated.WindowsImageName,
		&updated.DefaultCores, &updated.DefaultMemoryMB,
		&updated.DefaultDiskGB, &updated.Firmware, &updated.TPMVersion, &updated.AgentKind, &updated.DesktopProtocol,
		&updated.DefaultGPUProfileID, &updated.BuildStatus, &updated.StatusDetail, &updated.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ImageProfile{}, ErrNotFound
	}
	return updated, err
}

func (s *Store) UpdateImageBuildState(ctx context.Context, id, state, detail string) error {
	_, err := s.pool.Exec(ctx, `UPDATE image_profiles SET build_status=$2,status_detail=$3,updated_at=now() WHERE id=$1`, id, state, detail)
	return err
}

func (s *Store) GPUProfiles(ctx context.Context) ([]GPUProfile, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,display_name,mode,vendor_id,device_class,resource_mapping,mdev_type,enabled,exclusive,allow_live_migration,updated_at
		FROM gpu_profiles ORDER BY CASE mode WHEN 'none' THEN 0 ELSE 1 END,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []GPUProfile
	for rows.Next() {
		var profile GPUProfile
		if err := rows.Scan(&profile.ID, &profile.DisplayName, &profile.Mode, &profile.VendorID, &profile.DeviceClass,
			&profile.ResourceMapping, &profile.MDevType, &profile.Enabled, &profile.Exclusive, &profile.AllowLiveMigration, &profile.UpdatedAt); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *Store) GPUProfileByID(ctx context.Context, id string) (GPUProfile, error) {
	var profile GPUProfile
	err := s.pool.QueryRow(ctx, `
		SELECT id,display_name,mode,vendor_id,device_class,resource_mapping,mdev_type,enabled,exclusive,allow_live_migration,updated_at
		FROM gpu_profiles WHERE id=$1`, id).Scan(
		&profile.ID, &profile.DisplayName, &profile.Mode, &profile.VendorID, &profile.DeviceClass,
		&profile.ResourceMapping, &profile.MDevType, &profile.Enabled, &profile.Exclusive, &profile.AllowLiveMigration, &profile.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return GPUProfile{}, ErrNotFound
	}
	return profile, err
}

func (s *Store) UpdateGPUProfile(ctx context.Context, profile GPUProfile) (GPUProfile, error) {
	var updated GPUProfile
	err := s.pool.QueryRow(ctx, `
		UPDATE gpu_profiles SET display_name=$2,resource_mapping=$3,enabled=$4,updated_at=now() WHERE id=$1
		RETURNING id,display_name,mode,vendor_id,device_class,resource_mapping,mdev_type,enabled,exclusive,allow_live_migration,updated_at`,
		profile.ID, profile.DisplayName, profile.ResourceMapping, profile.Enabled,
	).Scan(
		&updated.ID, &updated.DisplayName, &updated.Mode, &updated.VendorID, &updated.DeviceClass,
		&updated.ResourceMapping, &updated.MDevType, &updated.Enabled, &updated.Exclusive, &updated.AllowLiveMigration, &updated.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return GPUProfile{}, ErrNotFound
	}
	return updated, err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
