package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Veritas-Calculus/vc-workspace/assets/brand"
	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/config"
	"github.com/Veritas-Calculus/vc-workspace/internal/gateway"
	"github.com/Veritas-Calculus/vc-workspace/internal/guestdesktop"
	"github.com/Veritas-Calculus/vc-workspace/internal/imagebuilder"
	"github.com/Veritas-Calculus/vc-workspace/internal/oidcauth"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"golang.org/x/oauth2"
)

type Dependencies struct {
	Logger            *slog.Logger
	PVE               *pve.Client
	Store             *store.Store
	PublicURL         string
	SetupToken        string
	OIDC              *oidcauth.Service
	InternalAPIToken  string
	NativeCallbackURL string
	ImageBuilder      ImageBuilder
	Computer          computer.Executor
	IdentityFeatures  config.IdentityFeatures
	NativeGateway     *gateway.BrokerRoute
}

type ImageBuilder interface {
	Available() bool
	Build(context.Context, imagebuilder.Request, func(imagebuilder.Progress)) error
}

type ReadinessChecker interface {
	Ping(context.Context) error
}

type Server struct {
	logger            *slog.Logger
	pve               *pve.Client
	store             *store.Store
	publicURL         string
	setupToken        string
	oidc              *oidcauth.Service
	internalAPIToken  string
	nativeCallbackURL string
	imageBuilder      ImageBuilder
	computer          computer.Executor
	readiness         ReadinessChecker
	cookieSecure      bool
	startedAt         time.Time
	identityFeatures  config.IdentityFeatures
	nativeGateway     *gateway.BrokerRoute
}

const (
	workspaceSessionCookieName = "vc_workspace_session"
	legacySessionCookieName    = "vc_vdi_session"
	workspaceOIDCStateCookie   = "vc_workspace_oidc_state"
	legacyOIDCStateCookie      = "vc_vdi_oidc_state"
)

type desktopCloneRequest struct {
	Name                     string `json:"name"`
	SourceVMID               int    `json:"source_vmid"`
	TargetNode               string `json:"target_node"`
	Storage                  string `json:"storage"`
	Full                     bool   `json:"full"`
	GPUProfileID             string `json:"gpu_profile_id"`
	PCIResourceMapping       string `json:"pci_resource_mapping,omitempty"`
	MDevType                 string `json:"mdev_type,omitempty"`
	ValidationImageProfileID string `json:"validation_image_profile_id,omitempty"`
	SourceOSFamily           string `json:"source_os_family,omitempty"` // Server-owned admission snapshot.
	PVEPrincipal             string `json:"pve_principal,omitempty"`    // Server-owned submitting principal, not a secret.
}

type pciResourceMappingRequest struct {
	ID          string                           `json:"id"`
	Description string                           `json:"description"`
	MDev        bool                             `json:"mdev"`
	Entries     []pciResourceMappingEntryRequest `json:"entries"`
}

type desktopAccessPolicyRequest struct {
	PrivilegeMode        string `json:"privilege_mode"`
	ClipboardRedirection *bool  `json:"clipboard_redirection"`
	DriveRedirection     *bool  `json:"drive_redirection"`
	ManagedBackground    *bool  `json:"managed_background"`
}

type nativeSessionPolicy struct {
	Version              int    `json:"version"`
	Revision             int64  `json:"revision"`
	ClipboardRedirection bool   `json:"clipboard_redirection"`
	DriveRedirection     bool   `json:"drive_redirection"`
	ManagedBackground    bool   `json:"managed_background"`
	Hash                 string `json:"hash"`
}

type managedDesktopIdentityRequest struct {
	AccessMode        string `json:"access_mode"`
	OwnerUserID       string `json:"owner_user_id"`
	IdentityProfileID string `json:"identity_profile_id"`
}

type identityProfileRequest struct {
	DisplayName  string         `json:"display_name"`
	Platform     string         `json:"platform"`
	Mode         string         `json:"mode"`
	Enabled      *bool          `json:"enabled"`
	Experimental bool           `json:"experimental"`
	Config       map[string]any `json:"config"`
}

type identityGroupRequest struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Source      string `json:"source"`
	Enabled     *bool  `json:"enabled"`
}

type desktopIdentityReconcileRequest struct {
	JoinUsername string `json:"join_username"`
	JoinPassword string `json:"join_password"`
	ClientSecret string `json:"client_secret"`
}

type pciResourceMappingEntryRequest struct {
	Node     string `json:"node"`
	DeviceID string `json:"device_id"`
}

type imageProfileUpdateRequest struct {
	DisplayName         *string `json:"display_name"`
	Enabled             *bool   `json:"enabled"`
	SourceNode          *string `json:"source_node"`
	SourceISO           *string `json:"source_iso"`
	SourceISOChecksum   *string `json:"source_iso_checksum"`
	DriverISO           *string `json:"driver_iso"`
	DriverISOChecksum   *string `json:"driver_iso_checksum"`
	TemplateVMID        *int    `json:"template_vmid"`
	MirrorURL           *string `json:"mirror_url"`
	SecurityMirrorURL   *string `json:"security_mirror_url"`
	StoragePool         *string `json:"storage_pool"`
	Bridge              *string `json:"bridge"`
	WindowsImageName    *string `json:"windows_image_name"`
	DefaultCores        *int    `json:"default_cores"`
	DefaultMemoryMB     *int    `json:"default_memory_mb"`
	DefaultDiskGB       *int    `json:"default_disk_gb"`
	Firmware            *string `json:"firmware"`
	TPMVersion          *string `json:"tpm_version"`
	DefaultGPUProfileID *string `json:"default_gpu_profile_id"`
	BuildStatus         *string `json:"build_status"`
	StatusDetail        *string `json:"status_detail"`
}

type computerActionRequest struct {
	ControlEpoch  int64                   `json:"control_epoch"`
	Operation     computer.Operation      `json:"operation"`
	TimeoutMS     int                     `json:"timeout_ms,omitempty"`
	Screenshot    *computer.Screenshot    `json:"screenshot,omitempty"`
	Accessibility *computer.Accessibility `json:"accessibility,omitempty"`
	Mouse         *computer.Mouse         `json:"mouse,omitempty"`
	Key           *computer.Key           `json:"key,omitempty"`
	Text          *computer.Text          `json:"text,omitempty"`
}

func New(deps Dependencies) *Server {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	publicURL, _ := url.Parse(deps.PublicURL)
	var readiness ReadinessChecker
	if deps.Store != nil {
		readiness = deps.Store
	}
	computerExecutor := deps.Computer
	if computerExecutor == nil && deps.PVE != nil {
		computerExecutor = computer.NewPVEExecutor(deps.PVE)
	}
	return &Server{
		logger: logger, pve: deps.PVE, store: deps.Store, publicURL: deps.PublicURL,
		setupToken: deps.SetupToken, oidc: deps.OIDC, internalAPIToken: deps.InternalAPIToken, nativeCallbackURL: deps.NativeCallbackURL,
		imageBuilder: deps.ImageBuilder, computer: computerExecutor, readiness: readiness, identityFeatures: deps.IdentityFeatures,
		nativeGateway: deps.NativeGateway,
		cookieSecure:  publicURL != nil && publicURL.Scheme == "https", startedAt: time.Now(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/ready", s.ready)
	mux.HandleFunc("GET /api/v1/system", s.system)
	mux.HandleFunc("POST /api/v1/setup/admin", s.createInitialAdmin)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("GET /api/v1/auth/oidc/start", s.oidcStart)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", s.oidcCallback)
	mux.HandleFunc("POST /api/v1/auth/native/login", s.nativeLogin)
	mux.HandleFunc("POST /api/v1/auth/native/exchange", s.nativeExchange)
	mux.HandleFunc("POST /api/v1/auth/native/logout", s.requireNativeAuth(s.nativeLogout))
	mux.HandleFunc("POST /api/v1/native/desktops/{vmid}/actions/{action}", s.requireNativeAuth(s.changeNativeDesktopPower))
	mux.HandleFunc("POST /api/v1/native/desktops/{vmid}/connections", s.requireNativeAuth(s.createNativeDesktopConnection))
	mux.HandleFunc("DELETE /api/v1/native/connections/{id}", s.requireNativeAuth(s.releaseNativeDesktopConnection))
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireAuth(s.logout))
	mux.HandleFunc("GET /api/v1/me", s.requireAuth(s.me))
	mux.HandleFunc("PUT /api/v1/me/password", s.requireAuth(s.changeOwnPassword))
	mux.HandleFunc("GET /api/v1/api-tokens", s.requireAuth(s.apiTokens))
	mux.HandleFunc("POST /api/v1/api-tokens", s.requireAuth(s.createAPIToken))
	mux.HandleFunc("DELETE /api/v1/api-tokens/{id}", s.requireAuth(s.deleteAPIToken))
	mux.HandleFunc("GET /api/v1/infrastructure", s.requireAuth(s.infrastructure))
	mux.HandleFunc("GET /api/v1/platform-config", s.requireAuth(s.platformConfig))
	mux.HandleFunc("GET /api/v1/access-control", s.requireCookieOrAPIToken(s.accessControl))
	mux.HandleFunc("POST /api/v1/access-control/reconcile", s.requireAuth(s.reconcileAccessControl))
	mux.HandleFunc("POST /api/v1/users", s.requireAuth(s.createLocalUser))
	mux.HandleFunc("PATCH /api/v1/users/{id}", s.requireAuth(s.updateUser))
	mux.HandleFunc("PUT /api/v1/users/{id}/password", s.requireAuth(s.resetUserPassword))
	mux.HandleFunc("POST /api/v1/agents", s.requireAuth(s.createAgentPrincipal))
	mux.HandleFunc("PATCH /api/v1/agents/{id}", s.requireAuth(s.updateAgentPrincipal))
	mux.HandleFunc("POST /api/v1/agents/{id}/token", s.requireAuth(s.rotateAgentToken))
	mux.HandleFunc("PUT /api/v1/desktop-assignments/{subject_type}/{subject_id}/{vmid}", s.requireCookieOrAPIToken(s.putDesktopAssignment))
	mux.HandleFunc("DELETE /api/v1/desktop-assignments/{subject_type}/{subject_id}/{vmid}", s.requireCookieOrAPIToken(s.deleteDesktopAssignment))
	mux.HandleFunc("PUT /api/v1/managed-desktops/{vmid}/identity", s.requireAuth(s.updateManagedDesktopIdentity))
	mux.HandleFunc("POST /api/v1/managed-desktops/{vmid}/identity/reconcile", s.requireAuth(s.reconcileManagedDesktopIdentity))
	mux.HandleFunc("GET /api/v1/managed-desktops/{vmid}/native-recovery", s.requireAuth(s.inspectNativeRecovery))
	mux.HandleFunc("POST /api/v1/managed-desktops/{vmid}/native-recovery/{user_id}", s.requireAuth(s.recoverNativeAccount))
	mux.HandleFunc("PUT /api/v1/identity-profiles/{id}", s.requireAuth(s.putIdentityProfile))
	mux.HandleFunc("POST /api/v1/identity-groups", s.requireAuth(s.createIdentityGroup))
	mux.HandleFunc("PATCH /api/v1/identity-groups/{id}", s.requireAuth(s.updateIdentityGroup))
	mux.HandleFunc("PUT /api/v1/identity-groups/{id}/members/{user_id}", s.requireAuth(s.putIdentityGroupMembership))
	mux.HandleFunc("DELETE /api/v1/identity-groups/{id}/members/{user_id}", s.requireAuth(s.deleteIdentityGroupMembership))
	mux.HandleFunc("PATCH /api/v1/image-profiles/{id}", s.requireAuth(s.updateImageProfile))
	mux.HandleFunc("POST /api/v1/image-profiles/{id}/builds", s.requireAuth(s.startImageBuild))
	mux.HandleFunc("PATCH /api/v1/gpu-profiles/{id}", s.requireAuth(s.updateGPUProfile))
	mux.HandleFunc("POST /api/v1/pci-resource-mappings", s.requireAuth(s.createPCIResourceMapping))
	mux.HandleFunc("PUT /api/v1/pci-resource-mappings/{id}", s.requireAuth(s.updatePCIResourceMapping))
	mux.HandleFunc("POST /api/v1/desktop-instances", s.requireAuth(s.createDesktopInstance))
	mux.HandleFunc("GET /api/v1/jobs", s.requireAuth(s.jobs))
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.requireAuth(s.job))
	mux.HandleFunc("GET /api/v1/jobs/{id}/clone-inspection", s.requireAuth(s.inspectCloneTarget))
	mux.HandleFunc("GET /api/v1/jobs/{id}/clone-candidates", s.requireAuth(s.cloneTaskCandidates))
	mux.HandleFunc("POST /api/v1/jobs/{id}/clone-recovery", s.requireAuth(s.recoverCloneTask))
	mux.HandleFunc("GET /api/v1/audit-events", s.requireAuth(s.auditEvents))
	mux.HandleFunc("POST /api/v1/virtual-machines/{vmid}/actions/{action}", s.requireAuth(s.changeVirtualMachinePower))
	mux.HandleFunc("GET /api/v1/virtual-machines/{vmid}/access-policy", s.requireAuth(s.desktopAccessPolicy))
	mux.HandleFunc("PUT /api/v1/virtual-machines/{vmid}/access-policy", s.requireAuth(s.updateDesktopAccessPolicy))
	mux.HandleFunc("GET /api/v1/agent/desktops", s.requireService(s.agentDesktops))
	mux.HandleFunc("POST /api/v1/agent/authenticate", s.requireService(s.authenticateAgent))
	mux.HandleFunc("POST /api/v1/agent/desktop-leases", s.requireService(s.createAgentDesktopLease))
	mux.HandleFunc("GET /api/v1/agent/desktop-leases/{id}", s.requireService(s.agentDesktopLease))
	mux.HandleFunc("DELETE /api/v1/agent/desktop-leases/{id}", s.requireService(s.releaseAgentDesktopLease))
	mux.HandleFunc("POST /api/v1/agent/desktop-leases/{id}/actions/{action}", s.requireService(s.changeAgentDesktopPower))
	mux.HandleFunc("POST /api/v1/agent/desktop-leases/{id}/computer-actions", s.requireService(s.agentComputerAction))
	mux.HandleFunc("GET /api/v1/native/desktops", s.requireNativeAuth(s.nativeDesktops))
	return s.recover(s.requestLog(mux))
}

func (s *Server) requireService(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.internalAPIToken == "" {
			writeError(w, http.StatusServiceUnavailable, "agent_api_unavailable", "Agent API is not configured")
			return
		}
		provided, ok := auth.ParseBearerToken(r.Header.Get("Authorization"))
		if !ok || !auth.ConstantTimeEqual(provided, s.internalAPIToken) {
			writeError(w, http.StatusUnauthorized, "invalid_service_token", "Service authentication failed")
			return
		}
		next(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.readiness == nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.readiness.Ping(ctx); err != nil {
		s.logger.Warn("readiness check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	initialized := false
	if s.store != nil {
		var err error
		initialized, err = s.store.IsInitialized(r.Context())
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			s.logger.Error("read setup state", "error", err)
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":                "VC Workspace",
		"pve_configured":      s.pve != nil,
		"database_configured": s.store != nil,
		"initialized":         initialized,
		"oidc_configured":     s.oidc != nil,
		"oidc_name":           s.oidcName(),
		"uptime_seconds":      int(time.Since(s.startedAt).Seconds()),
		"identity_capabilities": map[string]any{
			"managed_local":     true,
			"linux_sssd":        true,
			"windows_ad":        true,
			"linux_sssd_oidc":   s.identityFeatures.SSSDOIDCEnabled,
			"windows_entra_rdp": s.identityFeatures.WindowsEntraEnabled,
		},
	})
}

func (s *Server) oidcName() string {
	if s.oidc == nil {
		return ""
	}
	return s.oidc.Name()
}

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{2,63}$`)

func (s *Server) createInitialAdmin(w http.ResponseWriter, r *http.Request) {
	if s.store == nil || s.setupToken == "" {
		writeError(w, http.StatusServiceUnavailable, "setup_unavailable", "Setup is not configured")
		return
	}
	var request struct {
		SetupToken  string `json:"setup_token"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !auth.ConstantTimeEqual(request.SetupToken, s.setupToken) {
		s.auditRequest(r, "", "setup.admin_creation_failed", "platform", "initial_setup", map[string]any{"reason": "invalid_setup_token"})
		writeError(w, http.StatusUnauthorized, "invalid_setup_token", "Setup token is invalid")
		return
	}
	request.Username = strings.TrimSpace(request.Username)
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	if !usernamePattern.MatchString(request.Username) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_username", "Username must be 3–64 characters using letters, numbers, dot, dash, or underscore")
		return
	}
	if len(request.Password) < 12 || len(request.Password) > 128 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_password", "Password must be 12–128 characters")
		return
	}
	if request.DisplayName == "" {
		request.DisplayName = request.Username
	}
	passwordHash, err := auth.HashPassword(request.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to secure password")
		return
	}
	userID, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create user")
		return
	}
	user := store.User{ID: userID, Username: request.Username, DisplayName: request.DisplayName, PasswordHash: passwordHash, Role: "platform_admin"}
	if err := s.store.CreateInitialAdmin(r.Context(), user); err != nil {
		if errors.Is(err, store.ErrAlreadyInitialized) {
			s.auditRequest(r, "", "setup.admin_creation_failed", "platform", "initial_setup", map[string]any{"username": user.Username, "reason": "already_initialized"})
			writeError(w, http.StatusConflict, "already_initialized", "VC Workspace is already initialized")
			return
		}
		s.logger.Error("create initial administrator", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create administrator")
		return
	}
	s.auditRequest(r, user.ID, "setup.admin_created", "user", user.ID, map[string]any{"username": user.Username})
	s.startSession(w, r, user)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	username := strings.TrimSpace(request.Username)
	user, err := s.store.UserByUsername(r.Context(), username)
	if err != nil || user.Disabled || user.PasswordHash == "" {
		actorID := ""
		reason := "unknown_user"
		if err == nil {
			actorID = user.ID
			if user.Disabled {
				reason = "disabled_user"
			} else {
				reason = "password_login_unavailable"
			}
		}
		targetID := actorID
		if targetID == "" {
			targetID = username
		}
		s.auditRequest(r, actorID, "auth.login_failed", "user", targetID, map[string]any{"username": username, "client": "web", "reason": reason})
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Username or password is incorrect")
		return
	}
	valid, err := auth.VerifyPassword(user.PasswordHash, request.Password)
	if err != nil {
		s.logger.Error("verify password hash", "user_id", user.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to verify credentials")
		return
	}
	if !valid {
		s.auditRequest(r, user.ID, "auth.login_failed", "user", user.ID, map[string]any{"username": username, "client": "web", "reason": "invalid_password"})
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Username or password is incorrect")
		return
	}
	if s.startSession(w, r, user) {
		s.auditRequest(r, user.ID, "auth.login_succeeded", "user", user.ID, map[string]any{"client": "web"})
	}
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil || s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "oidc_unavailable", "Single sign-on is not configured")
		return
	}
	state, err := auth.OpaqueToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to start single sign-on")
		return
	}
	nonce, err := auth.OpaqueToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to start single sign-on")
		return
	}
	pkceVerifier := oauth2.GenerateVerifier()
	clientKind := "web"
	if r.URL.Query().Get("client") == "macos" {
		clientKind = "macos"
	}
	const lifetime = 10 * time.Minute
	if err := s.store.CreateOIDCRequest(r.Context(), auth.TokenDigest(state), nonce, pkceVerifier, clientKind, time.Now().Add(lifetime)); err != nil {
		s.logger.Error("create OIDC authorization request", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to start single sign-on")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: workspaceOIDCStateCookie, Value: state, Path: "/api/v1/auth/oidc/callback", MaxAge: int(lifetime.Seconds()),
		HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, s.oidc.AuthorizationURL(state, nonce, pkceVerifier), http.StatusFound)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil || s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "oidc_unavailable", "Single sign-on is not configured")
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	stateCookie, err := requestCookie(r, workspaceOIDCStateCookie, legacyOIDCStateCookie)
	if err != nil || state == "" || code == "" || !auth.ConstantTimeEqual(stateCookie.Value, state) {
		s.auditRequest(r, "", "auth.oidc_login_failed", "", "", map[string]any{"stage": "callback_validation"})
		writeError(w, http.StatusBadRequest, "invalid_oidc_callback", "Single sign-on response is invalid or expired")
		return
	}
	clearCookie(w, workspaceOIDCStateCookie, "/api/v1/auth/oidc/callback", s.cookieSecure)
	clearCookie(w, legacyOIDCStateCookie, "/api/v1/auth/oidc/callback", s.cookieSecure)
	request, err := s.store.ConsumeOIDCRequest(r.Context(), auth.TokenDigest(state))
	if err != nil {
		s.auditRequest(r, "", "auth.oidc_login_failed", "", "", map[string]any{"stage": "state_consumption"})
		writeError(w, http.StatusBadRequest, "invalid_oidc_callback", "Single sign-on response is invalid or expired")
		return
	}
	claims, err := s.oidc.Exchange(r.Context(), code, request.PKCEVerifier, request.Nonce)
	if err != nil {
		s.logger.Warn("OIDC callback rejected", "error", err)
		s.auditRequest(r, "", "auth.oidc_login_failed", "", "", map[string]any{"stage": "token_exchange"})
		writeError(w, http.StatusUnauthorized, "oidc_login_failed", "Single sign-on could not be completed")
		return
	}
	userID, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create user")
		return
	}
	displayName := strings.TrimSpace(claims.Name)
	if displayName == "" {
		displayName = strings.TrimSpace(claims.Email)
	}
	if displayName == "" {
		displayName = "SSO User"
	}
	user, err := s.store.ResolveOIDCUser(r.Context(), oidcauth.ProviderID, claims.Subject, store.User{
		ID: userID, Username: oidcUsername(claims.Email, claims.Subject), DisplayName: displayName,
	})
	if err != nil || user.Disabled {
		s.logger.Error("resolve OIDC user", "error", err)
		s.auditRequest(r, user.ID, "auth.oidc_login_failed", "user", user.ID, map[string]any{"stage": "identity_resolution"})
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to sign in user")
		return
	}
	groups := make([]store.IdentityGroup, 0, len(claims.Groups))
	for _, externalID := range claims.Groups {
		groups = append(groups, store.IdentityGroup{
			ID: oidcGroupID(oidcauth.ProviderID, externalID), ProviderID: oidcauth.ProviderID,
			ExternalID: externalID, DisplayName: externalID, Source: "oidc", Enabled: true,
		})
	}
	if err := s.store.SyncOIDCGroupMemberships(r.Context(), oidcauth.ProviderID, user.ID, groups); err != nil {
		s.logger.Error("synchronize OIDC group memberships", "user_id", user.ID, "error", err)
		s.auditRequest(r, user.ID, "auth.oidc_login_failed", "user", user.ID, map[string]any{"stage": "group_synchronization"})
		writeError(w, http.StatusInternalServerError, "identity_sync_failed", "Single sign-on group membership could not be synchronized")
		return
	}
	s.auditRequest(r, user.ID, "auth.oidc_login_succeeded", "user", user.ID, map[string]any{"provider": oidcauth.ProviderID, "client": request.ClientKind})
	if request.ClientKind == "macos" {
		s.finishNativeOIDC(w, r, user)
		return
	}
	if _, ok := s.issueSession(w, r, user); !ok {
		return
	}
	http.Redirect(w, r, s.publicURL, http.StatusSeeOther)
}

func oidcGroupID(providerID, externalID string) string {
	digest := sha256.Sum256([]byte(providerID + "\x00" + externalID))
	return fmt.Sprintf("oidcg_%x", digest[:12])
}

func (s *Server) finishNativeOIDC(w http.ResponseWriter, r *http.Request, user store.User) {
	callback, err := url.Parse(s.nativeCallbackURL)
	if err != nil || callback.Scheme == "" {
		writeError(w, http.StatusInternalServerError, "native_callback_invalid", "Native client callback is not configured")
		return
	}
	code, err := auth.OpaqueToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to complete native sign-in")
		return
	}
	if err := s.store.CreateNativeAuthCode(r.Context(), auth.TokenDigest(code), user.ID, time.Now().Add(2*time.Minute)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to complete native sign-in")
		return
	}
	query := callback.Query()
	query.Set("code", code)
	callback.RawQuery = query.Encode()
	http.Redirect(w, r, callback.String(), http.StatusSeeOther)
}

func (s *Server) nativeLogin(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	username := strings.TrimSpace(request.Username)
	user, err := s.store.UserByUsername(r.Context(), username)
	if err != nil || user.Disabled || user.PasswordHash == "" {
		actorID := ""
		reason := "unknown_user"
		if err == nil {
			actorID = user.ID
			if user.Disabled {
				reason = "disabled_user"
			} else {
				reason = "password_login_unavailable"
			}
		}
		targetID := actorID
		if targetID == "" {
			targetID = username
		}
		s.auditRequest(r, actorID, "auth.native_login_failed", "user", targetID, map[string]any{"username": username, "client": "macos", "reason": reason})
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Username or password is incorrect")
		return
	}
	valid, err := auth.VerifyPassword(user.PasswordHash, request.Password)
	if err != nil || !valid {
		s.auditRequest(r, user.ID, "auth.native_login_failed", "user", user.ID, map[string]any{"username": username, "client": "macos", "reason": "invalid_password"})
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Username or password is incorrect")
		return
	}
	if s.issueNativeSession(w, r, user) {
		s.auditRequest(r, user.ID, "auth.native_login_succeeded", "user", user.ID, map[string]any{"client": "macos"})
	}
}

func (s *Server) nativeExchange(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
		return
	}
	var request struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &request); err != nil || request.Code == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Native authorization code is required")
		return
	}
	user, err := s.store.ConsumeNativeAuthCode(r.Context(), auth.TokenDigest(request.Code))
	if err != nil || user.Disabled {
		s.auditRequest(r, user.ID, "auth.native_oidc_login_failed", "user", user.ID, map[string]any{"client": "macos"})
		writeError(w, http.StatusUnauthorized, "invalid_native_code", "Native authorization code is invalid or expired")
		return
	}
	if s.issueNativeSession(w, r, user) {
		s.auditRequest(r, user.ID, "auth.native_oidc_login_succeeded", "user", user.ID, map[string]any{"client": "macos"})
	}
}

func (s *Server) issueNativeSession(w http.ResponseWriter, r *http.Request, user store.User) bool {
	token, err := auth.OpaqueToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create native session")
		return false
	}
	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	if err := s.store.CreateNativeSessionForVerifiedUser(r.Context(), auth.TokenDigest(token), user, expiresAt); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "authentication_changed", "Authentication changed; sign in again")
			return false
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create native session")
		return false
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"access_token": token, "token_type": "Bearer", "expires_at": expiresAt, "user": publicUser(user),
	})
	return true
}

type nativeHandler func(http.ResponseWriter, *http.Request, store.User, string)

func (s *Server) requireNativeAuth(next nativeHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.store == nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
			return
		}
		token, ok := auth.ParseBearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
			return
		}
		user, err := s.store.NativeSessionByToken(r.Context(), auth.TokenDigest(token))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
			return
		}
		next(w, r, user, token)
	}
}

func (s *Server) nativeLogout(w http.ResponseWriter, r *http.Request, user store.User, token string) {
	if err := s.store.RevokeNativeSession(r.Context(), auth.TokenDigest(token)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to end native session")
		return
	}
	// Guest work is persisted in the same transaction as token invalidation.
	// Do not re-select and revoke active connections here: another device may
	// already have drained the queue and issued a new, authorized connection.
	s.auditRequest(r, user.ID, "auth.native_logout_succeeded", "user", user.ID, map[string]any{"client": "macos", "guest_revocation": "queued"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) releaseNativeDesktopConnection(w http.ResponseWriter, r *http.Request, user store.User, _ string) {
	connection, err := s.store.BeginRevokeDesktopConnection(r.Context(), strings.TrimSpace(r.PathValue("id")), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "desktop_connection_not_found", "Desktop connection was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to close desktop connection")
		return
	}
	if connection.State == "revoked" || connection.State == "expired" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.releaseDesktopConnection(r.Context(), connection); err != nil {
		s.logger.Warn("revoke desktop connection credential", "connection_id", connection.ID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "desktop_connection_revoke_pending", "Desktop connection is closing and credential revocation will be retried")
		return
	}
	s.auditRequest(r, user.ID, "native.desktop_connection_revoked", "desktop_connection", connection.ID, map[string]any{"vmid": connection.DesktopVMID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) nativeDesktops(w http.ResponseWriter, r *http.Request, user store.User, _ string) {
	if s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return
	}
	summary, err := s.desktopInventory(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to read desktop inventory")
		return
	}
	vmids, err := s.store.UserAssignedDesktopVMIDs(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read desktop assignments")
		return
	}
	desktops := filterDesktopsByVMID(summary.VMs, vmids)
	writeJSON(w, http.StatusOK, map[string]any{"desktops": desktops})
}

func (s *Server) changeNativeDesktopPower(w http.ResponseWriter, r *http.Request, user store.User, _ string) {
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	action := r.PathValue("action")
	if err != nil || vmid <= 0 || (action != "start" && action != "stop") {
		writeError(w, http.StatusBadRequest, "invalid_power_request", "Power request is invalid")
		return
	}
	if !s.requireUserDesktopAccess(w, r, user, vmid) {
		return
	}
	idempotencyKey, fingerprint, proceed := s.prepareJobRequest(w, r, "user:"+user.ID)
	if !proceed {
		return
	}
	machine, err := s.managedVirtualMachine(r.Context(), vmid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		} else {
			writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to validate PVE resources")
		}
		return
	}
	if (action == "start" && machine.Status == "running") || (action == "stop" && machine.Status == "stopped") {
		writeError(w, http.StatusConflict, "power_state_conflict", "Virtual machine is already in the requested power state")
		return
	}
	requestJSON, _ := json.Marshal(map[string]any{"vmid": vmid, "action": action, "node": machine.Node, "client": "native"})
	jobToken, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create job")
		return
	}
	job, created, err := s.store.CreateJob(r.Context(), store.Job{
		ID: "job_" + jobToken, IdempotencyKey: idempotencyKey, RequestFingerprint: fingerprint, Operation: "native.desktop_" + action, State: "accepted",
		TargetVMID: vmid, TargetNode: machine.Node, TaskNode: machine.Node, Request: requestJSON, CreatedBy: user.ID,
	})
	if errors.Is(err, store.ErrConflict) {
		writeJobConflict(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create job")
		return
	}
	if !created {
		writeJSON(w, http.StatusAccepted, job)
		return
	}
	upid, err := s.pve.ChangePowerState(r.Context(), machine.Node, vmid, action)
	if err != nil {
		_ = s.store.UpdateJobTask(r.Context(), job.ID, "failed", "", err.Error())
		s.logger.Error("change native desktop power state", "job_id", job.ID, "vmid", vmid, "action", action, "error", err)
		writeError(w, http.StatusBadGateway, "pve_power_failed", "PVE rejected the power request")
		return
	}
	_ = s.store.UpdateJobTask(r.Context(), job.ID, "running", upid, "")
	s.auditRequest(r, user.ID, "native.desktop_"+action+"_requested", "job", job.ID, map[string]any{"vmid": vmid})
	job.State, job.UPID = "running", upid
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) createNativeDesktopConnection(w http.ResponseWriter, r *http.Request, user store.User, token string) {
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	if err != nil || vmid <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_desktop", "Desktop identifier is invalid")
		return
	}
	if !s.requireUserDesktopAccess(w, r, user, vmid) {
		return
	}
	if s.nativeGateway != nil && (len(r.Header.Values("X-VC-Workspace-Transport")) != 1 ||
		r.Header.Get("X-VC-Workspace-Transport") != gateway.TunnelSubprotocol) {
		writeError(w, http.StatusUpgradeRequired, "gateway_client_required", "Update VC Workspace to connect through the configured Gateway")
		return
	}
	machine, err := s.managedVirtualMachine(r.Context(), vmid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		} else {
			writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to validate PVE resources")
		}
		return
	}
	if machine.Status != "running" {
		writeError(w, http.StatusConflict, "desktop_not_running", "Desktop must be running before a connection can be created")
		return
	}
	interfaces, err := s.pve.GuestNetworkInterfaces(r.Context(), machine.Node, machine.VMID)
	if err != nil {
		writeError(w, http.StatusConflict, "desktop_not_ready", "Desktop services are still starting")
		return
	}
	host := desktopIPv4(interfaces)
	if host == "" {
		writeError(w, http.StatusConflict, "desktop_network_unavailable", "Desktop has no reachable IPv4 address")
		return
	}
	if s.nativeGateway != nil {
		target, err := netip.ParseAddr(host)
		if err != nil || !s.nativeGateway.Allows(target) {
			writeError(w, http.StatusConflict, "desktop_gateway_unavailable", "Desktop is outside the configured Gateway target ranges")
			return
		}
	}
	if err := readDesktopReady(r.Context(), s.pve, machine.Node, machine.VMID); err != nil {
		writeError(w, http.StatusConflict, "desktop_not_ready", "Desktop services are still starting")
		return
	}
	var connection store.DesktopConnectionSession
	var password string
	var revokedLeases []store.DesktopLease
	var takeoverErr error
	var appliedPolicy store.DesktopAccessPolicy
	var gatewayConnection *nativeGatewayConnection
	err = s.store.WithDesktopConnectionLock(r.Context(), machine.VMID, func() error {
		if _, err := s.store.NativeSessionByToken(r.Context(), auth.TokenDigest(token)); err != nil {
			return err
		}
		allowed, err := s.store.UserCanAccessDesktop(r.Context(), user.ID, machine.VMID)
		if err != nil {
			return err
		}
		if !allowed {
			return store.ErrNotFound
		}
		desktop, err := s.store.ManagedDesktopByVMID(r.Context(), machine.VMID)
		if err != nil {
			return err
		}
		if desktop.OSFamily != "linux" && desktop.OSFamily != "windows" {
			return computer.ErrUnavailable
		}
		// Stable Windows credentials and its trusted discovery adapter are not
		// yet integrated. Enabling Gateway routing must not reopen that path.
		if s.nativeGateway != nil && desktop.OSFamily != "linux" {
			return computer.ErrUnavailable
		}
		if desktop.OSFamily == "linux" {
			lifecycle, ok := s.computer.(computer.NativeAccountLifecycle)
			if !ok {
				return computer.ErrUnavailable
			}
			// Probe before retiring another connection. An unupgraded Guest
			// never falls back to the old username-only credential commands.
			if err := lifecycle.CheckNativeAccountSupport(r.Context(), machine); err != nil {
				return err
			}
		}
		if err := s.recoverNativeAccountsForDesktop(r.Context(), machine.VMID); err != nil {
			return err
		}
		if err := s.revokeGuestIdentitiesForDesktop(r.Context(), machine.VMID); err != nil {
			return err
		}
		previous, err := s.store.BeginRevokeDesktopConnections(r.Context(), machine.VMID)
		if err != nil {
			return err
		}
		for _, existing := range previous {
			if err := s.revokeDesktopConnectionCredential(r.Context(), existing); err != nil {
				return fmt.Errorf("revoke previous desktop connection %s: %w", existing.ID, err)
			}
		}
		binding, err := s.ensureManagedGuestIdentity(r.Context(), machine, user)
		if err != nil {
			return err
		}
		var nativeAccount store.NativeGuestAccount
		if desktop.OSFamily == "linux" {
			nativeAccount, err = s.bindNativeGuestAccount(r.Context(), machine, user, auth.TokenDigest(token))
			if err != nil {
				return err
			}
		} else if _, err := s.store.NativeGuestAccount(r.Context(), machine.VMID, user.ID); !errors.Is(err, store.ErrNotFound) {
			// A pinned Windows account cannot return to legacy writes while
			// its versioned OS adapter is still being implemented.
			return computer.ErrUnavailable
		}
		appliedPolicy, err = s.reconcileDesktopAccessPolicy(r.Context(), machine)
		if err != nil {
			return fmt.Errorf("apply desktop access policy: %w", err)
		}
		var certificate [32]byte
		if s.nativeGateway != nil {
			// Policies can restart xrdp: observe its actual certificate after
			// reconciliation but before issuing any new OS credential.
			certificate, err = guestdesktop.RDPCertificate(r.Context(), s.pve, machine, desktop.OSFamily)
			if err != nil {
				return err
			}
		}
		if err := s.applyRequestedDesktopScale(r.Context(), machine, binding.GuestUsername, r.Header.Get("X-VC-Workspace-Desktop-Scale")); err != nil {
			return fmt.Errorf("apply desktop scale: %w", err)
		}
		revokedLeases, err = s.store.RevokeDesktopLeases(r.Context(), machine.VMID)
		if err != nil {
			return err
		}
		takeoverErr = s.synchronizeComputerRevocation(r.Context(), machine, true)
		if takeoverErr != nil {
			return takeoverErr
		}
		if desktop.OSFamily == "linux" {
			var gatewayRequest *store.NativeGatewayTicketRequest
			if s.nativeGateway != nil {
				gatewayRequest = &store.NativeGatewayTicketRequest{GatewayID: s.nativeGateway.ID(),
					Target: netip.MustParseAddr(host), CertificateSHA256: certificate[:], PolicyRevision: appliedPolicy.AppliedRevision}
			}
			var ticket store.NativeGatewayTicket
			connection, password, ticket, err = s.issueNativeGuestConnection(r.Context(), machine, nativeAccount, auth.TokenDigest(token), gatewayRequest)
			if err == nil && gatewayRequest != nil {
				gatewayConnection = nativeGatewayDescriptor(s.nativeGateway, ticket, certificate)
			}
			return err
		}
		passwordToken, err := auth.OpaqueToken(18)
		if err != nil {
			return err
		}
		password = "Vcw1!" + passwordToken
		if err := s.pve.SetGuestUserPassword(r.Context(), machine.Node, machine.VMID, binding.GuestUsername, password); err != nil {
			return err
		}
		// A revoked account must not briefly accept its old password while
		// policy/Guest provisioning runs. Rotate first, then enable the account.
		if err := s.enableGuestAccount(r.Context(), machine.Node, machine.VMID, binding.GuestUsername); err != nil {
			return err
		}
		connectionToken, err := auth.OpaqueToken(18)
		if err != nil {
			return err
		}
		connection, err = s.store.CreateNativeDesktopConnectionSession(r.Context(), store.DesktopConnectionSession{
			ID: "conn_" + connectionToken, UserID: user.ID, DesktopVMID: machine.VMID,
			GuestUsername: binding.GuestUsername, ExpiresAt: time.Now().Add(8 * time.Hour),
		}, auth.TokenDigest(token))
		return err
	})
	for _, lease := range revokedLeases {
		s.auditRequest(r, user.ID, "agent.desktop_lease_revoked", "desktop_lease", lease.ID, map[string]any{
			"agent_id": lease.AgentID, "desktop_id": lease.DesktopID, "control_epoch": lease.ControlEpoch, "reason": "human_takeover",
			"authority_synchronized": takeoverErr == nil,
		})
	}
	if err != nil {
		s.logger.Warn("prepare native desktop connection", "vmid", machine.VMID, "user_id", user.ID, "error", err)
		s.auditRequest(r, user.ID, "native.desktop_connection_failed", "virtual_machine", strconv.Itoa(machine.VMID), map[string]any{"vmid": machine.VMID, "reason": "guest_identity_or_credential_unavailable"})
		writeError(w, http.StatusConflict, "desktop_identity_not_ready", "Desktop identity and permissions are still being prepared")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	sessionPolicy := nativeSessionPolicySnapshot(appliedPolicy)
	protocolName := "rdp"
	if gatewayConnection != nil {
		protocolName = "rdp-gateway"
	}
	s.auditRequest(r, user.ID, "native.desktop_connection_created", "desktop_connection", connection.ID, map[string]any{
		"vmid": machine.VMID, "protocol": protocolName, "host": host, "guest_username": connection.GuestUsername, "expires_at": connection.ExpiresAt,
		"policy_revision": sessionPolicy.Revision, "policy_hash": sessionPolicy.Hash,
	})
	descriptor := map[string]any{
		"id":             connection.ID,
		"protocol":       protocolName,
		"host":           host,
		"port":           3389,
		"username":       connection.GuestUsername,
		"password":       password,
		"issued_at":      connection.CreatedAt,
		"expires_at":     connection.ExpiresAt,
		"desktop_id":     strconv.Itoa(machine.VMID),
		"session_policy": sessionPolicy,
	}
	if gatewayConnection != nil {
		// A distinct protocol prevents old clients that ignore new JSON fields
		// from interpreting this as permission to open direct TCP/3389.
		descriptor["gateway"] = gatewayConnection
	}
	writeJSON(w, http.StatusCreated, descriptor)
}

func nativeSessionPolicySnapshot(policy store.DesktopAccessPolicy) nativeSessionPolicy {
	snapshot := nativeSessionPolicy{
		Version: 1, Revision: policy.AppliedRevision,
		ClipboardRedirection: policy.ClipboardRedirection,
		DriveRedirection:     policy.DriveRedirection,
		ManagedBackground:    policy.ManagedBackground,
	}
	canonical, _ := json.Marshal(struct {
		Version              int   `json:"version"`
		Revision             int64 `json:"revision"`
		ClipboardRedirection bool  `json:"clipboard_redirection"`
		DriveRedirection     bool  `json:"drive_redirection"`
		ManagedBackground    bool  `json:"managed_background"`
	}{snapshot.Version, snapshot.Revision, snapshot.ClipboardRedirection, snapshot.DriveRedirection, snapshot.ManagedBackground})
	digest := sha256.Sum256(canonical)
	snapshot.Hash = fmt.Sprintf("sha256:%x", digest)
	return snapshot
}

var guestUsernamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,19}$`)

func managedGuestUsername(userID string) string {
	return store.NativeGuestUsername(userID)
}

func (s *Server) ensureManagedGuestIdentity(ctx context.Context, machine pve.VM, user store.User) (store.GuestIdentityBinding, error) {
	desktop, err := s.store.ManagedDesktopByVMID(ctx, machine.VMID)
	if err != nil {
		return store.GuestIdentityBinding{}, err
	}
	configuration, err := s.pve.VMConfiguration(ctx, machine.Node, machine.VMID)
	if err != nil {
		return store.GuestIdentityBinding{}, err
	}
	osFamily := desktopOSFamily(configuration.OSType)
	if osFamily != desktop.OSFamily {
		return store.GuestIdentityBinding{}, computer.ErrUnavailable
	}
	profileID := desktop.IdentityProfileID
	if profileID == "" {
		profileID = "managed-local-" + osFamily
	}
	profile, err := s.store.IdentityProfileByID(ctx, profileID)
	if err != nil {
		return store.GuestIdentityBinding{}, fmt.Errorf("read desktop identity profile: %w", err)
	}
	if !profile.Enabled || profile.Mode != "managed_local" || profile.Platform != osFamily {
		return store.GuestIdentityBinding{}, errors.New("native brokered connection currently requires an enabled managed-local identity profile for the guest operating system")
	}
	username := managedGuestUsername(user.ID)
	binding, err := s.store.PutGuestIdentityBinding(ctx, store.GuestIdentityBinding{
		DesktopVMID: machine.VMID, UserID: user.ID, ProfileID: profile.ID,
		GuestUsername: username, State: "provisioning",
	})
	if err != nil {
		return store.GuestIdentityBinding{}, err
	}
	if osFamily == "linux" {
		lifecycle, ok := s.computer.(computer.NativeAccountLifecycle)
		if !ok {
			return store.GuestIdentityBinding{}, computer.ErrUnavailable
		}
		if _, err := lifecycle.PrepareNativeAccount(ctx, machine, username); err != nil {
			return store.GuestIdentityBinding{}, err
		}
		binding.State, binding.LastError = "ready", ""
		return s.store.PutGuestIdentityBinding(ctx, binding)
	}
	command, err := ensureGuestUserCommand(osFamily, username)
	if err != nil {
		return store.GuestIdentityBinding{}, err
	}
	execContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result, err := s.pve.ExecGuest(execContext, machine.Node, machine.VMID, command)
	if err != nil {
		binding.State, binding.LastError = "failed", "Guest Agent could not provision the account"
		_, _ = s.store.PutGuestIdentityBinding(ctx, binding)
		return store.GuestIdentityBinding{}, err
	}
	if result.ExitCode != 0 {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Stdout)
		}
		if len(detail) > 300 {
			detail = detail[:300]
		}
		binding.State, binding.LastError = "failed", "Guest account provisioning failed"
		_, _ = s.store.PutGuestIdentityBinding(ctx, binding)
		return store.GuestIdentityBinding{}, fmt.Errorf("guest account command exited with %d: %s", result.ExitCode, detail)
	}
	binding.State, binding.LastError = "ready", ""
	return s.store.PutGuestIdentityBinding(ctx, binding)
}

func ensureGuestUserCommand(osFamily, username string) ([]string, error) {
	if !guestUsernamePattern.MatchString(username) {
		return nil, errors.New("guest username is invalid")
	}
	switch osFamily {
	case "linux":
		script := fmt.Sprintf(`set -eu
username=%s
if ! getent passwd "$username" >/dev/null; then
  useradd --create-home --shell /bin/bash "$username"
fi
if getent group ssl-cert >/dev/null; then
  usermod -a -G ssl-cert "$username"
fi
# Render nodes expose unprivileged GPU submission, not display-master or sudo.
# Do not change device modes or grant the broader video group.
if [ -c /dev/dri/renderD128 ] && getent group render >/dev/null; then
  usermod -a -G render "$username"
fi
install -d -m 0700 -o "$username" -g "$username" "/home/$username/.config"
printf 'startxfce4\n' >"/home/$username/.xsession"
chown "$username:$username" "/home/$username/.xsession"
getent passwd "$username" >/dev/null`, username)
		return []string{"/bin/sh", "-c", script}, nil
	case "windows":
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $name='%s'; $user=Get-LocalUser -Name $name -ErrorAction SilentlyContinue; if (-not $user) { $empty=ConvertTo-SecureString ([Guid]::NewGuid().ToString()+'aA1!') -AsPlainText -Force; New-LocalUser -Name $name -Password $empty -AccountNeverExpires -PasswordNeverExpires -Disabled | Out-Null; $user=Get-LocalUser -Name $name }; $rdp=(Get-LocalGroup -SID 'S-1-5-32-555').Name; if (-not (Get-LocalGroupMember -Group $rdp | Where-Object { $_.SID -eq $user.SID })) { Add-LocalGroupMember -Group $rdp -Member $name }`, username)
		return []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}, nil
	default:
		return nil, errors.New("desktop operating system is not supported")
	}
}

func (s *Server) revokeDesktopConnectionCredential(ctx context.Context, connection store.DesktopConnectionSession) error {
	// The caller holds the per-desktop lock. A queued snapshot can predate a
	// successful revocation and a new connection; never act on that snapshot.
	current, err := s.store.DesktopConnectionByID(ctx, connection.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.State != "revoking" {
		return nil
	}
	connection = current
	desktop, err := s.store.ManagedDesktopByVMID(ctx, connection.DesktopVMID)
	if err != nil {
		return err
	}
	terminate, err := s.store.DesktopConnectionTerminationRequired(ctx, connection.ID)
	if err != nil {
		return err
	}
	account, err := s.store.NativeGuestAccount(ctx, connection.DesktopVMID, connection.UserID)
	if err == nil {
		return s.retireNativeGuestConnection(ctx, pve.VM{Node: desktop.Node, VMID: desktop.VMID}, account, connection, terminate)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if terminate {
		if err := s.revokeGuestAccount(ctx, desktop, connection.GuestUsername); err != nil {
			return err
		}
	} else {
		// A normal disconnect preserves the OS session for reconnect, but its
		// issued password must no longer authenticate a new RDP connection.
		passwordToken, err := auth.OpaqueToken(24)
		if err != nil {
			return err
		}
		if err := s.pve.SetGuestUserPassword(ctx, desktop.Node, desktop.VMID, connection.GuestUsername, "Vcw1!"+passwordToken); err != nil {
			return err
		}
	}
	state := "revoked"
	if !connection.ExpiresAt.After(time.Now()) {
		state = "expired"
	}
	return s.store.MarkDesktopConnectionClosed(ctx, connection.ID, state)
}

func (s *Server) releaseDesktopConnection(ctx context.Context, connection store.DesktopConnectionSession) error {
	return s.store.WithDesktopConnectionLock(ctx, connection.DesktopVMID, func() error {
		return s.revokeDesktopConnectionCredential(ctx, connection)
	})
}

// RunBackgroundMaintenance retries credential revocation after expiry, logout,
// node recovery, or a transient Guest Agent failure. It is safe to run on each
// control-plane replica because work is serialized per desktop in PostgreSQL.
func (s *Server) RunBackgroundMaintenance(ctx context.Context) {
	if s.store == nil || s.pve == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.recoverDueNativeAccounts(ctx)
		s.revokeDueComputerAuthorities(ctx)
		s.revokeDueGuestIdentities(ctx)
		s.revokeDueDesktopConnections(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) revokeDueDesktopConnections(ctx context.Context) {
	connections, err := s.store.DesktopConnectionsDueForRevocation(ctx, 100)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.logger.Warn("claim expired desktop connections", "error", err)
		}
		return
	}
	for _, connection := range connections {
		if err := s.releaseDesktopConnection(ctx, connection); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Warn("revoke expired desktop credential", "connection_id", connection.ID, "vmid", connection.DesktopVMID, "error", err)
		}
	}
}

func (s *Server) applyRequestedDesktopScale(ctx context.Context, machine pve.VM, username, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	scale, err := strconv.Atoi(raw)
	if err != nil || (scale != 100 && scale != 200) {
		return errors.New("desktop scale must be 100 or 200")
	}
	configuration, err := s.pve.VMConfiguration(ctx, machine.Node, machine.VMID)
	if err != nil {
		return err
	}
	if configuration.OSType != "l26" {
		return nil
	}
	if !guestUsernamePattern.MatchString(username) {
		return errors.New("guest username is invalid")
	}
	dpi := scale * 96 / 100
	script := "set -eu\n" + guestdesktop.InstallScript() + fmt.Sprintf(`
username=%s
scale_tmp=$(mktemp /var/lib/vc-workspace/display-scale/.scale.XXXXXX)
printf '%%s\n' '%d' >"$scale_tmp"
chmod 0644 "$scale_tmp"
mv -f "$scale_tmp" "/var/lib/vc-workspace/display-scale/$username"
session_pid=$(pgrep -u "$username" -x xfce4-session | head -n1 || true)
if [ -n "$session_pid" ]; then
  display=$(tr '\0' '\n' < "/proc/$session_pid/environ" | sed -n 's/^DISPLAY=//p' | head -n1)
  dbus=$(tr '\0' '\n' < "/proc/$session_pid/environ" | sed -n 's/^DBUS_SESSION_BUS_ADDRESS=//p' | head -n1)
  runuser -u "$username" -- env DISPLAY="$display" DBUS_SESSION_BUS_ADDRESS="$dbus" xfconf-query -c xsettings -p /Xft/DPI -n -t int -s %d
else
  runuser -u "$username" -- dbus-run-session -- xfconf-query -c xsettings -p /Xft/DPI -n -t int -s %d
fi`, username, scale, dpi, dpi)
	result, err := s.pve.ExecGuest(ctx, machine.Node, machine.VMID, []string{"/bin/sh", "-c", script})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("guest display scale command exited with %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

var desktopReadyPaths = []string{
	"/var/lib/vc-workspace/desktop-ready",
	"/var/lib/vc-vdi/desktop-ready",
	`C:\ProgramData\VC Workspace\Agent\desktop-ready`,
	`C:\ProgramData\VC VDI\Agent\desktop-ready`,
}

type guestFileReader interface {
	ReadGuestFile(context.Context, string, int, string, int) (string, error)
}

func readDesktopReady(ctx context.Context, reader guestFileReader, node string, vmid int) error {
	var lastErr error
	for _, path := range desktopReadyPaths {
		content, err := reader.ReadGuestFile(ctx, node, vmid, path, 64)
		if err == nil && strings.TrimSpace(content) == "ready" {
			return nil
		}
		if err == nil {
			err = errors.New("desktop readiness marker is invalid")
		}
		lastErr = err
	}
	return lastErr
}

func desktopIPv4(interfaces []pve.GuestNetworkInterface) string {
	fallback := ""
	for _, guestInterface := range interfaces {
		for _, item := range guestInterface.IPAddresses {
			address, err := netip.ParseAddr(item.Address)
			if err != nil || !address.Is4() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() {
				continue
			}
			if address.IsPrivate() {
				return address.String()
			}
			if fallback == "" {
				fallback = address.String()
			}
		}
	}
	return fallback
}

var oidcUsernameCharacters = regexp.MustCompile(`[^a-z0-9._-]+`)

func oidcUsername(email, subject string) string {
	base := strings.ToLower(strings.TrimSpace(email))
	if at := strings.IndexByte(base, '@'); at >= 0 {
		base = base[:at]
	}
	base = strings.Trim(oidcUsernameCharacters.ReplaceAllString(base, "-"), ".-_")
	if len(base) < 3 {
		base = "sso-user"
	}
	if len(base) > 52 {
		base = base[:52]
	}
	if base == "sso-user" && subject != "" {
		suffix := strings.ToLower(oidcUsernameCharacters.ReplaceAllString(subject, ""))
		if len(suffix) > 8 {
			suffix = suffix[:8]
		}
		if suffix != "" {
			base += "-" + suffix
		}
	}
	return base
}

type authenticatedHandler func(http.ResponseWriter, *http.Request, store.Session, string)

func (s *Server) requireAuth(next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.store == nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
			return
		}
		cookie, err := requestCookie(r, workspaceSessionCookieName, legacySessionCookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
			return
		}
		session, err := s.store.SessionByToken(r.Context(), auth.TokenDigest(cookie.Value))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
			return
		}
		next(w, r, session, cookie.Value)
	}
}

// requireCookieOrAPIToken is deliberately used only on routes exposed to the
// IaC provider. A vcwi_ credential must not silently expand to every Web admin
// endpoint merely because those handlers share the authenticatedHandler type.
func (s *Server) requireCookieOrAPIToken(next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.store == nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
			return
		}
		if provided, ok := auth.ParseBearerToken(r.Header.Get("Authorization")); ok {
			if !strings.HasPrefix(provided, "vcwi_") || len(provided) > 512 {
				writeError(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
				return
			}
			session, err := s.store.SessionByAPIToken(r.Context(), auth.TokenDigest(provided))
			if err != nil {
				writeError(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
				return
			}
			next(w, r, session, provided)
			return
		}
		s.requireAuth(next)(w, r)
	}
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user store.User) bool {
	csrf, ok := s.issueSession(w, r, user)
	if !ok {
		return false
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": publicUser(user), "csrf_token": csrf})
	return true
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, user store.User) (string, bool) {
	token, err := auth.OpaqueToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create session")
		return "", false
	}
	csrf, err := auth.OpaqueToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create session")
		return "", false
	}
	expiresAt := time.Now().Add(12 * time.Hour)
	if err := s.store.CreateSessionForVerifiedUser(r.Context(), auth.TokenDigest(token), user, csrf, expiresAt); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "authentication_changed", "Authentication changed; sign in again")
			return "", false
		}
		s.logger.Error("create web session", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create session")
		return "", false
	}
	http.SetCookie(w, &http.Cookie{Name: workspaceSessionCookieName, Value: token, Path: "/", Expires: expiresAt, MaxAge: int((12 * time.Hour).Seconds()), HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode})
	return csrf, true
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request, session store.Session, _ string) {
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(session.User), "csrf_token": session.CSRFToken})
}

func (s *Server) apiTokens(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	tokens, err := s.store.APITokens(r.Context(), session.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read API tokens")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_tokens": tokens})
}

func (s *Server) createAPIToken(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireWebAdminMutation(w, r, session) {
		return
	}
	var request struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expires_in_days"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len([]rune(request.Name)) > 80 || request.ExpiresInDays < 1 || request.ExpiresInDays > 90 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_api_token", "Token name and an expiry from 1–90 days are required")
		return
	}
	id, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create API token")
		return
	}
	secret, err := auth.OpaqueToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create API token")
		return
	}
	rawToken := "vcwi_" + secret
	token, err := s.store.CreateAPIToken(r.Context(), session.User.ID, store.APIToken{
		ID: "token_" + id, Name: request.Name, ExpiresAt: time.Now().Add(time.Duration(request.ExpiresInDays) * 24 * time.Hour),
	}, auth.TokenDigest(rawToken))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create API token")
		return
	}
	s.auditRequest(r, session.User.ID, "api_token.created", "api_token", token.ID, map[string]any{"name": token.Name, "expires_at": token.ExpiresAt})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"api_token": token, "access_token": rawToken})
}

func (s *Server) deleteAPIToken(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireWebAdminMutation(w, r, session) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	deleted, err := s.store.DeleteAPIToken(r.Context(), session.User.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to revoke API token")
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "api_token_not_found", "API token was not found")
		return
	}
	s.auditRequest(r, session.User.ID, "api_token.revoked", "api_token", id, map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request, session store.Session, token string) {
	if !s.requireWebMutation(w, r, session) {
		return
	}
	if session.User.PasswordHash == "" {
		writeError(w, http.StatusConflict, "password_managed_externally", "Password is managed by the identity provider")
		return
	}
	var request struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateNewPassword(request.NewPassword); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_password", err.Error())
		return
	}
	valid, err := auth.VerifyPassword(session.User.PasswordHash, request.CurrentPassword)
	if err != nil {
		s.logger.Error("verify current password", "user_id", session.User.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to verify current password")
		return
	}
	if !valid {
		s.auditRequest(r, session.User.ID, "user.password_change_failed", "user", session.User.ID, map[string]any{"reason": "invalid_current_password"})
		writeError(w, http.StatusUnprocessableEntity, "invalid_current_password", "Current password is incorrect")
		return
	}
	unchanged, err := auth.VerifyPassword(session.User.PasswordHash, request.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to verify new password")
		return
	}
	if unchanged {
		writeError(w, http.StatusUnprocessableEntity, "password_unchanged", "New password must be different from the current password")
		return
	}
	hash, err := auth.HashPassword(request.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to secure new password")
		return
	}
	if _, err := s.store.SetUserPassword(r.Context(), session.User.ID, hash, auth.TokenDigest(token)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to change password")
		return
	}
	s.auditRequest(r, session.User.ID, "user.password_changed", "user", session.User.ID, map[string]any{"other_sessions_revoked": true})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, session store.Session, token string) {
	if !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf_token", "CSRF token is invalid")
		return
	}
	if err := s.store.DeleteSession(r.Context(), auth.TokenDigest(token)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to end session")
		return
	}
	s.auditRequest(r, session.User.ID, "auth.logout_succeeded", "user", session.User.ID, map[string]any{"client": "web"})
	clearCookie(w, workspaceSessionCookieName, "/", s.cookieSecure)
	clearCookie(w, legacySessionCookieName, "/", s.cookieSecure)
	w.WriteHeader(http.StatusNoContent)
}

func requestCookie(r *http.Request, primary, legacy string) (*http.Cookie, error) {
	if cookie, err := r.Cookie(primary); err == nil && cookie.Value != "" {
		return cookie, nil
	}
	return r.Cookie(legacy)
}

func clearCookie(w http.ResponseWriter, name, path string, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

func publicUser(user store.User) map[string]any {
	identityKind := "oidc"
	if user.PasswordHash != "" {
		identityKind = "local"
	}
	return map[string]any{"id": user.ID, "username": user.Username, "display_name": user.DisplayName, "role": user.Role, "identity_kind": identityKind}
}

func (s *Server) infrastructure(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return
	}
	summary, err := s.desktopInventory(r.Context())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.logger.Error("read PVE infrastructure", "error", err)
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to read PVE infrastructure")
		return
	}
	if session.User.Role != "platform_admin" {
		vmids, err := s.store.UserAssignedDesktopVMIDs(r.Context(), session.User.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read desktop assignments")
			return
		}
		summary.VMs = filterDesktopsByVMID(summary.VMs, vmids)
		summary.Nodes = []pve.Node{}
		summary.Storage = []pve.Storage{}
		summary.Writable = false
	}
	writeJSON(w, http.StatusOK, summary)
}

func filterDesktopsByVMID(machines []pve.VM, vmids []int) []pve.VM {
	allowed := make(map[int]struct{}, len(vmids))
	for _, vmid := range vmids {
		allowed[vmid] = struct{}{}
	}
	desktops := make([]pve.VM, 0, len(vmids))
	for _, machine := range machines {
		if _, ok := allowed[machine.VMID]; ok && machine.Managed && isManagedDesktop(machine) {
			desktops = append(desktops, machine)
		}
	}
	return desktops
}

func (s *Server) accessControl(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	s.writeAccessControl(w, r, session.User.ID)
}

func (s *Server) writeAccessControl(w http.ResponseWriter, r *http.Request, userID string) {
	users, err := s.store.Users(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read users")
		return
	}
	agents, err := s.store.AgentPrincipals(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read AI agents")
		return
	}
	desktops, err := s.store.ManagedDesktops(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read desktop registry")
		return
	}
	assignments, err := s.store.DesktopAssignments(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read desktop assignments")
		return
	}
	activeLeases, err := s.store.ActiveDesktopLeases(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read active desktop control")
		return
	}
	groups, err := s.store.IdentityGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read identity groups")
		return
	}
	memberships, err := s.store.IdentityGroupMemberships(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read group memberships")
		return
	}
	profiles, err := s.store.IdentityProfiles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read identity profiles")
		return
	}
	bindings, err := s.store.GuestIdentityBindings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read guest identity bindings")
		return
	}
	apiTokens, err := s.store.APITokens(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read API tokens")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users": users, "agents": agents, "desktops": desktops, "assignments": assignments, "desktop_leases": activeLeases,
		"groups": groups, "group_memberships": memberships, "identity_profiles": profiles, "guest_identity_bindings": bindings, "api_tokens": apiTokens,
	})
}

func (s *Server) reconcileAccessControl(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	if s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return
	}
	summary, err := s.desktopInventory(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to reconcile desktop inventory")
		return
	}
	desktops := make([]store.ManagedDesktop, 0)
	for _, machine := range summary.VMs {
		if machine.Managed {
			osFamily := "unknown"
			configuration, configurationErr := s.pve.VMConfiguration(r.Context(), machine.Node, machine.VMID)
			if configurationErr != nil {
				s.logger.Warn("read managed desktop OS family", "vmid", machine.VMID, "node", machine.Node, "error", configurationErr)
			} else {
				osFamily = desktopOSFamily(configuration.OSType)
			}
			desktops = append(desktops, store.ManagedDesktop{VMID: machine.VMID, DisplayName: machine.Name, Node: machine.Node, OSFamily: osFamily, Present: true, Enabled: true})
		}
	}
	if err := s.store.ReconcileManagedDesktops(r.Context(), desktops); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to save desktop inventory")
		return
	}
	s.auditRequest(r, session.User.ID, "desktop_registry.reconciled", "desktop_registry", "pve", map[string]any{"desktop_count": len(desktops)})
	s.writeAccessControl(w, r, session.User.ID)
}

func (s *Server) createLocalUser(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	var request struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.Username = strings.TrimSpace(request.Username)
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	if !usernamePattern.MatchString(request.Username) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_username", "Username must be 3–64 characters using letters, numbers, dot, dash, or underscore")
		return
	}
	if len(request.Password) < 12 || len(request.Password) > 128 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_password", "Password must be 12–128 characters")
		return
	}
	if request.DisplayName == "" {
		request.DisplayName = request.Username
	}
	hash, err := auth.HashPassword(request.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to secure password")
		return
	}
	id, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create user")
		return
	}
	user, err := s.store.CreateLocalUser(r.Context(), store.User{ID: id, Username: request.Username, DisplayName: request.DisplayName, PasswordHash: hash})
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "username_exists", "Username already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create user")
		return
	}
	s.auditRequest(r, session.User.ID, "user.created", "user", user.ID, map[string]any{"username": user.Username, "identity_kind": user.IdentityKind})
	writeJSON(w, http.StatusCreated, user)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	var request struct {
		Disabled *bool `json:"disabled"`
	}
	if err := decodeJSON(r, &request); err != nil || request.Disabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "disabled is required")
		return
	}
	userID := strings.TrimSpace(r.PathValue("id"))
	if userID == session.User.ID && *request.Disabled {
		writeError(w, http.StatusConflict, "cannot_disable_self", "The current administrator cannot disable their own account")
		return
	}
	user, err := s.store.SetUserDisabled(r.Context(), userID, *request.Disabled)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user_not_found", "User was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to update user")
		return
	}
	s.auditRequest(r, session.User.ID, "user.updated", "user", user.ID, map[string]any{"disabled": user.Disabled})
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireWebAdminMutation(w, r, session) {
		return
	}
	userID := strings.TrimSpace(r.PathValue("id"))
	if userID == session.User.ID {
		writeError(w, http.StatusConflict, "use_self_service_password_change", "Use the account password form to change your own password")
		return
	}
	var request struct {
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateNewPassword(request.NewPassword); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_password", err.Error())
		return
	}
	hash, err := auth.HashPassword(request.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to secure new password")
		return
	}
	user, err := s.store.SetUserPassword(r.Context(), userID, hash, nil)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user_not_found", "User was not found")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "password_managed_externally", "Password is managed by the identity provider")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to reset password")
		return
	}
	s.auditRequest(r, session.User.ID, "user.password_reset", "user", user.ID, map[string]any{"username": user.Username, "sessions_revoked": true})
	w.WriteHeader(http.StatusNoContent)
}

func validateNewPassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < 12 || length > 128 {
		return errors.New("Password must be 12–128 characters")
	}
	return nil
}

func (s *Server) createAgentPrincipal(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	var request struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.ID = strings.TrimSpace(request.ID)
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	if !agentIDPattern.MatchString(request.ID) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_agent_id", "Agent ID must be 3–128 characters using letters, numbers, dot, dash, underscore, or colon")
		return
	}
	if request.DisplayName == "" || len([]rune(request.DisplayName)) > 128 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_display_name", "Display name is required and must not exceed 128 characters")
		return
	}
	token, err := newAgentAccessToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create agent credentials")
		return
	}
	agent, err := s.store.CreateAgentPrincipal(r.Context(), store.AgentPrincipal{ID: request.ID, DisplayName: request.DisplayName, CreatedBy: session.User.ID}, auth.TokenDigest(token))
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "agent_exists", "Agent ID already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create AI agent")
		return
	}
	s.auditRequest(r, session.User.ID, "agent.created", "agent", agent.ID, map[string]any{"display_name": agent.DisplayName})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"agent": agent, "access_token": token})
}

func (s *Server) updateAgentPrincipal(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &request); err != nil || request.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "enabled is required")
		return
	}
	agent, err := s.store.SetAgentEnabled(r.Context(), r.PathValue("id"), *request.Enabled)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "agent_not_found", "AI agent was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to update AI agent")
		return
	}
	s.auditRequest(r, session.User.ID, "agent.updated", "agent", agent.ID, map[string]any{"enabled": agent.Enabled})
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) rotateAgentToken(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	token, err := newAgentAccessToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to rotate agent credentials")
		return
	}
	agent, err := s.store.RotateAgentToken(r.Context(), r.PathValue("id"), auth.TokenDigest(token))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "agent_not_found", "AI agent was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to rotate agent credentials")
		return
	}
	s.auditRequest(r, session.User.ID, "agent.token_rotated", "agent", agent.ID, map[string]any{})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"agent": agent, "access_token": token})
}

func newAgentAccessToken() (string, error) {
	token, err := auth.OpaqueToken(32)
	if err != nil {
		return "", err
	}
	return "vcwa_" + token, nil
}

func (s *Server) putDesktopAssignment(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	s.changeDesktopAssignment(w, r, session, true)
}

func (s *Server) deleteDesktopAssignment(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	s.changeDesktopAssignment(w, r, session, false)
}

func (s *Server) changeDesktopAssignment(w http.ResponseWriter, r *http.Request, session store.Session, assign bool) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	subjectType := r.PathValue("subject_type")
	subjectID := strings.TrimSpace(r.PathValue("subject_id"))
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	if (subjectType != "user" && subjectType != "agent" && subjectType != "group") || subjectID == "" || vmid <= 0 || err != nil {
		writeError(w, http.StatusBadRequest, "invalid_assignment", "Desktop assignment is invalid")
		return
	}
	assignment := store.DesktopAssignment{SubjectType: subjectType, SubjectID: subjectID, DesktopVMID: vmid, CreatedBy: session.User.ID}
	if assign {
		created, err := s.store.PutDesktopAssignment(r.Context(), assignment)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "assignment_target_not_found", "Desktop or assignment subject was not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "desktop_assignment_conflict", "Personal desktops can have only one user owner; group access requires a shared desktop")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to assign desktop")
			return
		}
		if created {
			s.auditRequest(r, session.User.ID, "desktop_assignment.created", subjectType, subjectID, map[string]any{"desktop_vmid": vmid})
		}
		writeJSON(w, http.StatusOK, assignment)
		return
	}
	deleted, err := s.store.DeleteDesktopAssignment(r.Context(), subjectType, subjectID, vmid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to remove desktop assignment")
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "assignment_not_found", "Desktop assignment was not found")
		return
	}
	s.auditRequest(r, session.User.ID, "desktop_assignment.deleted", subjectType, subjectID, map[string]any{"desktop_vmid": vmid})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateManagedDesktopIdentity(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	var request managedDesktopIdentityRequest
	if err != nil || vmid <= 0 || decodeJSON(r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_desktop_identity", "Desktop identity configuration is invalid")
		return
	}
	request.AccessMode = strings.TrimSpace(request.AccessMode)
	request.OwnerUserID = strings.TrimSpace(request.OwnerUserID)
	request.IdentityProfileID = strings.TrimSpace(request.IdentityProfileID)
	if request.AccessMode != "personal" && request.AccessMode != "shared" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_access_mode", "Access mode must be personal or shared")
		return
	}
	if request.AccessMode == "personal" && request.OwnerUserID == "" {
		writeError(w, http.StatusUnprocessableEntity, "desktop_owner_required", "A personal desktop requires exactly one owner")
		return
	}
	desktop, desktopErr := s.store.ManagedDesktopByVMID(r.Context(), vmid)
	if errors.Is(desktopErr, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "desktop_identity_target_not_found", "Desktop was not found")
		return
	}
	if desktopErr != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read desktop identity")
		return
	}
	if request.IdentityProfileID != "" {
		profile, profileErr := s.store.IdentityProfileByID(r.Context(), request.IdentityProfileID)
		if errors.Is(profileErr, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "identity_profile_not_found", "Identity profile was not found")
			return
		}
		if profileErr != nil || !profile.Enabled {
			writeError(w, http.StatusConflict, "identity_profile_unavailable", "Identity profile is not enabled")
			return
		}
		osFamily := desktop.OSFamily
		if osFamily == "unknown" && s.pve != nil && desktop.Node != "" {
			if configuration, configurationErr := s.pve.VMConfiguration(r.Context(), desktop.Node, vmid); configurationErr == nil {
				osFamily = desktopOSFamily(configuration.OSType)
			}
		}
		if osFamily == "unknown" {
			writeError(w, http.StatusConflict, "desktop_os_unknown", "Desktop operating system must be reconciled before selecting an identity profile")
			return
		}
		if osFamily != "unknown" && osFamily != profile.Platform {
			writeError(w, http.StatusConflict, "identity_profile_platform_mismatch", "Identity profile does not match the desktop operating system")
			return
		}
	}
	desktop, err = s.store.UpdateManagedDesktopIdentity(r.Context(), vmid, request.AccessMode, request.OwnerUserID, request.IdentityProfileID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "desktop_identity_target_not_found", "Desktop, owner, or identity profile was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to update desktop identity")
		return
	}
	s.auditRequest(r, session.User.ID, "desktop.identity_updated", "virtual_machine", strconv.Itoa(vmid), map[string]any{
		"access_mode": desktop.AccessMode, "owner_user_id": desktop.OwnerUserID, "identity_profile_id": desktop.IdentityProfileID,
	})
	writeJSON(w, http.StatusOK, desktop)
}

func (s *Server) reconcileManagedDesktopIdentity(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	if s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return
	}
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	var request desktopIdentityReconcileRequest
	if err != nil || vmid <= 0 || decodeJSON(r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_identity_reconciliation", "Desktop identity reconciliation request is invalid")
		return
	}
	desktop, err := s.store.ManagedDesktopByVMID(r.Context(), vmid)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		return
	}
	if err != nil || !desktop.Present || !desktop.Enabled || desktop.Node == "" {
		writeError(w, http.StatusConflict, "desktop_not_ready", "Desktop is not ready for identity reconciliation")
		return
	}
	osFamily := desktop.OSFamily
	if osFamily == "unknown" {
		configuration, configurationErr := s.pve.VMConfiguration(r.Context(), desktop.Node, desktop.VMID)
		if configurationErr != nil {
			writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to read desktop operating system")
			return
		}
		osFamily = desktopOSFamily(configuration.OSType)
	}
	if osFamily == "unknown" {
		writeError(w, http.StatusConflict, "desktop_os_unknown", "Desktop operating system could not be identified")
		return
	}
	profileID := desktop.IdentityProfileID
	if profileID == "" {
		profileID = "managed-local-" + osFamily
	}
	profile, err := s.store.IdentityProfileByID(r.Context(), profileID)
	if err != nil || !profile.Enabled {
		writeError(w, http.StatusConflict, "identity_profile_unavailable", "Identity profile is not enabled")
		return
	}
	if profile.Platform != osFamily {
		writeError(w, http.StatusConflict, "identity_profile_platform_mismatch", "Identity profile does not match the desktop operating system")
		return
	}
	if profile.Mode == "linux_sssd_oidc" && !s.identityFeatures.SSSDOIDCEnabled {
		writeError(w, http.StatusConflict, "experimental_capability_disabled", "SSSD OIDC must be enabled explicitly after validating the selected provider")
		return
	}
	if profile.Mode == "windows_entra" && !s.identityFeatures.WindowsEntraEnabled {
		writeError(w, http.StatusConflict, "experimental_capability_disabled", "Windows Entra RDP must be enabled explicitly after rebuilding the client with AAD support")
		return
	}
	plan, err := buildGuestIdentityReconcilePlan(profile, request)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "identity_reconciliation_requirements_not_met", err.Error())
		return
	}
	execContext, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	var result pve.GuestExecResult
	if len(plan.input) > 0 {
		result, err = s.pve.ExecGuestWithInput(execContext, desktop.Node, desktop.VMID, plan.command, plan.input)
	} else {
		result, err = s.pve.ExecGuest(execContext, desktop.Node, desktop.VMID, plan.command)
	}
	if err == nil && result.ExitCode != 0 {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Stdout)
		}
		err = fmt.Errorf("guest identity command exited with %d: %s", result.ExitCode, detail)
	}
	if err != nil {
		_, _ = s.store.MarkManagedDesktopIdentityFailed(r.Context(), vmid, "Guest Agent could not apply the identity profile")
		s.logger.Warn("reconcile desktop identity", "vmid", vmid, "profile_id", profile.ID, "error", err)
		s.auditRequest(r, session.User.ID, "desktop.identity_reconcile_failed", "virtual_machine", strconv.Itoa(vmid), map[string]any{"profile_id": profile.ID, "mode": profile.Mode})
		writeError(w, http.StatusConflict, "identity_reconciliation_failed", "Guest operating system could not apply the identity profile")
		return
	}
	restartRequired := plan.restartRequired || (plan.restartMarker != "" && strings.Contains(result.Stdout, plan.restartMarker))
	if restartRequired {
		desktop, err = s.store.MarkManagedDesktopIdentityRestartRequired(r.Context(), vmid, profile.ID)
	} else {
		desktop, err = s.store.MarkManagedDesktopIdentityApplied(r.Context(), vmid, profile.ID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to record identity reconciliation")
		return
	}
	s.auditRequest(r, session.User.ID, "desktop.identity_reconciled", "virtual_machine", strconv.Itoa(vmid), map[string]any{"profile_id": profile.ID, "mode": profile.Mode, "restart_required": restartRequired})
	writeJSON(w, http.StatusOK, desktop)
}

type guestIdentityReconcilePlan struct {
	command         []string
	input           []byte
	restartRequired bool
	restartMarker   string
}

var directoryNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
var directoryPrincipalPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@\\-]{0,127}$`)
var directoryGroupListPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9 ._@\\,-]{0,510}[A-Za-z0-9])?$`)
var tenantIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func buildGuestIdentityReconcilePlan(profile store.IdentityProfile, request desktopIdentityReconcileRequest) (guestIdentityReconcilePlan, error) {
	config := make(map[string]any)
	if err := json.Unmarshal(profile.Config, &config); err != nil {
		return guestIdentityReconcilePlan{}, errors.New("identity profile config is invalid")
	}
	value := func(key string) string {
		item, _ := config[key].(string)
		return strings.TrimSpace(item)
	}
	linuxInstall := func(contents string, before string) guestIdentityReconcilePlan {
		encoded := base64.StdEncoding.EncodeToString([]byte(contents))
		script := "set -eu\n" + before + "\nprintf '%s' '" + encoded + "' | base64 -d >/etc/sssd/sssd.conf\nchmod 0600 /etc/sssd/sssd.conf\nchown root:root /etc/sssd/sssd.conf\nDEBIAN_FRONTEND=noninteractive pam-auth-update --package --enable mkhomedir >/dev/null\nsystemctl enable sssd oddjobd\nsystemctl restart sssd\nsystemctl start oddjobd\nsssctl config-check"
		return guestIdentityReconcilePlan{command: []string{"/bin/sh", "-c", script}}
	}
	switch profile.Mode {
	case "managed_local":
		if profile.Platform == "linux" {
			return guestIdentityReconcilePlan{command: []string{"/bin/sh", "-c", `set -eu; test -f /usr/share/vc-workspace/identity-capabilities.json; command -v useradd >/dev/null; command -v xrdp >/dev/null`}}, nil
		}
		return guestIdentityReconcilePlan{command: []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", `$ErrorActionPreference='Stop'; if (-not (Test-Path 'C:\ProgramData\VC Workspace\identity-capabilities.json')) { throw 'identity capabilities are missing' }; Get-Service TermService | Out-Null`}}, nil
	case "linux_sssd_ad":
		domain, realm := value("domain"), value("realm")
		if !directoryNamePattern.MatchString(domain) || !directoryNamePattern.MatchString(realm) || !directoryPrincipalPattern.MatchString(request.JoinUsername) || request.JoinPassword == "" {
			return guestIdentityReconcilePlan{}, errors.New("AD reconciliation requires a valid domain, realm, join username, and one-time join password")
		}
		allowed := value("allowed_groups")
		if !directoryGroupListPattern.MatchString(allowed) {
			return guestIdentityReconcilePlan{}, errors.New("AD identity profile requires allowed_groups to prevent unrestricted guest login")
		}
		sssd := fmt.Sprintf("[sssd]\nservices = nss, pam\ndomains = %s\n\n[domain/%s]\nid_provider = ad\nauth_provider = ad\naccess_provider = simple\nsimple_allow_groups = %s\ncache_credentials = true\ndefault_shell = /bin/bash\nfallback_homedir = /home/%%d/%%u\nuse_fully_qualified_names = true\nad_domain = %s\nkrb5_realm = %s\n", domain, domain, allowed, domain, strings.ToUpper(realm))
		plan := linuxInstall(sssd, fmt.Sprintf(`test -f /usr/share/vc-workspace/identity-capabilities.json; password=$(cat); if ! realm list --name-only | grep -Fxiq '%s'; then printf '%%s\n' "$password" | realm join --user='%s' '%s'; fi`, domain, request.JoinUsername, domain))
		plan.input = []byte(request.JoinPassword)
		return plan, nil
	case "linux_sssd_freeipa":
		domain, server, realm := value("domain"), value("server"), value("realm")
		if !directoryNamePattern.MatchString(domain) || !directoryNamePattern.MatchString(server) || !directoryNamePattern.MatchString(realm) || !directoryPrincipalPattern.MatchString(request.JoinUsername) || request.JoinPassword == "" {
			return guestIdentityReconcilePlan{}, errors.New("FreeIPA reconciliation requires a valid domain, server, realm, join username, and one-time join password")
		}
		script := fmt.Sprintf(`set -eu
test -f /usr/share/vc-workspace/identity-capabilities.json
password=$(cat)
if [ ! -f /etc/ipa/default.conf ]; then
  printf '%%s\n' "$password" | kinit '%s'
  ipa-client-install --unattended --mkhomedir --no-ntp --domain='%s' --realm='%s' --server='%s' --principal='%s'
  kdestroy
fi
systemctl enable sssd oddjobd
systemctl restart sssd
systemctl start oddjobd
sssctl config-check`, request.JoinUsername, domain, strings.ToUpper(realm), server, request.JoinUsername)
		return guestIdentityReconcilePlan{command: []string{"/bin/sh", "-c", script}, input: []byte(request.JoinPassword)}, nil
	case "linux_sssd_ldap":
		domain, uri, searchBase, accessFilter := value("domain"), value("uri"), value("search_base"), value("access_filter")
		parsedURI, err := url.Parse(uri)
		if !directoryNamePattern.MatchString(domain) || err != nil || (parsedURI.Scheme != "ldap" && parsedURI.Scheme != "ldaps") || parsedURI.Host == "" || searchBase == "" || accessFilter == "" {
			return guestIdentityReconcilePlan{}, errors.New("LDAP reconciliation requires domain, LDAP(S) URI, search base, and access filter")
		}
		startTLS := "false"
		if parsedURI.Scheme == "ldap" {
			startTLS = "true"
		}
		sssd := fmt.Sprintf("[sssd]\nservices = nss, pam\ndomains = %s\n\n[domain/%s]\nid_provider = ldap\nauth_provider = ldap\naccess_provider = ldap\nldap_uri = %s\nldap_search_base = %s\nldap_access_filter = %s\nldap_id_use_start_tls = %s\nldap_tls_cacert = /etc/ssl/certs/ca-certificates.crt\ncache_credentials = true\ndefault_shell = /bin/bash\nfallback_homedir = /home/%%d/%%u\n", domain, domain, uri, searchBase, accessFilter, startTLS)
		return linuxInstall(sssd, `test -f /usr/share/vc-workspace/identity-capabilities.json`), nil
	case "linux_sssd_oidc":
		domain, idpType, clientID := value("domain"), value("idp_type"), value("client_id")
		tokenEndpoint, userinfoEndpoint, deviceEndpoint := value("token_endpoint"), value("userinfo_endpoint"), value("device_auth_endpoint")
		if !directoryNamePattern.MatchString(domain) || (idpType != "entra_id" && !strings.HasPrefix(idpType, "keycloak:")) || clientID == "" || request.ClientSecret == "" {
			return guestIdentityReconcilePlan{}, errors.New("SSSD OIDC reconciliation requires a supported idp_type, domain, client ID, and one-time client secret")
		}
		for _, endpoint := range []string{tokenEndpoint, userinfoEndpoint, deviceEndpoint} {
			parsed, err := url.Parse(endpoint)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
				return guestIdentityReconcilePlan{}, errors.New("SSSD OIDC endpoints must be absolute HTTPS URLs")
			}
		}
		sssd := fmt.Sprintf("[sssd]\nservices = nss, pam\ndomains = %s\n\n[domain/%s]\nid_provider = idp\nauth_provider = idp\nidp_type = %s\nidp_client_id = %s\nidp_client_secret = %s\nidp_token_endpoint = %s\nidp_userinfo_endpoint = %s\nidp_device_auth_endpoint = %s\nidp_id_scope = %s\nidp_auth_scope = openid profile email\ndefault_shell = /bin/bash\nfallback_homedir = /home/%%f\n", domain, domain, idpType, clientID, request.ClientSecret, tokenEndpoint, userinfoEndpoint, deviceEndpoint, value("id_scope"))
		script := `set -eu
test -f /usr/share/vc-workspace/identity-capabilities.json
test -x /usr/libexec/sssd/oidc_child -o -x /usr/lib/sssd/oidc_child
cat >/etc/sssd/sssd.conf
chmod 0600 /etc/sssd/sssd.conf
chown root:root /etc/sssd/sssd.conf
pam-auth-update --package --enable mkhomedir >/dev/null
systemctl enable sssd oddjobd
systemctl restart sssd
systemctl start oddjobd
sssctl config-check`
		return guestIdentityReconcilePlan{command: []string{"/bin/sh", "-c", script}, input: []byte(sssd)}, nil
	case "windows_ad":
		domain, allowedGroup, joinMethod := value("domain"), value("allowed_group"), value("join_method")
		if joinMethod != "online" || !directoryNamePattern.MatchString(domain) || !directoryPrincipalPattern.MatchString(request.JoinUsername) || request.JoinPassword == "" || allowedGroup == "" || strings.ContainsAny(allowedGroup, "\r\n';") {
			return guestIdentityReconcilePlan{}, errors.New("Windows AD reconciliation requires domain, allowed group, join username, and one-time join password")
		}
		const restartMarker = "VC_WORKSPACE_RESTART_REQUIRED"
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; if (-not (Test-Path 'C:\ProgramData\VC Workspace\identity-capabilities.json')) { throw 'identity capabilities are missing' }; $computer=Get-CimInstance Win32_ComputerSystem; if (-not $computer.PartOfDomain) { $secret=[Console]::In.ReadToEnd().TrimEnd(); $secure=ConvertTo-SecureString $secret -AsPlainText -Force; $credential=[PSCredential]::new('%s',$secure); Add-Computer -DomainName '%s' -Credential $credential -Force; Write-Output '%s'; exit 0 }; if ($computer.Domain -ine '%s') { throw 'desktop is joined to a different domain' }; $rdp=(Get-LocalGroup -SID 'S-1-5-32-555').Name; Add-LocalGroupMember -Group $rdp -Member '%s' -ErrorAction SilentlyContinue`, request.JoinUsername, domain, restartMarker, domain, allowedGroup)
		return guestIdentityReconcilePlan{command: []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}, input: []byte(request.JoinPassword), restartMarker: restartMarker}, nil
	case "windows_entra":
		principal, hostname, tenantID := value("allowed_principal"), value("target_hostname"), value("tenant_id")
		if !directoryPrincipalPattern.MatchString(principal) || !directoryNamePattern.MatchString(hostname) || !tenantIDPattern.MatchString(tenantID) {
			return guestIdentityReconcilePlan{}, errors.New("Windows Entra reconciliation requires a target hostname and allowed Entra principal")
		}
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; if (-not (Test-Path 'C:\ProgramData\VC Workspace\identity-capabilities.json')) { throw 'identity capabilities are missing' }; $status=(& dsregcmd.exe /status | Out-String); if ($status -notmatch 'AzureAdJoined\s*:\s*YES') { throw 'device is not Microsoft Entra joined' }; if ($status -notmatch '(?im)^\s*TenantId\s*:\s*%s\s*$') { throw 'device tenant does not match the identity profile' }; if ($env:COMPUTERNAME -ine '%s') { throw 'desktop hostname does not match the Entra RDP target' }; $rdp=(Get-LocalGroup -SID 'S-1-5-32-555').Name; Add-LocalGroupMember -Group $rdp -Member 'AzureAD\%s' -ErrorAction SilentlyContinue`, tenantID, hostname, principal)
		return guestIdentityReconcilePlan{command: []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}}, nil
	default:
		return guestIdentityReconcilePlan{}, errors.New("identity profile mode is not supported")
	}
}

func (s *Server) putIdentityProfile(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var request identityProfileRequest
	if !agentIDPattern.MatchString(id) || decodeJSON(r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_identity_profile", "Identity profile is invalid")
		return
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.Platform = strings.TrimSpace(request.Platform)
	request.Mode = strings.TrimSpace(request.Mode)
	if request.DisplayName == "" || len([]rune(request.DisplayName)) > 128 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_display_name", "Display name is required and must not exceed 128 characters")
		return
	}
	experimentalMode := request.Mode == "linux_sssd_oidc" || request.Mode == "windows_entra"
	if err := validateIdentityProfileConfig(request.Platform, request.Mode, request.Config); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_identity_profile", err.Error())
		return
	}
	if experimentalMode && !request.Experimental {
		writeError(w, http.StatusUnprocessableEntity, "experimental_acknowledgement_required", "Experimental identity modes must be explicitly acknowledged")
		return
	}
	if existing, existingErr := s.store.IdentityProfileByID(r.Context(), id); existingErr == nil {
		if existing.Platform != request.Platform || existing.Mode != request.Mode {
			writeError(w, http.StatusConflict, "identity_profile_shape_immutable", "An existing identity profile cannot change platform or mode")
			return
		}
	} else if !errors.Is(existingErr, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to validate identity profile")
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	configJSON, err := json.Marshal(request.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_identity_profile", "Identity profile configuration is invalid")
		return
	}
	profile, err := s.store.PutIdentityProfile(r.Context(), store.IdentityProfile{
		ID: id, DisplayName: request.DisplayName, Platform: request.Platform, Mode: request.Mode,
		Enabled: enabled, Experimental: experimentalMode, Config: configJSON, CreatedBy: session.User.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to save identity profile")
		return
	}
	s.auditRequest(r, session.User.ID, "identity_profile.updated", "identity_profile", profile.ID, map[string]any{"platform": profile.Platform, "mode": profile.Mode, "enabled": profile.Enabled, "experimental": profile.Experimental})
	writeJSON(w, http.StatusOK, profile)
}

func validateIdentityProfileConfig(platform, mode string, config map[string]any) error {
	validPlatform := platform == "linux" || platform == "windows"
	validMode := map[string]string{
		"managed_local": "", "linux_sssd_ad": "linux", "linux_sssd_freeipa": "linux",
		"linux_sssd_ldap": "linux", "linux_sssd_oidc": "linux", "windows_ad": "windows", "windows_entra": "windows",
	}
	requiredPlatform, ok := validMode[mode]
	if !validPlatform || !ok || (requiredPlatform != "" && requiredPlatform != platform) {
		return errors.New("identity profile mode does not match its platform")
	}
	if containsInlineSecret(config) {
		return errors.New("identity profiles may contain secret references, but not inline passwords, tokens, or private keys")
	}
	if containsMultilineConfigValue(config) {
		return errors.New("identity profile string values must be single-line")
	}
	required := map[string][]string{
		"linux_sssd_ad":      {"domain", "realm", "allowed_groups"},
		"linux_sssd_freeipa": {"domain", "server", "realm"},
		"linux_sssd_ldap":    {"domain", "uri", "search_base", "access_filter"},
		"linux_sssd_oidc":    {"domain", "idp_type", "client_id", "token_endpoint", "userinfo_endpoint", "device_auth_endpoint", "id_scope"},
		"windows_ad":         {"domain", "join_method", "allowed_group"},
		"windows_entra":      {"tenant_id", "target_hostname", "allowed_principal"},
	}
	for _, key := range required[mode] {
		value, _ := config[key].(string)
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("identity profile config field %q is required", key)
		}
	}
	configString := func(key string) string {
		value, _ := config[key].(string)
		return strings.TrimSpace(value)
	}
	switch mode {
	case "linux_sssd_ad":
		if !directoryNamePattern.MatchString(configString("domain")) || !directoryNamePattern.MatchString(configString("realm")) || !directoryGroupListPattern.MatchString(configString("allowed_groups")) {
			return errors.New("AD domain, realm, or allowed groups are invalid")
		}
	case "linux_sssd_freeipa":
		if !directoryNamePattern.MatchString(configString("domain")) || !directoryNamePattern.MatchString(configString("server")) || !directoryNamePattern.MatchString(configString("realm")) {
			return errors.New("FreeIPA domain, server, or realm are invalid")
		}
	case "linux_sssd_ldap":
		uri, err := url.Parse(configString("uri"))
		if !directoryNamePattern.MatchString(configString("domain")) || err != nil || (uri.Scheme != "ldap" && uri.Scheme != "ldaps") || uri.Host == "" {
			return errors.New("LDAP domain and LDAP(S) URI are invalid")
		}
	case "linux_sssd_oidc":
		idpType := configString("idp_type")
		if !directoryNamePattern.MatchString(configString("domain")) || (idpType != "entra_id" && !strings.HasPrefix(idpType, "keycloak:")) {
			return errors.New("SSSD OIDC domain or provider type is invalid")
		}
		for _, key := range []string{"token_endpoint", "userinfo_endpoint", "device_auth_endpoint"} {
			endpoint, err := url.Parse(configString(key))
			if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
				return fmt.Errorf("identity profile config field %q must be an absolute HTTPS URL", key)
			}
		}
	case "windows_ad":
		if configString("join_method") != "online" || !directoryNamePattern.MatchString(configString("domain")) || strings.ContainsAny(configString("allowed_group"), "\r\n';") {
			return errors.New("Windows AD currently supports only a valid online join profile")
		}
	case "windows_entra":
		if !tenantIDPattern.MatchString(configString("tenant_id")) || !directoryNamePattern.MatchString(configString("target_hostname")) || !directoryPrincipalPattern.MatchString(configString("allowed_principal")) {
			return errors.New("Windows Entra tenant, target hostname, or principal are invalid")
		}
	}
	return nil
}

func containsMultilineConfigValue(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.ContainsAny(typed, "\r\n")
	case map[string]any:
		for _, child := range typed {
			if containsMultilineConfigValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsMultilineConfigValue(child) {
				return true
			}
		}
	}
	return false
}

func containsInlineSecret(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if normalized != "credential_reference" && normalized != "offline_join_blob_reference" &&
				(strings.Contains(normalized, "password") || strings.Contains(normalized, "secret") ||
					strings.Contains(normalized, "token") || strings.Contains(normalized, "private_key")) {
				return true
			}
			if containsInlineSecret(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsInlineSecret(child) {
				return true
			}
		}
	}
	return false
}

func (s *Server) createIdentityGroup(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	var request identityGroupRequest
	if decodeJSON(r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_identity_group", "Identity group is invalid")
		return
	}
	request.ID = strings.TrimSpace(request.ID)
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.Source = strings.TrimSpace(request.Source)
	if !agentIDPattern.MatchString(request.ID) || request.DisplayName == "" || len([]rune(request.DisplayName)) > 128 || (request.Source != "local" && request.Source != "scim") {
		writeError(w, http.StatusUnprocessableEntity, "invalid_identity_group", "Group ID, display name, and local or scim source are required")
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	group, err := s.store.PutIdentityGroup(r.Context(), store.IdentityGroup{ID: request.ID, DisplayName: request.DisplayName, Source: request.Source, Enabled: enabled, CreatedBy: session.User.ID})
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "identity_group_exists", "Identity group already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create identity group")
		return
	}
	s.auditRequest(r, session.User.ID, "identity_group.created", "identity_group", group.ID, map[string]any{"source": group.Source})
	writeJSON(w, http.StatusCreated, group)
}

func (s *Server) updateIdentityGroup(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if decodeJSON(r, &request) != nil || request.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_identity_group", "enabled is required")
		return
	}
	group, err := s.store.SetIdentityGroupEnabled(r.Context(), strings.TrimSpace(r.PathValue("id")), *request.Enabled)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "identity_group_not_found", "Identity group was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to update identity group")
		return
	}
	s.auditRequest(r, session.User.ID, "identity_group.updated", "identity_group", group.ID, map[string]any{"enabled": group.Enabled})
	writeJSON(w, http.StatusOK, group)
}

func (s *Server) putIdentityGroupMembership(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	groupID, userID := strings.TrimSpace(r.PathValue("id")), strings.TrimSpace(r.PathValue("user_id"))
	if groupID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "invalid_group_membership", "Group and user are required")
		return
	}
	membership, err := s.store.PutIdentityGroupMembership(r.Context(), store.IdentityGroupMembership{GroupID: groupID, UserID: userID, Source: "local"})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "group_membership_target_not_found", "Group or user was not found")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "oidc_group_membership_managed", "OIDC group membership is managed by the identity provider")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to add group member")
		return
	}
	s.auditRequest(r, session.User.ID, "identity_group.member_added", "identity_group", groupID, map[string]any{"user_id": userID})
	writeJSON(w, http.StatusOK, membership)
}

func (s *Server) deleteIdentityGroupMembership(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if !s.requireAdminMutation(w, r, session) {
		return
	}
	groupID, userID := strings.TrimSpace(r.PathValue("id")), strings.TrimSpace(r.PathValue("user_id"))
	deleted, err := s.store.DeleteIdentityGroupMembership(r.Context(), groupID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to remove group member")
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "group_membership_not_found", "Group membership was not found")
		return
	}
	s.auditRequest(r, session.User.ID, "identity_group.member_removed", "identity_group", groupID, map[string]any{"user_id": userID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requireAdminMutation(w http.ResponseWriter, r *http.Request, session store.Session) bool {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return false
	}
	if session.AuthKind != "api_token" && !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf_token", "CSRF token is invalid")
		return false
	}
	return true
}

func (s *Server) requireWebAdminMutation(w http.ResponseWriter, r *http.Request, session store.Session) bool {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return false
	}
	return s.requireWebMutation(w, r, session)
}

func (s *Server) requireWebMutation(w http.ResponseWriter, r *http.Request, session store.Session) bool {
	if session.AuthKind != "web" {
		writeError(w, http.StatusForbidden, "web_session_required", "A Web session is required")
		return false
	}
	if !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf_token", "CSRF token is invalid")
		return false
	}
	return true
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	jobs, err := s.store.Jobs(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read recent tasks")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		s.auditRequest(r, session.User.ID, "audit.read_denied", "audit_log", "", map[string]any{"role": session.User.Role})
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	query := store.AuditEventQuery{Limit: 30, Outcome: strings.TrimSpace(r.URL.Query().Get("outcome")), Search: strings.TrimSpace(r.URL.Query().Get("q"))}
	if len(query.Search) > 128 || (query.Outcome != "" && query.Outcome != "success" && query.Outcome != "failure") {
		writeError(w, http.StatusBadRequest, "invalid_audit_filter", "Audit filters are invalid")
		return
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100")
			return
		}
		query.Limit = limit
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Audit cursor is invalid")
			return
		}
		query.Before = cursor
	}
	page, err := s.store.AuditEvents(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read audit events")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) platformConfig(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	images, err := s.store.ImageProfiles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read image profiles")
		return
	}
	gpuProfiles, err := s.store.GPUProfiles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read GPU profiles")
		return
	}
	imageBuilds, err := s.store.JobsByOperation(r.Context(), "image.build", 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read image build jobs")
		return
	}
	devices := []pve.GPUDevice{}
	mappings := []pve.PCIResourceMapping{}
	inventoryAvailable := false
	mappingInventoryAvailable := false
	if s.pve != nil {
		summary, summaryErr := s.pve.Summary(r.Context())
		if summaryErr == nil {
			nodes := make([]string, 0, len(summary.Nodes))
			for _, node := range summary.Nodes {
				if node.Status == "online" {
					nodes = append(nodes, node.Name)
				}
			}
			if inventory, inventoryErr := s.pve.GPUDevices(r.Context(), nodes); inventoryErr == nil {
				devices = inventory
				inventoryAvailable = true
			} else {
				s.logger.Warn("read PVE GPU inventory", "error", inventoryErr)
			}
		} else {
			s.logger.Warn("read PVE nodes for GPU inventory", "error", summaryErr)
		}
		if inventory, inventoryErr := s.pve.PCIResourceMappings(r.Context()); inventoryErr == nil {
			mappings = inventory
			mappingInventoryAvailable = true
		} else {
			s.logger.Warn("read PVE PCI resource mappings", "error", inventoryErr)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"image_profiles":                  images,
		"gpu_profiles":                    gpuProfiles,
		"gpu_devices":                     devices,
		"pci_resource_mappings":           mappings,
		"gpu_inventory_available":         inventoryAvailable,
		"gpu_mapping_inventory_available": mappingInventoryAvailable,
		"image_builder_available":         s.imageBuilder != nil && s.imageBuilder.Available(),
		"image_builds":                    imageBuilds,
	})
}

func (s *Server) updateImageProfile(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	if !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf_token", "CSRF token is invalid")
		return
	}
	profile, err := s.store.ImageProfileByID(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "image_profile_not_found", "Image profile was not found")
		} else {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read image profile")
		}
		return
	}
	var request imageProfileUpdateRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	applyImageProfileUpdate(&profile, request)
	if err := validateImageProfile(profile); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_image_profile", err.Error())
		return
	}
	validateTemplate := request.BuildStatus != nil && profile.BuildStatus == "ready"
	validateTemplate = validateTemplate || request.Enabled != nil && profile.Enabled && profile.BuildStatus == "ready"
	validateTemplate = validateTemplate || request.TemplateVMID != nil && profile.BuildStatus == "ready"
	validateTemplate = validateTemplate || request.SourceNode != nil && profile.BuildStatus == "ready"
	if validateTemplate {
		if s.pve == nil {
			writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is required to validate a ready image")
			return
		}
		summary, inventoryErr := s.pve.Summary(r.Context())
		if inventoryErr != nil {
			writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to validate the PVE template")
			return
		}
		if inventoryErr := validateImageTemplateInventory(profile, summary); inventoryErr != nil {
			writeError(w, http.StatusConflict, "image_template_unavailable", inventoryErr.Error())
			return
		}
	}
	if _, err := s.store.GPUProfileByID(r.Context(), profile.DefaultGPUProfileID); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_gpu_profile", "Default GPU profile was not found")
		return
	}
	updated, err := s.store.UpdateImageProfile(r.Context(), profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to update image profile")
		return
	}
	s.auditRequest(r, session.User.ID, "image_profile.updated", "image_profile", profile.ID, map[string]any{"build_status": profile.BuildStatus, "enabled": profile.Enabled})
	writeJSON(w, http.StatusOK, updated)
}

func applyImageProfileUpdate(profile *store.ImageProfile, request imageProfileUpdateRequest) {
	if request.DisplayName != nil {
		profile.DisplayName = strings.TrimSpace(*request.DisplayName)
	}
	if request.Enabled != nil {
		profile.Enabled = *request.Enabled
	}
	if request.SourceNode != nil {
		profile.SourceNode = strings.TrimSpace(*request.SourceNode)
	}
	if request.SourceISO != nil {
		profile.SourceISO = strings.TrimSpace(*request.SourceISO)
	}
	if request.SourceISOChecksum != nil {
		profile.SourceISOChecksum = strings.TrimSpace(*request.SourceISOChecksum)
	}
	if request.DriverISO != nil {
		profile.DriverISO = strings.TrimSpace(*request.DriverISO)
	}
	if request.DriverISOChecksum != nil {
		profile.DriverISOChecksum = strings.TrimSpace(*request.DriverISOChecksum)
	}
	if request.TemplateVMID != nil {
		profile.TemplateVMID = *request.TemplateVMID
	}
	if request.MirrorURL != nil {
		profile.MirrorURL = strings.TrimSpace(*request.MirrorURL)
	}
	if request.SecurityMirrorURL != nil {
		profile.SecurityMirrorURL = strings.TrimSpace(*request.SecurityMirrorURL)
	}
	if request.StoragePool != nil {
		profile.StoragePool = strings.TrimSpace(*request.StoragePool)
	}
	if request.Bridge != nil {
		profile.Bridge = strings.TrimSpace(*request.Bridge)
	}
	if request.WindowsImageName != nil {
		profile.WindowsImageName = strings.TrimSpace(*request.WindowsImageName)
	}
	if request.DefaultCores != nil {
		profile.DefaultCores = *request.DefaultCores
	}
	if request.DefaultMemoryMB != nil {
		profile.DefaultMemoryMB = *request.DefaultMemoryMB
	}
	if request.DefaultDiskGB != nil {
		profile.DefaultDiskGB = *request.DefaultDiskGB
	}
	if request.Firmware != nil {
		profile.Firmware = strings.TrimSpace(*request.Firmware)
	}
	if request.TPMVersion != nil {
		profile.TPMVersion = strings.TrimSpace(*request.TPMVersion)
	}
	if request.DefaultGPUProfileID != nil {
		profile.DefaultGPUProfileID = strings.TrimSpace(*request.DefaultGPUProfileID)
	}
	if request.BuildStatus != nil {
		profile.BuildStatus = strings.TrimSpace(*request.BuildStatus)
	}
	if request.StatusDetail != nil {
		profile.StatusDetail = strings.TrimSpace(*request.StatusDetail)
	}
}

func prepareImageBuildProfile(current store.ImageProfile, request *imageProfileUpdateRequest) (store.ImageProfile, error) {
	profile := current
	if request != nil {
		applyImageProfileUpdate(&profile, *request)
	}
	profile.BuildStatus = "draft"
	profile.StatusDetail = ""
	return profile, validateImageProfile(profile)
}

func validateImageProfile(profile store.ImageProfile) error {
	if profile.DisplayName == "" || len(profile.DisplayName) > 80 {
		return errors.New("Display name must be 1–80 characters")
	}
	if profile.TemplateVMID < 0 {
		return errors.New("Template VMID cannot be negative")
	}
	resourceNamePattern := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	if profile.SourceNode != "" && !resourceNamePattern.MatchString(profile.SourceNode) {
		return errors.New("Source node is invalid")
	}
	if profile.StoragePool != "" && !resourceNamePattern.MatchString(profile.StoragePool) {
		return errors.New("Storage pool is invalid")
	}
	if profile.Bridge != "" && !resourceNamePattern.MatchString(profile.Bridge) {
		return errors.New("Network bridge is invalid")
	}
	for _, media := range []string{profile.SourceISO, profile.DriverISO} {
		if media != "" && (!strings.Contains(media, ":iso/") || len(media) > 255) {
			return errors.New("Installation media must be a PVE ISO volume identifier")
		}
	}
	checksumPattern := regexp.MustCompile(`^sha256:[0-9a-fA-F]{64}$`)
	for _, checksum := range []string{profile.SourceISOChecksum, profile.DriverISOChecksum} {
		if checksum != "" && !checksumPattern.MatchString(checksum) {
			return errors.New("ISO checksum must use sha256:<64 hex characters>")
		}
	}
	for _, mirror := range []string{profile.MirrorURL, profile.SecurityMirrorURL} {
		if mirror == "" {
			continue
		}
		parsed, err := url.Parse(mirror)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return errors.New("Mirror URL must be an absolute HTTP or HTTPS URL without credentials")
		}
	}
	if len(profile.WindowsImageName) > 100 {
		return errors.New("Windows image name must not exceed 100 characters")
	}
	if profile.DefaultCores < 1 || profile.DefaultCores > 256 {
		return errors.New("Default CPU cores must be between 1 and 256")
	}
	if profile.DefaultMemoryMB < 512 || profile.DefaultMemoryMB > 1048576 {
		return errors.New("Default memory must be between 512 and 1048576 MiB")
	}
	if profile.DefaultDiskGB < 8 || profile.DefaultDiskGB > 16384 {
		return errors.New("Default disk must be between 8 and 16384 GiB")
	}
	if profile.Firmware != "seabios" && profile.Firmware != "uefi" {
		return errors.New("Firmware must be seabios or uefi")
	}
	if profile.TPMVersion != "none" && profile.TPMVersion != "2.0" {
		return errors.New("TPM version must be none or 2.0")
	}
	if profile.TPMVersion == "2.0" && profile.Firmware != "uefi" {
		return errors.New("TPM 2.0 requires UEFI firmware")
	}
	if !map[string]bool{"draft": true, "blocked": true, "building": true, "testing": true, "ready": true, "failed": true}[profile.BuildStatus] {
		return errors.New("Build status is invalid")
	}
	if profile.BuildStatus == "ready" && profile.TemplateVMID == 0 {
		return errors.New("A ready image profile requires a template VMID")
	}
	if len(profile.StatusDetail) > 500 {
		return errors.New("Status detail must not exceed 500 characters")
	}
	return nil
}

func (s *Server) startImageBuild(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	if !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf_token", "CSRF token is invalid")
		return
	}
	if s.imageBuilder == nil || !s.imageBuilder.Available() {
		writeError(w, http.StatusServiceUnavailable, "image_builder_unavailable", "Image builder is not configured")
		return
	}
	idempotencyKey, fingerprint, proceed := s.prepareJobRequest(w, r, "user:"+session.User.ID)
	if !proceed {
		return
	}
	profile, err := s.store.ImageProfileByID(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "image_profile_not_found", "Image profile was not found")
		} else {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read image profile")
		}
		return
	}
	var input struct {
		Profile           *imageProfileUpdateRequest `json:"profile"`
		BuilderPassword   string                     `json:"builder_password"`
		WindowsProductKey string                     `json:"windows_product_key"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	profile, err = prepareImageBuildProfile(profile, input.Profile)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_image_profile", err.Error())
		return
	}
	if _, err := s.store.GPUProfileByID(r.Context(), profile.DefaultGPUProfileID); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_gpu_profile", "Default GPU profile was not found")
		return
	}
	buildRequest := imageBuildRequest(profile, input.BuilderPassword, input.WindowsProductKey)
	if err := imagebuilder.ValidateRequest(buildRequest); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_image_build", err.Error())
		return
	}
	if s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return
	}
	summary, err := s.pve.Summary(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to validate the PVE build target")
		return
	}
	if err := validateImageBuildInventory(profile, summary); err != nil {
		writeError(w, http.StatusConflict, "image_build_target_unavailable", err.Error())
		return
	}
	sanitizedRequest, _ := json.Marshal(map[string]any{
		"image_profile_id": profile.ID,
		"node":             profile.SourceNode,
		"template_vmid":    profile.TemplateVMID,
		"source_iso":       profile.SourceISO,
		"driver_iso":       profile.DriverISO,
		"storage_pool":     profile.StoragePool,
		"bridge":           profile.Bridge,
		"cores":            profile.DefaultCores,
		"memory_mb":        profile.DefaultMemoryMB,
		"disk_gb":          profile.DefaultDiskGB,
		"mirror_url":       profile.MirrorURL,
	})
	jobToken, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create image build job")
		return
	}
	job, created, err := s.store.CreateJob(r.Context(), store.Job{
		ID: "job_" + jobToken, IdempotencyKey: idempotencyKey, RequestFingerprint: fingerprint, Operation: "image.build", State: "accepted",
		TargetVMID: profile.TemplateVMID, TargetNode: profile.SourceNode, TaskNode: profile.SourceNode,
		Request: sanitizedRequest, CreatedBy: session.User.ID,
	})
	if errors.Is(err, store.ErrConflict) {
		writeJobConflict(w)
		return
	}
	if err != nil {
		if active, activeErr := s.store.ActiveImageBuildJob(r.Context(), profile.ID); activeErr == nil {
			writeJSON(w, http.StatusAccepted, active)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create image build job")
		return
	}
	if !created {
		writeJSON(w, http.StatusAccepted, job)
		return
	}
	detail := "Image build is queued"
	if err := s.store.UpdateJobProgress(r.Context(), job.ID, "running", 1, detail, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to start image build job")
		return
	}
	profile.BuildStatus = "building"
	profile.StatusDetail = detail
	if _, err := s.store.UpdateImageProfile(r.Context(), profile); err != nil {
		_ = s.store.UpdateJobProgress(r.Context(), job.ID, "failed", 0, "", "Unable to update image profile")
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to update image profile")
		return
	}
	s.auditRequest(r, session.User.ID, "image_build.started", "job", job.ID, map[string]any{
		"image_profile_id": profile.ID, "template_vmid": profile.TemplateVMID, "node": profile.SourceNode,
	})
	job.State, job.Progress, job.Detail = "running", 1, detail
	go s.runImageBuild(job, profile.ID, buildRequest)
	writeJSON(w, http.StatusAccepted, job)
}

func imageBuildRequest(profile store.ImageProfile, builderPassword, productKey string) imagebuilder.Request {
	return imagebuilder.Request{
		ProfileID: profile.ID, Node: profile.SourceNode, VMID: profile.TemplateVMID,
		SourceISO: profile.SourceISO, SourceISOChecksum: profile.SourceISOChecksum,
		DriverISO: profile.DriverISO, DriverISOChecksum: profile.DriverISOChecksum,
		MirrorURL: profile.MirrorURL, SecurityMirrorURL: profile.SecurityMirrorURL,
		StoragePool: profile.StoragePool, Bridge: profile.Bridge, WindowsImageName: profile.WindowsImageName,
		Cores: profile.DefaultCores, MemoryMB: profile.DefaultMemoryMB, DiskGB: profile.DefaultDiskGB,
		Firmware: profile.Firmware, TPMVersion: profile.TPMVersion,
		BuilderPassword: builderPassword, WindowsProductKey: productKey,
	}
}

func validateImageBuildInventory(profile store.ImageProfile, summary pve.Summary) error {
	if !summary.Writable {
		return errors.New("PVE mutations are disabled")
	}
	nodeReady := false
	for _, node := range summary.Nodes {
		if node.Name == profile.SourceNode && node.Status == "online" {
			nodeReady = true
			break
		}
	}
	if !nodeReady {
		return errors.New("Configured build node is not online")
	}
	storageReady := false
	for _, storage := range summary.Storage {
		if storage.Name == profile.StoragePool && strings.Contains(storage.Content, "images") {
			storageReady = true
			break
		}
	}
	if !storageReady {
		return errors.New("Configured storage does not accept VM images")
	}
	for _, vm := range summary.VMs {
		if vm.VMID == profile.TemplateVMID {
			return errors.New("Template VMID already exists; choose a new VMID before rebuilding")
		}
	}
	return nil
}

func (s *Server) runImageBuild(job store.Job, profileID string, request imagebuilder.Request) {
	timeout := 90 * time.Minute
	if strings.HasPrefix(request.ProfileID, "windows-") {
		timeout = 3 * time.Hour
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	lastPersisted := time.Time{}
	err := s.imageBuilder.Build(ctx, request, func(progress imagebuilder.Progress) {
		if time.Since(lastPersisted) < 2*time.Second && progress.Percent < 90 {
			return
		}
		lastPersisted = time.Now()
		detail := limitText(progress.Detail, 500)
		_ = s.store.UpdateJobProgress(ctx, job.ID, "running", progress.Percent, detail, "")
		_ = s.store.UpdateImageBuildState(ctx, profileID, "building", detail)
	})
	if err != nil {
		message := limitText(err.Error(), 500)
		_ = s.store.UpdateJobProgress(context.Background(), job.ID, "failed", 0, "Image build failed", message)
		_ = s.store.UpdateImageBuildState(context.Background(), profileID, "failed", message)
		_ = s.store.Audit(context.Background(), job.CreatedBy, "image_build.failed", "job", job.ID, map[string]any{"image_profile_id": profileID, "error": message})
		s.logger.Error("image build failed", "job_id", job.ID, "image_profile_id", profileID, "error", message)
		return
	}
	detail := "Template created; clone and validate Guest Agent and RDP before marking it ready"
	_ = s.store.UpdateJobProgress(context.Background(), job.ID, "succeeded", 100, detail, "")
	_ = s.store.UpdateImageBuildState(context.Background(), profileID, "testing", detail)
	_ = s.store.Audit(context.Background(), job.CreatedBy, "image_build.succeeded", "job", job.ID, map[string]any{"image_profile_id": profileID, "template_vmid": job.TargetVMID})
}

func limitText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}

func validateImageTemplateInventory(profile store.ImageProfile, summary pve.Summary) error {
	for _, vm := range summary.VMs {
		if vm.VMID != profile.TemplateVMID {
			continue
		}
		if vm.Kind != "qemu" || !vm.Template {
			return errors.New("Configured VMID is not a QEMU template")
		}
		if profile.SourceNode != "" && vm.Node != profile.SourceNode {
			return fmt.Errorf("Configured template is on node %s, not %s", vm.Node, profile.SourceNode)
		}
		return nil
	}
	return errors.New("Configured template VMID was not found in PVE")
}

func (s *Server) updateGPUProfile(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	if !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf_token", "CSRF token is invalid")
		return
	}
	profile, err := s.store.GPUProfileByID(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "gpu_profile_not_found", "GPU profile was not found")
		} else {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read GPU profile")
		}
		return
	}
	var request struct {
		DisplayName     *string `json:"display_name"`
		ResourceMapping *string `json:"resource_mapping"`
		Enabled         *bool   `json:"enabled"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if request.DisplayName != nil {
		profile.DisplayName = strings.TrimSpace(*request.DisplayName)
	}
	if request.ResourceMapping != nil {
		profile.ResourceMapping = strings.TrimSpace(*request.ResourceMapping)
	}
	if request.Enabled != nil {
		profile.Enabled = *request.Enabled
	}
	if profile.DisplayName == "" || len(profile.DisplayName) > 80 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_gpu_profile", "Display name must be 1–80 characters")
		return
	}
	if profile.ID == "none" && !profile.Enabled {
		writeError(w, http.StatusUnprocessableEntity, "invalid_gpu_profile", "CPU-only profile cannot be disabled")
		return
	}
	if profile.ID != "none" && profile.ResourceMapping != "" && !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`).MatchString(profile.ResourceMapping) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_gpu_profile", "PCI resource mapping ID is invalid")
		return
	}
	if request.Enabled != nil && *request.Enabled && profile.ID != "none" && s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return
	}
	if request.Enabled != nil && *request.Enabled && profile.ID != "none" {
		assignable, inventoryErr := s.gpuProfileAssignable(r.Context(), profile)
		if inventoryErr != nil {
			writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to validate GPU inventory")
			return
		}
		if !assignable {
			writeError(w, http.StatusConflict, "gpu_unavailable", "No assignable GPU matches this profile")
			return
		}
	}
	updated, err := s.store.UpdateGPUProfile(r.Context(), profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to update GPU profile")
		return
	}
	s.auditRequest(r, session.User.ID, "gpu_profile.updated", "gpu_profile", profile.ID, map[string]any{"enabled": profile.Enabled})
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) gpuProfileAssignable(ctx context.Context, profile store.GPUProfile) (bool, error) {
	if profile.Mode == "none" {
		return true, nil
	}
	if profile.ResourceMapping == "" {
		return false, nil
	}
	summary, err := s.pve.Summary(ctx)
	if err != nil {
		return false, err
	}
	nodes := make([]string, 0, len(summary.Nodes))
	for _, node := range summary.Nodes {
		if node.Status == "online" {
			nodes = append(nodes, node.Name)
		}
	}
	devices, err := s.pve.GPUDevices(ctx, nodes)
	if err != nil {
		return false, err
	}
	mappings, err := s.pve.PCIResourceMappings(ctx)
	if err != nil {
		return false, err
	}
	for _, node := range nodes {
		if gpuPlacementAvailable(profile, node, mappings, devices) {
			return true, nil
		}
	}
	return false, nil
}

func gpuPlacementAvailable(profile store.GPUProfile, targetNode string, mappings []pve.PCIResourceMapping, devices []pve.GPUDevice) bool {
	if profile.Mode == "none" {
		return true
	}
	for _, mapping := range mappings {
		if mapping.ID != profile.ResourceMapping || (profile.Mode == "mdev") != mapping.MDev {
			continue
		}
		for _, entry := range mapping.Entries {
			if entry.Node != targetNode {
				continue
			}
			for _, device := range devices {
				vendorMatches := profile.VendorID == "" || strings.EqualFold(profile.VendorID, device.VendorID)
				classMatches := profile.DeviceClass == "" || strings.EqualFold(profile.DeviceClass, device.Class)
				if device.Node != targetNode || !mappingEntryContainsDevice(entry, device.ID) || !vendorMatches || !classMatches {
					continue
				}
				switch profile.Mode {
				case "pci_passthrough":
					if device.Assignable {
						return true
					}
				case "mdev":
					for _, kind := range device.MDevTypes {
						if kind.Type == profile.MDevType && kind.Available > 0 {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func mappingEntryContainsDevice(entry pve.PCIResourceMappingEntry, deviceID string) bool {
	if pciPathContainsDevice(entry.DeviceID, deviceID) {
		return true
	}
	for _, path := range entry.DevicePaths {
		if pciPathContainsDevice(path, deviceID) {
			return true
		}
	}
	return false
}

func pciPathContainsDevice(path, deviceID string) bool {
	if strings.EqualFold(path, deviceID) {
		return true
	}
	path = strings.ToLower(path)
	deviceID = strings.ToLower(deviceID)
	return !strings.Contains(path[strings.LastIndex(path, ":")+1:], ".") && strings.HasPrefix(deviceID, path+".")
}

func (s *Server) createPCIResourceMapping(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	request, ok := s.decodePCIResourceMappingRequest(w, r, session)
	if !ok {
		return
	}
	existing, err := s.pve.PCIResourceMappings(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to read PVE resource mappings")
		return
	}
	for _, mapping := range existing {
		if mapping.ID == request.ID {
			writeError(w, http.StatusConflict, "pci_resource_mapping_exists", "PCI resource mapping already exists")
			return
		}
	}
	mapping, ok := s.resolvePCIResourceMapping(w, r, request)
	if !ok {
		return
	}
	if err := s.pve.CreatePCIResourceMapping(r.Context(), mapping); err != nil {
		s.logger.Error("create PVE PCI resource mapping", "mapping_id", mapping.ID, "error", err)
		writeError(w, http.StatusBadGateway, "pve_mutation_failed", "Unable to create the PVE PCI resource mapping")
		return
	}
	s.auditRequest(r, session.User.ID, "pci_resource_mapping.created", "pci_resource_mapping", mapping.ID, map[string]any{"entries": len(mapping.Entries)})
	writeJSON(w, http.StatusCreated, mapping)
}

func (s *Server) updatePCIResourceMapping(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	request, ok := s.decodePCIResourceMappingRequest(w, r, session)
	if !ok {
		return
	}
	request.ID = strings.TrimSpace(r.PathValue("id"))
	existing, err := s.pve.PCIResourceMappings(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to read PVE resource mappings")
		return
	}
	found := false
	for _, mapping := range existing {
		if mapping.ID == request.ID {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "pci_resource_mapping_not_found", "PCI resource mapping was not found")
		return
	}
	mapping, ok := s.resolvePCIResourceMapping(w, r, request)
	if !ok {
		return
	}
	if err := s.pve.UpdatePCIResourceMapping(r.Context(), mapping); err != nil {
		s.logger.Error("update PVE PCI resource mapping", "mapping_id", mapping.ID, "error", err)
		writeError(w, http.StatusBadGateway, "pve_mutation_failed", "Unable to update the PVE PCI resource mapping")
		return
	}
	s.auditRequest(r, session.User.ID, "pci_resource_mapping.updated", "pci_resource_mapping", mapping.ID, map[string]any{"entries": len(mapping.Entries)})
	writeJSON(w, http.StatusOK, mapping)
}

func (s *Server) decodePCIResourceMappingRequest(w http.ResponseWriter, r *http.Request, session store.Session) (pciResourceMappingRequest, bool) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return pciResourceMappingRequest{}, false
	}
	if !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf_token", "CSRF token is invalid")
		return pciResourceMappingRequest{}, false
	}
	if s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return pciResourceMappingRequest{}, false
	}
	var request pciResourceMappingRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return pciResourceMappingRequest{}, false
	}
	request.ID = strings.TrimSpace(request.ID)
	request.Description = strings.TrimSpace(request.Description)
	return request, true
}

func (s *Server) resolvePCIResourceMapping(w http.ResponseWriter, r *http.Request, request pciResourceMappingRequest) (pve.PCIResourceMapping, bool) {
	if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`).MatchString(request.ID) || len(request.Description) > 256 || len(request.Entries) == 0 || len(request.Entries) > 64 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_pci_resource_mapping", "PCI resource mapping metadata is invalid")
		return pve.PCIResourceMapping{}, false
	}
	summary, err := s.pve.Summary(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to read PVE nodes")
		return pve.PCIResourceMapping{}, false
	}
	if !summary.Writable {
		writeError(w, http.StatusConflict, "pve_mutations_disabled", "PVE mutations are disabled")
		return pve.PCIResourceMapping{}, false
	}
	online := make(map[string]bool, len(summary.Nodes))
	requestedNodes := make([]string, 0, len(request.Entries))
	for _, node := range summary.Nodes {
		online[node.Name] = node.Status == "online"
	}
	for _, entry := range request.Entries {
		if online[entry.Node] && !containsString(requestedNodes, entry.Node) {
			requestedNodes = append(requestedNodes, entry.Node)
		}
	}
	if len(requestedNodes) == 0 {
		writeError(w, http.StatusConflict, "gpu_node_unavailable", "No requested mapping node is online")
		return pve.PCIResourceMapping{}, false
	}
	devices, err := s.pve.GPUDevices(r.Context(), requestedNodes)
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to read PVE GPU inventory")
		return pve.PCIResourceMapping{}, false
	}
	mapping, err := buildPCIResourceMapping(request, online, devices)
	if err != nil {
		writeError(w, http.StatusConflict, "gpu_unavailable", err.Error())
		return pve.PCIResourceMapping{}, false
	}
	return mapping, true
}

func buildPCIResourceMapping(request pciResourceMappingRequest, online map[string]bool, devices []pve.GPUDevice) (pve.PCIResourceMapping, error) {
	mapping := pve.PCIResourceMapping{ID: request.ID, Description: request.Description, MDev: request.MDev, Entries: make([]pve.PCIResourceMappingEntry, 0, len(request.Entries))}
	seen := map[string]bool{}
	for _, requested := range request.Entries {
		requested.Node = strings.TrimSpace(requested.Node)
		requested.DeviceID = strings.ToLower(strings.TrimSpace(requested.DeviceID))
		if !online[requested.Node] {
			return pve.PCIResourceMapping{}, fmt.Errorf("Mapping node %s is not online", requested.Node)
		}
		key := requested.Node + "\x00" + requested.DeviceID
		if seen[key] {
			return pve.PCIResourceMapping{}, errors.New("PCI resource mapping contains a duplicate device")
		}
		seen[key] = true
		var matched *pve.GPUDevice
		for index := range devices {
			device := &devices[index]
			if device.Node == requested.Node && strings.EqualFold(device.ID, requested.DeviceID) {
				matched = device
				break
			}
		}
		if matched == nil || (!request.MDev && (!matched.Assignable || matched.IOMMUGroup == nil)) || (request.MDev && !matched.MDevCapable) {
			return pve.PCIResourceMapping{}, fmt.Errorf("GPU %s on %s is not assignable", requested.DeviceID, requested.Node)
		}
		hardwareID := strings.TrimPrefix(strings.ToLower(matched.VendorID), "0x") + ":" + strings.TrimPrefix(strings.ToLower(matched.DeviceID), "0x")
		subsystemID := ""
		if matched.SubsystemVendorID != "" && matched.SubsystemDeviceID != "" {
			subsystemID = strings.TrimPrefix(strings.ToLower(matched.SubsystemVendorID), "0x") + ":" + strings.TrimPrefix(strings.ToLower(matched.SubsystemDeviceID), "0x")
		}
		entry := pve.PCIResourceMappingEntry{
			Node: requested.Node, DeviceID: matched.ID, DevicePaths: []string{matched.ID}, HardwareID: hardwareID, SubsystemID: subsystemID,
		}
		if matched.IOMMUGroup != nil {
			entry.IOMMUGroup = strconv.Itoa(*matched.IOMMUGroup)
		}
		mapping.Entries = append(mapping.Entries, entry)
	}
	return mapping, nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (s *Server) createDesktopInstance(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	if !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf_token", "CSRF token is invalid")
		return
	}
	idempotencyKey, fingerprint, proceed := s.prepareJobRequest(w, r, "user:"+session.User.ID)
	if !proceed {
		return
	}
	var request desktopCloneRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if !strings.HasPrefix(request.Name, "vc-workspace-") || len(request.Name) > 63 || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*$`).MatchString(request.Name) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", "Desktop names must start with vc-workspace- and contain only letters, numbers, and dashes")
		return
	}
	if s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return
	}
	summary, err := s.pve.Summary(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to validate PVE resources")
		return
	}
	var source *pve.VM
	for index := range summary.VMs {
		if summary.VMs[index].VMID == request.SourceVMID && summary.VMs[index].Kind == "qemu" && summary.VMs[index].Template {
			source = &summary.VMs[index]
			break
		}
	}
	if source == nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_template", "Source VMID is not a QEMU template")
		return
	}
	imageProfile, imageErr := s.store.ImageProfileByTemplateVMID(r.Context(), request.SourceVMID)
	if errors.Is(imageErr, store.ErrConflict) {
		writeError(w, http.StatusConflict, "image_template_ambiguous", "Multiple image profiles reference this template; resolve the duplicate references before cloning")
		return
	}
	if imageErr != nil && !errors.Is(imageErr, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read the source image")
		return
	}
	if err := validateCloneImage(request, imageProfile, imageErr == nil); err != nil {
		writeError(w, http.StatusConflict, "image_not_cloneable", err.Error())
		return
	}
	// Placement is resolved exclusively from an enabled GPU profile below.
	request.PCIResourceMapping, request.MDevType = "", ""
	request.SourceOSFamily = ""
	request.PVEPrincipal = s.pve.Principal()
	if imageErr == nil {
		request.SourceOSFamily = imageProfile.OSFamily
	}
	if request.TargetNode == "" {
		request.TargetNode = source.Node
	}
	nodeOnline := false
	for _, node := range summary.Nodes {
		if node.Name == request.TargetNode && node.Status == "online" {
			nodeOnline = true
			break
		}
	}
	if !nodeOnline {
		writeError(w, http.StatusUnprocessableEntity, "invalid_target_node", "Target node is not online")
		return
	}
	request.GPUProfileID = strings.TrimSpace(request.GPUProfileID)
	if request.GPUProfileID == "" {
		// Use the same source snapshot that passed lifecycle admission above.
		if imageErr == nil {
			request.GPUProfileID = imageProfile.DefaultGPUProfileID
		} else {
			request.GPUProfileID = "none"
		}
	}
	gpuProfile, err := s.store.GPUProfileByID(r.Context(), request.GPUProfileID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_gpu_profile", "GPU profile was not found")
		} else {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read the GPU profile")
		}
		return
	}
	if !gpuProfile.Enabled {
		writeError(w, http.StatusConflict, "gpu_profile_disabled", "GPU profile is disabled")
		return
	}
	if gpuProfile.Mode != "none" {
		profileSupported := gpuProfile.Mode == "pci_passthrough" && gpuProfile.Exclusive
		profileSupported = profileSupported || gpuProfile.Mode == "mdev" && !gpuProfile.Exclusive && gpuProfile.MDevType != ""
		if !profileSupported || gpuProfile.AllowLiveMigration {
			writeError(w, http.StatusUnprocessableEntity, "unsupported_gpu_profile", "GPU profile is not supported by this scheduler")
			return
		}
		mappings, mappingErr := s.pve.PCIResourceMappings(r.Context())
		if mappingErr != nil {
			writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to validate PCI resource mappings")
			return
		}
		devices, deviceErr := s.pve.GPUDevices(r.Context(), []string{request.TargetNode})
		if deviceErr != nil {
			writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to validate GPU inventory")
			return
		}
		if !gpuPlacementAvailable(gpuProfile, request.TargetNode, mappings, devices) {
			writeError(w, http.StatusConflict, "gpu_unavailable", "GPU mapping is not assignable on the target node")
			return
		}
		request.PCIResourceMapping = gpuProfile.ResourceMapping
		request.MDevType = gpuProfile.MDevType
	}
	storageValid := request.Storage == ""
	for _, storage := range summary.Storage {
		if storage.Name == request.Storage && strings.Contains(storage.Content, "images") {
			storageValid = true
			break
		}
	}
	if !storageValid {
		writeError(w, http.StatusUnprocessableEntity, "invalid_storage", "Storage does not accept VM images")
		return
	}
	targetVMID, err := s.pve.NextVMID(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to allocate a VMID")
		return
	}
	encodedRequest, _ := json.Marshal(request)
	jobID, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create job")
		return
	}
	occupiedVMIDs := make([]int, 0, len(summary.VMs))
	for _, machine := range summary.VMs {
		occupiedVMIDs = append(occupiedVMIDs, machine.VMID)
	}
	createdJob, created, err := s.store.ReserveAvailableDesktopCloneJob(r.Context(), store.Job{
		ID: "job_" + jobID, IdempotencyKey: idempotencyKey, RequestFingerprint: fingerprint, Operation: "pve.template_clone", State: "accepted",
		SourceVMID: request.SourceVMID, TargetVMID: targetVMID, TargetNode: request.TargetNode,
		TaskNode: source.Node, Request: encodedRequest, CreatedBy: session.User.ID,
	}, occupiedVMIDs)
	if errors.Is(err, store.ErrConflict) {
		writeJobConflict(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create job")
		return
	}
	if !created {
		writeJSON(w, http.StatusAccepted, createdJob)
		return
	}
	targetVMID = createdJob.TargetVMID
	upid, err := s.pve.CloneTemplate(r.Context(), pve.CloneRequest{
		Description: "VC Workspace clone job: " + createdJob.ID,
		SourceNode:  source.Node, SourceVMID: request.SourceVMID, TargetVMID: targetVMID,
		Name: request.Name, TargetNode: request.TargetNode, Storage: request.Storage, Full: request.Full,
	})
	if err != nil {
		// A lost response does not prove that PVE rejected the mutation.
		// Keep the reservation unresolved and never automatically replay it.
		_ = s.store.UpdateJobTask(r.Context(), createdJob.ID, "accepted", "", "PVE clone outcome is unknown; reconciliation required")
		s.logger.Error("clone PVE template", "job_id", createdJob.ID, "source_vmid", request.SourceVMID, "target_vmid", targetVMID, "error", err)
		writeError(w, http.StatusBadGateway, "pve_clone_uncertain", "Unable to confirm the clone outcome; inspect the original job before creating another desktop")
		return
	}
	if err := s.store.RecordCloneTaskHandle(r.Context(), createdJob, upid); err != nil {
		s.logger.Error("persist clone task handle", "job_id", createdJob.ID, "target_vmid", targetVMID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "clone_task_persistence_uncertain", "The clone was submitted but its task record could not be confirmed; inspect the original job before creating another desktop")
		return
	}
	s.auditRequest(r, session.User.ID, "desktop.clone_requested", "job", createdJob.ID, map[string]any{"source_vmid": request.SourceVMID, "target_vmid": targetVMID, "name": request.Name, "gpu_profile_id": request.GPUProfileID, "pci_resource_mapping": request.PCIResourceMapping})
	createdJob.State = "running"
	createdJob.UPID = upid
	go s.watchPVEJob(createdJob.ID)
	writeJSON(w, http.StatusAccepted, createdJob)
}

func (s *Server) watchPVEJob(jobID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		job, err := s.refreshPVEJob(ctx, jobID)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, store.ErrNotFound) {
				return
			}
			s.logger.Warn("watch PVE job", "job_id", jobID, "error", err)
		} else if job.State != "running" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) refreshPVEJob(ctx context.Context, jobID string) (store.Job, error) {
	job, err := s.store.JobByID(ctx, jobID)
	if err != nil || job.State != "running" || job.UPID == "" || s.pve == nil {
		return job, err
	}
	if job.Operation == "pve.template_clone" {
		unlock, lockErr := s.store.AcquireDesktopControlLock(ctx, job.TargetVMID)
		if lockErr != nil {
			return job, lockErr
		}
		defer unlock()
		// A watcher, a restarted replica and an administrator poll may race.
		// Re-read after the shared lock before any GPU or registry writes.
		job, err = s.store.JobByID(ctx, jobID)
		if err != nil || job.State != "running" || job.UPID == "" {
			return job, err
		}
	}
	status, err := s.pve.TaskStatus(ctx, job.TaskNode, job.UPID)
	if err != nil || status.Status != "stopped" {
		return job, err
	}
	state, message := "succeeded", ""
	if status.ExitStatus != "OK" {
		state, message = "failed", status.ExitStatus
	} else if job.Operation == "pve.template_clone" {
		var request desktopCloneRequest
		if err := json.Unmarshal(job.Request, &request); err != nil {
			state, message = "failed", "Stored desktop request is invalid"
		} else {
			// A successful historical task does not identify today's VM at the
			// same numeric ID. Recheck before writing GPU configuration or
			// publishing access, including after recovery or process restart.
			if err := s.requireCloneTarget(ctx, job, request); err != nil {
				return job, err
			}
			if err := s.ensureCloneNetwork(ctx, job, request); err != nil {
				return job, err
			}
			if request.PCIResourceMapping != "" {
				configuration, err := s.pve.VMConfiguration(ctx, job.TargetNode, job.TargetVMID)
				if err != nil {
					return job, err
				}
				if configuration.Template || configuration.Lock != "" || configuration.Name != request.Name || configuration.Description != "VC Workspace clone job: "+job.ID || !pve.ValidConfigurationDigest(configuration.Digest) {
					return job, errors.New("clone GPU configuration precondition is not verified")
				}
				var configureErr error
				// A prior write may have succeeded before registry persistence
				// failed. Do not replay device configuration merely to retry SQL.
				if pve.MatchesGPUResourceMapping(configuration.PCIHostDevices["hostpci0"], request.PCIResourceMapping, request.MDevType) {
					configureErr = nil
				} else if request.MDevType != "" {
					configureErr = s.pve.ConfigureVMMDevResourceMapping(ctx, job.TargetNode, job.TargetVMID, request.PCIResourceMapping, request.MDevType, configuration.Digest)
				} else {
					configureErr = s.pve.ConfigureVMPCIResourceMapping(ctx, job.TargetNode, job.TargetVMID, request.PCIResourceMapping, configuration.Digest)
				}
				if configureErr != nil {
					s.logger.Error("configure cloned VM PCI mapping", "job_id", job.ID, "target_vmid", job.TargetVMID, "mapping", request.PCIResourceMapping, "mdev_type", request.MDevType, "error", configureErr)
					state, message = "failed", "PCI resource mapping configuration failed"
				}
			}
			osFamily := request.SourceOSFamily
			if osFamily != "linux" && osFamily != "windows" {
				osFamily = "unknown"
				// Legacy/unregistered jobs have no admission snapshot. Inspect
				// the clone itself rather than a mutable source profile.
				if configuration, configurationErr := s.pve.VMConfiguration(ctx, job.TargetNode, job.TargetVMID); configurationErr == nil {
					osFamily = desktopOSFamily(configuration.OSType)
				}
			}
			if err := s.requireCloneTarget(ctx, job, request); err != nil {
				return job, err
			}
			desktop := store.ManagedDesktop{VMID: job.TargetVMID, DisplayName: request.Name, Node: job.TargetNode, OSFamily: osFamily, Present: true, Enabled: true}
			var registryErr error
			if state == "failed" {
				registryErr = s.store.QuarantineClonedDesktop(ctx, desktop)
			} else {
				registryErr = s.store.UpsertManagedDesktop(ctx, desktop)
			}
			if registryErr != nil {
				s.logger.Error("register cloned desktop", "job_id", job.ID, "target_vmid", job.TargetVMID, "error", registryErr)
				// Keep the observed task pending reconciliation until registry
				// state (including failure quarantine) is durably recorded.
				return job, registryErr
			}
		}
	}
	completed, err := s.store.CompleteJobTask(ctx, job.ID, state, job.UPID, message)
	if err != nil {
		return job, err
	}
	job.State, job.Error = state, message
	if completed {
		eventType := "job.succeeded"
		if state == "failed" {
			eventType = "job.failed"
		}
		_ = s.store.Audit(ctx, job.CreatedBy, eventType, "job", job.ID, map[string]any{
			"operation": job.Operation, "target_vmid": job.TargetVMID, "target_node": job.TargetNode, "error": message,
		})
	}
	return job, nil
}

func (s *Server) job(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	w.Header().Set("Cache-Control", "no-store")
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	job, err := s.store.JobByID(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job_not_found", "Job was not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read job")
		return
	}
	if job.State == "running" && job.UPID != "" && s.pve != nil {
		if refreshed, refreshErr := s.refreshPVEJob(r.Context(), job.ID); refreshErr == nil {
			job = refreshed
		} else if !errors.Is(refreshErr, context.Canceled) {
			s.logger.Warn("refresh PVE job", "job_id", job.ID, "error", refreshErr)
		}
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) changeVirtualMachinePower(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	if !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf_token", "CSRF token is invalid")
		return
	}
	idempotencyKey, fingerprint, proceed := s.prepareJobRequest(w, r, "user:"+session.User.ID)
	if !proceed {
		return
	}
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	action := r.PathValue("action")
	if err != nil || vmid <= 0 || (action != "start" && action != "stop") {
		writeError(w, http.StatusBadRequest, "invalid_power_request", "Power request is invalid")
		return
	}
	if s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return
	}
	summary, err := s.desktopInventory(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to validate PVE resources")
		return
	}
	var machine *pve.VM
	for index := range summary.VMs {
		candidate := &summary.VMs[index]
		if candidate.VMID == vmid && candidate.Kind == "qemu" && !candidate.Template {
			machine = candidate
			break
		}
	}
	if machine == nil || !machine.Managed {
		writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		return
	}
	if (action == "start" && machine.Status == "running") || (action == "stop" && machine.Status == "stopped") {
		writeError(w, http.StatusConflict, "power_state_conflict", "Virtual machine is already in the requested power state")
		return
	}
	encodedRequest, _ := json.Marshal(map[string]any{"vmid": vmid, "action": action, "node": machine.Node})
	jobToken, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create job")
		return
	}
	operation := "pve.vm_" + action
	createdJob, created, err := s.store.CreateJob(r.Context(), store.Job{
		ID: "job_" + jobToken, IdempotencyKey: idempotencyKey, RequestFingerprint: fingerprint, Operation: operation, State: "accepted",
		TargetVMID: vmid, TargetNode: machine.Node, TaskNode: machine.Node, Request: encodedRequest, CreatedBy: session.User.ID,
	})
	if errors.Is(err, store.ErrConflict) {
		writeJobConflict(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create job")
		return
	}
	if !created {
		writeJSON(w, http.StatusAccepted, createdJob)
		return
	}
	upid, err := s.pve.ChangePowerState(r.Context(), machine.Node, vmid, action)
	if err != nil {
		_ = s.store.UpdateJobTask(r.Context(), createdJob.ID, "failed", "", err.Error())
		s.logger.Error("change PVE power state", "job_id", createdJob.ID, "vmid", vmid, "action", action, "error", err)
		writeError(w, http.StatusBadGateway, "pve_power_failed", "PVE rejected the power request")
		return
	}
	_ = s.store.UpdateJobTask(r.Context(), createdJob.ID, "running", upid, "")
	s.auditRequest(r, session.User.ID, "virtual_machine."+action+"_requested", "job", createdJob.ID, map[string]any{"vmid": vmid})
	createdJob.State, createdJob.UPID = "running", upid
	writeJSON(w, http.StatusAccepted, createdJob)
}

func (s *Server) desktopAccessPolicy(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	if err != nil || vmid <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_desktop", "Desktop identifier is invalid")
		return
	}
	machine, err := s.managedVirtualMachine(r.Context(), vmid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		} else {
			writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to validate PVE resources")
		}
		return
	}
	policy, err := s.store.EnsureDesktopAccessPolicy(r.Context(), vmid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read desktop permissions")
		return
	}
	if policy.OSFamily == "unknown" {
		if configuration, configErr := s.pve.VMConfiguration(r.Context(), machine.Node, machine.VMID); configErr == nil {
			policy.OSFamily = desktopOSFamily(configuration.OSType)
		}
	}
	writeJSON(w, http.StatusOK, policy)
}

func (s *Server) updateDesktopAccessPolicy(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
		return
	}
	if !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf_token", "CSRF token is invalid")
		return
	}
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	if err != nil || vmid <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_desktop", "Desktop identifier is invalid")
		return
	}
	var request desktopAccessPolicyRequest
	if err := decodeJSON(r, &request); err != nil || (request.PrivilegeMode != "standard" && request.PrivilegeMode != "local_admin") {
		writeError(w, http.StatusBadRequest, "invalid_access_policy", "Privilege mode must be standard or local_admin")
		return
	}
	machine, err := s.managedVirtualMachine(r.Context(), vmid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		} else {
			writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to validate PVE resources")
		}
		return
	}
	unlock, err := s.store.AcquireDesktopControlLock(r.Context(), vmid)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "desktop_busy", "Desktop policy is temporarily unavailable")
		return
	}
	defer unlock()
	current, err := s.store.EnsureDesktopAccessPolicy(r.Context(), vmid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read desktop permissions")
		return
	}
	clipboardRedirection := current.ClipboardRedirection
	driveRedirection := current.DriveRedirection
	managedBackground := current.ManagedBackground
	if request.ClipboardRedirection != nil {
		clipboardRedirection = *request.ClipboardRedirection
	}
	if request.DriveRedirection != nil {
		driveRedirection = *request.DriveRedirection
	}
	if request.ManagedBackground != nil {
		managedBackground = *request.ManagedBackground
	}
	policy, err := s.store.PutDesktopAccessPolicy(r.Context(), vmid, request.PrivilegeMode, clipboardRedirection, driveRedirection, managedBackground, session.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to save desktop permissions")
		return
	}
	s.auditRequest(r, session.User.ID, "desktop.access_policy_updated", "virtual_machine", strconv.Itoa(vmid), map[string]any{
		"vmid": vmid, "privilege_mode": request.PrivilegeMode, "clipboard_redirection": clipboardRedirection,
		"drive_redirection": driveRedirection, "managed_background": managedBackground, "desired_revision": policy.DesiredRevision,
	})
	if machine.Status == "running" {
		if applied, applyErr := s.applyDesktopAccessPolicy(r.Context(), machine, policy); applyErr == nil {
			policy = applied
			snapshot := nativeSessionPolicySnapshot(policy)
			s.auditRequest(r, session.User.ID, "desktop.access_policy_applied", "virtual_machine", strconv.Itoa(vmid), map[string]any{"vmid": vmid, "privilege_mode": request.PrivilegeMode, "applied_revision": policy.AppliedRevision, "policy_hash": snapshot.Hash})
		} else {
			s.logger.Warn("apply desktop access policy", "vmid", vmid, "mode", request.PrivilegeMode, "error", applyErr)
			s.auditRequest(r, session.User.ID, "desktop.access_policy_apply_failed", "virtual_machine", strconv.Itoa(vmid), map[string]any{"vmid": vmid, "privilege_mode": request.PrivilegeMode, "error": applyErr.Error()})
			if failed, markErr := s.store.MarkDesktopAccessPolicyFailed(r.Context(), vmid, policy.DesiredRevision, policy.OSFamily, "Guest Agent could not apply the policy"); markErr == nil {
				policy = failed
			}
		}
	}
	writeJSON(w, http.StatusOK, policy)
}

func (s *Server) reconcileDesktopAccessPolicy(ctx context.Context, machine pve.VM) (store.DesktopAccessPolicy, error) {
	policy, err := s.store.EnsureDesktopAccessPolicy(ctx, machine.VMID)
	if err != nil {
		return store.DesktopAccessPolicy{}, err
	}
	applied, err := s.applyDesktopAccessPolicy(ctx, machine, policy)
	if err != nil {
		osFamily := policy.OSFamily
		if osFamily == "" {
			osFamily = "unknown"
		}
		_, _ = s.store.MarkDesktopAccessPolicyFailed(ctx, machine.VMID, policy.DesiredRevision, osFamily, "Guest Agent could not apply the policy")
		return store.DesktopAccessPolicy{}, err
	}
	return applied, nil
}

func (s *Server) applyDesktopAccessPolicy(ctx context.Context, machine pve.VM, policy store.DesktopAccessPolicy) (store.DesktopAccessPolicy, error) {
	configuration, err := s.pve.VMConfiguration(ctx, machine.Node, machine.VMID)
	if err != nil {
		return store.DesktopAccessPolicy{}, err
	}
	osFamily := desktopOSFamily(configuration.OSType)
	policy.OSFamily = osFamily
	if policy.ManagedBackground {
		backgroundPath := "/tmp/vc-workspace-desktop-background-session.jpg"
		if osFamily == "windows" {
			backgroundPath = `C:\Windows\Temp\vc-workspace-desktop-background-session.jpg`
		}
		if err := s.pve.WriteGuestBinaryFile(ctx, machine.Node, machine.VMID, backgroundPath, brand.DesktopBackgroundSessionJPEG); err != nil {
			return store.DesktopAccessPolicy{}, fmt.Errorf("stage managed desktop background: %w", err)
		}
	}
	sessionCommand, err := desktopSessionPolicyCommand(osFamily, policy)
	if err != nil {
		return store.DesktopAccessPolicy{}, err
	}
	sessionContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	sessionResult, sessionErr := s.pve.ExecGuest(sessionContext, machine.Node, machine.VMID, sessionCommand)
	cancel()
	if sessionErr != nil {
		return store.DesktopAccessPolicy{}, sessionErr
	}
	if sessionResult.ExitCode != 0 {
		detail := strings.TrimSpace(sessionResult.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(sessionResult.Stdout)
		}
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return store.DesktopAccessPolicy{}, fmt.Errorf("guest session policy exited with %d: %s", sessionResult.ExitCode, detail)
	}
	bindings, err := s.store.GuestIdentityBindingsForDesktop(ctx, machine.VMID)
	if err != nil {
		return store.DesktopAccessPolicy{}, err
	}
	usernames := []string{"vdi"}
	seen := map[string]struct{}{"vdi": {}}
	for _, binding := range bindings {
		if binding.State == "disabled" {
			continue
		}
		if _, exists := seen[binding.GuestUsername]; !exists {
			seen[binding.GuestUsername] = struct{}{}
			usernames = append(usernames, binding.GuestUsername)
		}
	}
	agentSessions, err := s.store.AgentGuestSessions(ctx, machine.VMID)
	if err != nil {
		return store.DesktopAccessPolicy{}, err
	}
	for _, binding := range agentSessions {
		if binding.State != "ready" && binding.State != "provisioning" {
			continue
		}
		if _, exists := seen[binding.GuestUsername]; !exists {
			seen[binding.GuestUsername] = struct{}{}
			usernames = append(usernames, binding.GuestUsername)
		}
	}
	for _, username := range usernames {
		command, err := desktopAccessPolicyCommandForUser(osFamily, policy.PrivilegeMode, username)
		if err != nil {
			return store.DesktopAccessPolicy{}, err
		}
		execContext, cancel := context.WithTimeout(ctx, 60*time.Second)
		result, execErr := s.pve.ExecGuest(execContext, machine.Node, machine.VMID, command)
		cancel()
		if execErr != nil {
			return store.DesktopAccessPolicy{}, execErr
		}
		if result.ExitCode != 0 {
			detail := strings.TrimSpace(result.Stderr)
			if detail == "" {
				detail = strings.TrimSpace(result.Stdout)
			}
			if len(detail) > 300 {
				detail = detail[:300]
			}
			return store.DesktopAccessPolicy{}, fmt.Errorf("guest policy command for %s exited with %d: %s", username, result.ExitCode, detail)
		}
	}
	applied, err := s.store.MarkDesktopAccessPolicyApplied(ctx, machine.VMID, policy.DesiredRevision, osFamily)
	if err != nil {
		return store.DesktopAccessPolicy{}, err
	}
	return applied, nil
}

func desktopOSFamily(osType string) string {
	osType = strings.ToLower(strings.TrimSpace(osType))
	if strings.HasPrefix(osType, "win") || strings.HasPrefix(osType, "w2k") {
		return "windows"
	}
	if osType == "l26" || strings.HasPrefix(osType, "linux") {
		return "linux"
	}
	return "unknown"
}

func desktopAccessPolicyCommand(osFamily, privilegeMode string) ([]string, error) {
	return desktopAccessPolicyCommandForUser(osFamily, privilegeMode, "vdi")
}

func desktopSessionPolicyCommand(osFamily string, policy store.DesktopAccessPolicy) ([]string, error) {
	clipboard := "false"
	clipboardRestriction := "all"
	if policy.ClipboardRedirection {
		clipboard = "true"
		clipboardRestriction = "none"
	}
	drive := "false"
	if policy.DriveRedirection {
		drive = "true"
	}
	backgroundMode := "allow-user"
	if policy.ManagedBackground {
		backgroundMode = "managed"
	}
	switch osFamily {
	case "linux":
		script := fmt.Sprintf(`set -eu
xrdp_ini=/etc/xrdp/xrdp.ini
sesman_ini=/etc/xrdp/sesman.ini
[ -r "$xrdp_ini" ] && [ -r "$sesman_ini" ]
before=$(sha256sum "$xrdp_ini" "$sesman_ini")
set_ini_key() {
  file=$1
  section=$2
  key=$3
  value=$4
  temporary=$(mktemp "${file}.vcw.XXXXXX")
  if ! awk -v section="$section" -v key="$key" -v value="$value" '
    BEGIN { wanted="[" section "]"; inside=0; seen=0; written=0 }
    function emit_missing() { if (inside && !written) { print key "=" value; written=1 } }
    /^[[:space:]]*\[[^]]+\][[:space:]]*$/ {
      emit_missing()
      inside=($0 == wanted)
      if (inside) seen=1
      print
      next
    }
    {
      if (inside && $0 ~ "^[[:space:]]*" key "[[:space:]]*=") {
        if (!written) print key "=" value
        written=1
        next
      }
      print
    }
    END { emit_missing(); if (!seen) exit 45 }
  ' "$file" >"$temporary"; then
    rm -f "$temporary"
    echo "required xrdp policy section is missing: $section" >&2
    exit 44
  fi
  chmod --reference="$file" "$temporary"
  chown --reference="$file" "$temporary"
  mv "$temporary" "$file"
}
set_ini_key "$xrdp_ini" Channels cliprdr %s
set_ini_key "$xrdp_ini" Channels rdpdr %s
set_ini_key "$sesman_ini" Security RestrictInboundClipboard %s
set_ini_key "$sesman_ini" Security RestrictOutboundClipboard %s
set_ini_key "$sesman_ini" Chansrv EnableFuseMount %s
install -d -m 0755 /etc/vc-workspace
marker=$(mktemp /etc/vc-workspace/background-policy.XXXXXX)
printf '%%s\n' %s >"$marker"
chmod 0644 "$marker"
mv "$marker" /etc/vc-workspace/background-policy
if [ %s = managed ]; then
  install -d -m 0755 /usr/share/backgrounds/vc-workspace /usr/local/bin /etc/xdg/autostart
  if [ ! -r /usr/share/backgrounds/vc-workspace/desktop-background.png ]; then
    install -m 0644 /tmp/vc-workspace-desktop-background-session.jpg /usr/share/backgrounds/vc-workspace/desktop-background-session.jpg
  fi
  cat >/usr/local/bin/vc-workspace-apply-background <<'VCW_BACKGROUND'
#!/usr/bin/env bash
set -u
[ "$(cat /etc/vc-workspace/background-policy 2>/dev/null || true)" = managed ] || exit 0
if [ -r /usr/share/backgrounds/vc-workspace/desktop-background.png ]; then
  wallpaper=/usr/share/backgrounds/vc-workspace/desktop-background.png
else
  wallpaper=/usr/share/backgrounds/vc-workspace/desktop-background-session.jpg
fi
command -v xfconf-query >/dev/null 2>&1 || exit 0
sleep 1
mapfile -t image_properties < <(xfconf-query -c xfce4-desktop -l 2>/dev/null | grep -E '/(last-image|image-path)$' || true)
if [ "${#image_properties[@]}" -eq 0 ]; then
  image_properties=(/backdrop/screen0/monitorrdp0/workspace0/last-image)
fi
for property in "${image_properties[@]}"; do
  xfconf-query -c xfce4-desktop -p "$property" --create -t string -s "$wallpaper" >/dev/null 2>&1 || true
  style_property="${property%%/*}/image-style"
  xfconf-query -c xfce4-desktop -p "$style_property" --create -t int -s 5 >/dev/null 2>&1 || true
done
VCW_BACKGROUND
  chmod 0755 /usr/local/bin/vc-workspace-apply-background
  cat >/etc/xdg/autostart/vc-workspace-background.desktop <<'VCW_BACKGROUND_DESKTOP'
[Desktop Entry]
Type=Application
Name=VC Workspace Background
Exec=/usr/local/bin/vc-workspace-apply-background
OnlyShowIn=XFCE;
NoDisplay=true
X-GNOME-Autostart-enabled=true
VCW_BACKGROUND_DESKTOP
fi
rm -f /tmp/vc-workspace-desktop-background-session.jpg
after=$(sha256sum "$xrdp_ini" "$sesman_ini")
if [ "$before" != "$after" ]; then
  systemctl restart xrdp-sesman.service xrdp.service
fi
grep -Eq '^cliprdr=%s$' "$xrdp_ini"
grep -Eq '^rdpdr=%s$' "$xrdp_ini"`, clipboard, drive, clipboardRestriction, clipboardRestriction, drive, backgroundMode, backgroundMode, clipboard, drive)
		return []string{"/bin/sh", "-c", script}, nil
	case "windows":
		disableClipboard := 1
		if policy.ClipboardRedirection {
			disableClipboard = 0
		}
		disableDrive := 1
		if policy.DriveRedirection {
			disableDrive = 0
		}
		managed := "$false"
		if policy.ManagedBackground {
			managed = "$true"
		}
		script := fmt.Sprintf(`$ErrorActionPreference='Stop';
$terminalServices='HKLM:\SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services';
if (-not (Test-Path -LiteralPath $terminalServices)) { New-Item -Path $terminalServices -Force | Out-Null };
New-ItemProperty -Path $terminalServices -Name 'fDisableClip' -PropertyType DWord -Value %d -Force | Out-Null;
New-ItemProperty -Path $terminalServices -Name 'fDisableCdm' -PropertyType DWord -Value %d -Force | Out-Null;
$brand='C:\ProgramData\VC Workspace\Brand';
New-Item -ItemType Directory -Path $brand -Force | Out-Null;
Set-Content -LiteralPath (Join-Path $brand 'background-policy') -Value '%s' -Encoding Ascii;
if (%s) {
  $backgroundPNG=Join-Path $brand 'desktop-background.png';
  $backgroundJPEG=Join-Path $brand 'desktop-background-session.jpg';
  if (-not (Test-Path -LiteralPath $backgroundPNG)) {
    Copy-Item -LiteralPath 'C:\Windows\Temp\vc-workspace-desktop-background-session.jpg' -Destination $backgroundJPEG -Force;
  };
  $backgroundHelper=Join-Path $brand 'Apply-DesktopBackground.ps1';
  @'
$ErrorActionPreference = 'SilentlyContinue'
$brand = 'C:\ProgramData\VC Workspace\Brand'
$policy = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Policies\System'
$mode = (Get-Content -LiteralPath (Join-Path $brand 'background-policy') -Raw).Trim()
if ($mode -eq 'managed') {
    $background = Join-Path $brand 'desktop-background.png'
    if (-not (Test-Path -LiteralPath $background)) { $background = Join-Path $brand 'desktop-background-session.jpg' }
    if (-not (Test-Path -LiteralPath $policy)) { New-Item -Path $policy -Force | Out-Null }
    Set-ItemProperty -Path $policy -Name Wallpaper -Value $background -Type String
    Set-ItemProperty -Path $policy -Name WallpaperStyle -Value '10' -Type String
    Set-ItemProperty -Path $policy -Name TileWallpaper -Value '0' -Type String
} elseif (Test-Path -LiteralPath $policy) {
    Remove-ItemProperty -Path $policy -Name Wallpaper,WallpaperStyle,TileWallpaper -ErrorAction SilentlyContinue
}
& "$env:SystemRoot\System32\RUNDLL32.EXE" user32.dll,UpdatePerUserSystemParameters 1, True
'@ | Set-Content -LiteralPath $backgroundHelper -Encoding UTF8;
  $runKey='HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run';
  New-ItemProperty -Path $runKey -Name 'VCWorkspaceBackground' -PropertyType String -Value ('powershell.exe -NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "{0}"' -f $backgroundHelper) -Force | Out-Null;
};
Remove-Item -LiteralPath 'C:\Windows\Temp\vc-workspace-desktop-background-session.jpg' -Force -ErrorAction SilentlyContinue;
& gpupdate.exe /target:computer /force | Out-Null;
if ((Get-ItemPropertyValue -Path $terminalServices -Name 'fDisableClip') -ne %d) { throw 'clipboard policy was not applied' };
if ((Get-ItemPropertyValue -Path $terminalServices -Name 'fDisableCdm') -ne %d) { throw 'drive policy was not applied' };`, disableClipboard, disableDrive, backgroundMode, managed, disableClipboard, disableDrive)
		return []string{"powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}, nil
	default:
		return nil, errors.New("desktop session policy OS is unsupported")
	}
}

func desktopAccessPolicyCommandForUser(osFamily, privilegeMode, username string) ([]string, error) {
	if privilegeMode != "standard" && privilegeMode != "local_admin" {
		return nil, errors.New("desktop privilege mode is invalid")
	}
	if !guestUsernamePattern.MatchString(username) {
		return nil, errors.New("guest username is invalid")
	}
	switch osFamily {
	case "linux":
		if privilegeMode == "local_admin" {
			script := fmt.Sprintf(`set -eu
username=%s
getent passwd "$username" >/dev/null
getent group sudo >/dev/null
policy_file="/etc/sudoers.d/vc-workspace-$username"
policy_line="$username ALL=(ALL:ALL) NOPASSWD: ALL"
changed=0
if ! id -nG "$username" | tr ' ' '\n' | grep -qx sudo; then
  usermod -a -G sudo "$username"
  changed=1
fi
if [ ! -f "$policy_file" ] || [ "$(cat "$policy_file")" != "$policy_line" ]; then
  printf '%%s\n' "$policy_line" >"$policy_file.tmp"
  chmod 0440 "$policy_file.tmp"
  visudo -cf "$policy_file.tmp" >/dev/null
  mv "$policy_file.tmp" "$policy_file"
  changed=1
fi
if [ "$changed" = 1 ]; then
  loginctl terminate-user "$username" >/dev/null 2>&1 || true
  pkill -KILL -u "$username" >/dev/null 2>&1 || true
fi
id -nG "$username" | tr ' ' '\n' | grep -qx sudo
runuser -u "$username" -- sudo -n /usr/bin/true`, username)
			return []string{"/bin/sh", "-c", script}, nil
		}
		script := fmt.Sprintf(`set -eu
username=%s
getent passwd "$username" >/dev/null
changed=0
if id -nG "$username" | tr ' ' '\n' | grep -Eq '^(sudo|admin)$'; then changed=1; fi
if [ -e "/etc/sudoers.d/vc-workspace-$username" ]; then changed=1; fi
if getent group sudo >/dev/null; then gpasswd -d "$username" sudo >/dev/null 2>&1 || true; fi
if getent group admin >/dev/null; then gpasswd -d "$username" admin >/dev/null 2>&1 || true; fi
rm -f "/etc/sudoers.d/vc-workspace-$username" "/etc/sudoers.d/vc-workspace-$username.tmp"
if [ "$changed" = 1 ]; then
  loginctl terminate-user "$username" >/dev/null 2>&1 || true
  pkill -KILL -u "$username" >/dev/null 2>&1 || true
fi
if id -nG "$username" | tr ' ' '\n' | grep -Eq '^(sudo|admin)$'; then exit 42; fi
if runuser -u "$username" -- sudo -n /usr/bin/true >/dev/null 2>&1; then exit 43; fi`, username)
		return []string{"/bin/sh", "-c", script}, nil
	case "windows":
		operation := `if (-not $isMember) { Add-LocalGroupMember -Group $administrators -Member $user.Name; try { Get-Process -IncludeUserName | Where-Object { $_.UserName -match ('\\'+[regex]::Escape($name)+'$') } | Stop-Process -Force -ErrorAction SilentlyContinue } catch {} }; if (-not (Get-LocalGroupMember -Group $administrators | Where-Object { $_.SID -eq $user.SID })) { throw 'administrator membership was not applied' }`
		if privilegeMode == "standard" {
			operation = `if ($isMember) { Remove-LocalGroupMember -Group $administrators -Member $user.Name; try { Get-Process -IncludeUserName | Where-Object { $_.UserName -match ('\\'+[regex]::Escape($name)+'$') } | Stop-Process -Force -ErrorAction SilentlyContinue } catch {} }; if (Get-LocalGroupMember -Group $administrators | Where-Object { $_.SID -eq $user.SID }) { throw 'administrator membership was not removed' }`
		}
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $name='%s'; $user=Get-LocalUser -Name $name; $administrators=(Get-LocalGroup -SID 'S-1-5-32-544').Name; $isMember=Get-LocalGroupMember -Group $administrators | Where-Object { $_.SID -eq $user.SID }; `, username) + operation
		return []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}, nil
	default:
		return nil, errors.New("desktop operating system is not supported")
	}
}

func (s *Server) authenticateAgent(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
		return
	}
	token := strings.TrimSpace(r.Header.Get("X-VC-Workspace-Agent-Token"))
	if token == "" || len(token) > 512 {
		writeError(w, http.StatusUnauthorized, "invalid_agent_token", "Agent authentication failed")
		return
	}
	agent, err := s.store.AgentByTokenDigest(r.Context(), auth.TokenDigest(token))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.logger.Error("authenticate AI agent", "error", err)
		}
		writeError(w, http.StatusUnauthorized, "invalid_agent_token", "Agent authentication failed")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) agentDesktops(w http.ResponseWriter, r *http.Request) {
	if s.store == nil || s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if !agentIDPattern.MatchString(agentID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "Agent ID is invalid")
		return
	}
	summary, err := s.desktopInventory(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to read PVE infrastructure")
		return
	}
	vmids, err := s.store.AgentAssignedDesktopVMIDs(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read desktop assignments")
		return
	}
	desktops := filterDesktopsByVMID(summary.VMs, vmids)
	writeJSON(w, http.StatusOK, map[string]any{"desktops": desktops})
}

var agentIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{2,127}$`)

func (s *Server) createAgentDesktopLease(w http.ResponseWriter, r *http.Request) {
	if s.store == nil || s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "agent_api_unavailable", "Agent API dependencies are unavailable")
		return
	}
	var request struct {
		AgentID     string `json:"agent_id"`
		DesktopVMID int    `json:"desktop_vmid"`
		TTLSeconds  int    `json:"ttl_seconds"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	if !agentIDPattern.MatchString(request.AgentID) || request.DesktopVMID <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_lease", "Agent ID or desktop VMID is invalid")
		return
	}
	if request.TTLSeconds == 0 {
		request.TTLSeconds = 900
	}
	if request.TTLSeconds < 60 || request.TTLSeconds > 3600 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_ttl", "Lease TTL must be between 60 and 3600 seconds")
		return
	}
	allowed, err := s.store.AgentCanAccessDesktop(r.Context(), request.AgentID, request.DesktopVMID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to verify desktop assignment")
		return
	}
	if !allowed {
		s.auditRequest(r, request.AgentID, "agent.desktop_access_denied", "agent", request.AgentID, map[string]any{"desktop_vmid": request.DesktopVMID, "reason": "not_assigned"})
		writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		return
	}
	if _, err := s.managedVirtualMachine(r.Context(), request.DesktopVMID); err != nil {
		writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		return
	}
	leaseToken, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create lease")
		return
	}
	unlock, err := s.store.AcquireDesktopControlLock(r.Context(), request.DesktopVMID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "desktop_busy", "Desktop control is temporarily unavailable")
		return
	}
	defer unlock()
	lease, err := s.store.CreateDesktopLease(r.Context(), store.DesktopLease{
		ID: "lease_" + leaseToken, AgentID: request.AgentID, DesktopID: strconv.Itoa(request.DesktopVMID),
		ExpiresAt: time.Now().Add(time.Duration(request.TTLSeconds) * time.Second),
	})
	if errors.Is(err, store.ErrAlreadyLeased) {
		writeError(w, http.StatusConflict, "desktop_already_leased", "Desktop is already controlled by a user or agent")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create lease")
		return
	}
	s.auditRequest(r, lease.AgentID, "agent.desktop_lease_created", "desktop_lease", lease.ID, map[string]any{"agent_id": lease.AgentID, "desktop_id": lease.DesktopID})
	writeJSON(w, http.StatusCreated, lease)
}

func (s *Server) agentDesktopLease(w http.ResponseWriter, r *http.Request) {
	lease, ok := s.requireAgentLease(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) releaseAgentDesktopLease(w http.ResponseWriter, r *http.Request) {
	lease, ok := s.requireAgentLease(w, r)
	if !ok {
		return
	}
	vmid, err := strconv.Atoi(lease.DesktopID)
	if err != nil {
		writeError(w, http.StatusConflict, "invalid_lease", "Desktop lease is invalid")
		return
	}
	unlock, err := s.store.AcquireDesktopControlLock(r.Context(), vmid)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "desktop_busy", "Desktop control is temporarily unavailable")
		return
	}
	defer unlock()
	lease, ok = s.requireAgentLease(w, r)
	if !ok {
		return
	}
	released, err := s.store.ReleaseDesktopLease(r.Context(), lease.ID)
	if err != nil {
		writeError(w, http.StatusConflict, "lease_not_active", "Desktop lease is no longer active")
		return
	}
	if vmid, conversionErr := strconv.Atoi(released.DesktopID); conversionErr == nil {
		if machine, machineErr := s.managedVirtualMachine(r.Context(), vmid); machineErr == nil && machine.Status == "running" {
			_ = s.synchronizeComputerRevocation(r.Context(), machine, false)
		}
	}
	s.auditRequest(r, lease.AgentID, "agent.desktop_lease_released", "desktop_lease", lease.ID, map[string]any{"agent_id": lease.AgentID, "desktop_id": lease.DesktopID})
	writeJSON(w, http.StatusOK, released)
}

func (s *Server) changeAgentDesktopPower(w http.ResponseWriter, r *http.Request) {
	lease, ok := s.requireAgentLease(w, r)
	if !ok {
		return
	}
	action := r.PathValue("action")
	if action != "start" && action != "stop" {
		writeError(w, http.StatusBadRequest, "invalid_power_request", "Power action must be start or stop")
		return
	}
	vmid, err := strconv.Atoi(lease.DesktopID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid_lease", "Lease desktop is invalid")
		return
	}
	idempotencyKey, fingerprint, proceed := s.prepareJobRequest(w, r, "agent:"+lease.AgentID)
	if !proceed {
		return
	}
	machine, err := s.managedVirtualMachine(r.Context(), vmid)
	if err != nil {
		writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		return
	}
	if (action == "start" && machine.Status == "running") || (action == "stop" && machine.Status == "stopped") {
		writeError(w, http.StatusConflict, "power_state_conflict", "Virtual machine is already in the requested power state")
		return
	}
	requestJSON, _ := json.Marshal(map[string]any{"lease_id": lease.ID, "agent_id": lease.AgentID, "vmid": vmid, "action": action})
	jobToken, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create job")
		return
	}
	job, created, err := s.store.CreateJob(r.Context(), store.Job{
		ID: "job_" + jobToken, IdempotencyKey: idempotencyKey, RequestFingerprint: fingerprint, Operation: "agent.desktop_" + action, State: "accepted",
		TargetVMID: vmid, TargetNode: machine.Node, TaskNode: machine.Node, Request: requestJSON,
	})
	if errors.Is(err, store.ErrConflict) {
		writeJobConflict(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create job")
		return
	}
	if !created {
		writeJSON(w, http.StatusAccepted, job)
		return
	}
	upid, err := s.pve.ChangePowerState(r.Context(), machine.Node, vmid, action)
	if err != nil {
		_ = s.store.UpdateJobTask(r.Context(), job.ID, "failed", "", err.Error())
		writeError(w, http.StatusBadGateway, "pve_power_failed", "PVE rejected the power request")
		return
	}
	_ = s.store.UpdateJobTask(r.Context(), job.ID, "running", upid, "")
	s.auditRequest(r, lease.AgentID, "agent.desktop_"+action+"_requested", "desktop_lease", lease.ID, map[string]any{"agent_id": lease.AgentID, "vmid": vmid})
	job.State, job.UPID = "running", upid
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) agentComputerAction(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), computer.APIActionTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	lease, ok := s.requireAgentLease(w, r)
	if !ok {
		s.auditRequest(r, strings.TrimSpace(r.URL.Query().Get("agent_id")), "agent.computer_action_denied", "desktop_lease", r.PathValue("id"), map[string]any{
			"reason": "lease_unavailable",
		})
		return
	}
	var input computerActionRequest
	auditDenied := func(reason string, extra map[string]any) {
		detail := map[string]any{
			"agent_id": lease.AgentID, "desktop_id": lease.DesktopID,
			"operation": input.Operation, "reason": reason,
		}
		for key, value := range extra {
			detail[key] = value
		}
		s.auditRequest(r, lease.AgentID, "agent.computer_action_denied", "desktop_lease", lease.ID, detail)
	}
	if err := decodeJSON(r, &input); err != nil {
		auditDenied("invalid_request", nil)
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if input.ControlEpoch != lease.ControlEpoch {
		auditDenied("stale_control_epoch", map[string]any{
			"provided_control_epoch": input.ControlEpoch, "current_control_epoch": lease.ControlEpoch,
		})
		writeError(w, http.StatusConflict, "stale_control_epoch", "Desktop control authority has changed")
		return
	}
	if s.computer == nil {
		auditDenied("computer_use_unavailable", nil)
		writeError(w, http.StatusServiceUnavailable, "computer_use_unavailable", "Computer Use is not configured")
		return
	}
	if input.TimeoutMS == 0 {
		input.TimeoutMS = 5000
	}
	if input.TimeoutMS < 250 || input.TimeoutMS > 15000 {
		auditDenied("invalid_timeout", nil)
		writeError(w, http.StatusUnprocessableEntity, "invalid_timeout", "Computer action timeout must be 250-15000 milliseconds")
		return
	}
	actionToken, err := auth.OpaqueToken(18)
	if err != nil {
		s.auditRequest(r, lease.AgentID, "agent.computer_action_failed", "desktop_lease", lease.ID, map[string]any{
			"agent_id": lease.AgentID, "desktop_id": lease.DesktopID, "operation": input.Operation, "reason": "request_id_failed",
		})
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create Computer Use action")
		return
	}
	timeout := time.Duration(input.TimeoutMS) * time.Millisecond
	action := computer.Request{
		SchemaVersion: computer.SchemaVersion,
		RequestID:     "action_" + actionToken,
		LeaseID:       lease.ID,
		ControlEpoch:  lease.ControlEpoch,
		ExpiresUnixMS: time.Now().Add(timeout).UnixMilli(),
		Operation:     input.Operation,
		Screenshot:    input.Screenshot,
		Accessibility: input.Accessibility,
		Mouse:         input.Mouse,
		Key:           input.Key,
		Text:          input.Text,
	}
	action.ApplyDefaults()
	if err := action.Validate(time.Now()); err != nil {
		auditDenied("invalid_computer_action", nil)
		writeError(w, http.StatusUnprocessableEntity, "invalid_computer_action", err.Error())
		return
	}
	vmid, err := strconv.Atoi(lease.DesktopID)
	if err != nil {
		auditDenied("invalid_lease", nil)
		writeError(w, http.StatusInternalServerError, "invalid_lease", "Lease desktop is invalid")
		return
	}
	machine, err := s.managedVirtualMachine(r.Context(), vmid)
	if err != nil {
		auditDenied("managed_vm_not_found", nil)
		writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		return
	}
	if machine.Status != "running" {
		auditDenied("desktop_not_running", nil)
		writeError(w, http.StatusConflict, "desktop_not_running", "Desktop must be running before Computer Use actions are sent")
		return
	}
	unlockComputer, err := s.store.AcquireDesktopControlLock(r.Context(), machine.VMID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "desktop_busy", "Desktop control is temporarily unavailable")
		return
	}
	defer unlockComputer()
	currentLease, ok := s.requireAgentLease(w, r)
	if !ok {
		auditDenied("lease_changed_while_waiting_for_control", nil)
		return
	}
	if currentLease.ControlEpoch != action.ControlEpoch {
		writeError(w, http.StatusConflict, "stale_control_epoch", "Desktop control authority has changed")
		return
	}
	if err := s.synchronizeComputerRevocation(r.Context(), machine, false); err != nil {
		writeError(w, http.StatusServiceUnavailable, "computer_revocation_pending", "Desktop control revocation is still being synchronized")
		return
	}
	target, err := s.prepareAgentGuestSession(r.Context(), machine, lease)
	if err != nil {
		s.auditRequest(r, lease.AgentID, "agent.computer_action_failed", "desktop_lease", lease.ID, map[string]any{
			"agent_id": lease.AgentID, "desktop_id": lease.DesktopID, "operation": input.Operation, "reason": "authority_sync_failed",
		})
		writeError(w, http.StatusServiceUnavailable, "computer_helper_unavailable", "Desktop Computer Use helper is unavailable")
		return
	}
	currentLease, currentErr := s.store.DesktopLeaseByID(r.Context(), lease.ID)
	if currentErr != nil || currentLease.State != "active" || currentLease.ControlEpoch != lease.ControlEpoch {
		if currentErr == nil {
			_ = s.synchronizeComputerRevocation(r.Context(), machine, true)
		}
		auditDenied("lease_changed_during_authority_sync", nil)
		writeError(w, http.StatusConflict, "stale_control_epoch", "Desktop control authority has changed")
		return
	}
	detail := action.AuditDetail()
	detail["agent_id"] = lease.AgentID
	detail["desktop_id"] = lease.DesktopID
	detail["request_id"] = action.RequestID
	// Queue/gate synchronization is not time spent inside the action worker.
	action.ExpiresUnixMS = time.Now().Add(timeout).UnixMilli()
	// Never dispatch to vdi or a foreground session when a Guest is not upgraded.
	result, err := s.computer.(computer.SessionExecutor).ExecuteForSession(r.Context(), machine, target, action, timeout)
	if err != nil {
		detail["reason"] = computerErrorReason(err)
		s.auditRequest(r, lease.AgentID, "agent.computer_action_failed", "desktop_lease", lease.ID, detail)
		switch {
		case errors.Is(err, computer.ErrInvalid):
			writeError(w, http.StatusUnprocessableEntity, "invalid_computer_action", "Computer Use action is invalid")
		case errors.Is(err, computer.ErrTimeout):
			writeError(w, http.StatusGatewayTimeout, "computer_action_timeout", "Computer Use action timed out")
		case errors.Is(err, computer.ErrUnavailable):
			writeError(w, http.StatusServiceUnavailable, "computer_helper_unavailable", "Desktop Computer Use helper is unavailable")
		default:
			writeError(w, http.StatusBadGateway, "computer_action_failed", "Desktop rejected the Computer Use action")
		}
		return
	}
	// Revocation may commit while the Guest worker is executing. Never return
	// screenshots or accessibility data after the caller loses authorization.
	if _, ok := s.requireAgentLease(w, r); !ok {
		auditDenied("lease_changed_after_action", nil)
		return
	}
	if result.Screenshot != nil {
		detail["image_width"] = result.Screenshot.Width
		detail["image_height"] = result.Screenshot.Height
		detail["image_sha256"] = result.Screenshot.SHA256
	}
	if result.Accessibility != nil {
		detail["accessibility_source"] = result.Accessibility.Source
		detail["node_count"] = len(result.Accessibility.Nodes)
		detail["truncated"] = result.Accessibility.Truncated
	}
	s.auditRequest(r, lease.AgentID, "agent.computer_action_succeeded", "desktop_lease", lease.ID, detail)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func computerErrorReason(err error) string {
	switch {
	case errors.Is(err, computer.ErrInvalid):
		return "invalid_request"
	case errors.Is(err, computer.ErrTimeout):
		return "timeout"
	case errors.Is(err, computer.ErrUnavailable):
		return "helper_unavailable"
	default:
		return "guest_rejected"
	}
}

// revokeAllComputerAuthority writes a denial tombstone even when the database
// has no active lease left. This makes a failed human-takeover attempt safely
// retryable: the next attempt cannot skip Guest revocation merely because the
// first attempt already changed the database state to revoked.
func (s *Server) revokeAllComputerAuthority(ctx context.Context, machine pve.VM) error {
	controller, ok := s.computer.(computer.AuthorityController)
	if !ok {
		return nil
	}
	if s.store == nil {
		return computer.ErrUnavailable
	}
	cancelContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	epoch, err := s.store.ComputerControlEpoch(cancelContext, machine.VMID)
	if err != nil {
		return err
	}
	err = controller.RevokeAuthority(cancelContext, machine, computer.Authority{
		SchemaVersion: computer.SchemaVersion,
		LeaseID:       "lease_human_takeover_tombstone",
		ControlEpoch:  epoch,
		State:         "revoked",
		ExpiresUnixMS: time.Now().UnixMilli(),
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		s.logger.Warn("revoke all guest computer authority", "vmid", machine.VMID, "error", err)
	}
	return err
}

func (s *Server) requireAgentLease(w http.ResponseWriter, r *http.Request) (store.DesktopLease, bool) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Database is unavailable")
		return store.DesktopLease{}, false
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	lease, err := s.store.DesktopLeaseByID(r.Context(), r.PathValue("id"))
	if err != nil || lease.AgentID != agentID {
		writeError(w, http.StatusNotFound, "lease_not_found", "Desktop lease was not found")
		return store.DesktopLease{}, false
	}
	if lease.State != "active" {
		writeError(w, http.StatusConflict, "lease_not_active", "Desktop lease is no longer active")
		return store.DesktopLease{}, false
	}
	vmid, err := strconv.Atoi(lease.DesktopID)
	if err != nil {
		writeError(w, http.StatusNotFound, "lease_not_found", "Desktop lease was not found")
		return store.DesktopLease{}, false
	}
	allowed, err := s.store.AgentCanAccessDesktop(r.Context(), agentID, vmid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to verify desktop assignment")
		return store.DesktopLease{}, false
	}
	if !allowed {
		writeError(w, http.StatusNotFound, "lease_not_found", "Desktop lease was not found")
		return store.DesktopLease{}, false
	}
	return lease, true
}

func (s *Server) requireUserDesktopAccess(w http.ResponseWriter, r *http.Request, user store.User, vmid int) bool {
	allowed, err := s.store.UserCanAccessDesktop(r.Context(), user.ID, vmid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to verify desktop assignment")
		return false
	}
	if !allowed {
		s.auditRequest(r, user.ID, "desktop.access_denied", "virtual_machine", strconv.Itoa(vmid), map[string]any{"reason": "not_assigned"})
		writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		return false
	}
	return true
}

func (s *Server) managedVirtualMachine(ctx context.Context, vmid int) (pve.VM, error) {
	if s.pve == nil {
		return pve.VM{}, store.ErrNotFound
	}
	summary, err := s.desktopInventory(ctx)
	if err != nil {
		return pve.VM{}, err
	}
	for _, machine := range summary.VMs {
		if machine.VMID == vmid && machine.Managed {
			return machine, nil
		}
	}
	return pve.VM{}, store.ErrNotFound
}

func isManagedDesktop(machine pve.VM) bool {
	return machine.Kind == "qemu" && !machine.Template &&
		(containsString(machine.Tags, "vc-workspace") || containsString(machine.Tags, "vc-vdi"))
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON body")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain one JSON object")
	}
	return nil
}

func (s *Server) auditRequest(r *http.Request, actorID, eventType, targetType, targetID string, detail map[string]any) {
	if s.store == nil {
		return
	}
	enriched := make(map[string]any, len(detail)+2)
	for key, value := range detail {
		enriched[key] = value
	}
	if sourceIP := requestSourceIP(r); sourceIP != "" {
		enriched["source_ip"] = sourceIP
	}
	if userAgent := strings.Join(strings.Fields(r.UserAgent()), " "); userAgent != "" {
		runes := []rune(userAgent)
		if len(runes) > 256 {
			userAgent = string(runes[:256]) + "…"
		}
		enriched["user_agent"] = userAgent
	}
	if err := s.store.Audit(r.Context(), actorID, eventType, targetType, targetID, enriched); err != nil {
		s.logger.Warn("write audit event", "event_type", eventType, "error", err)
	}
}

func requestSourceIP(r *http.Request) string {
	remote := strings.TrimSpace(r.RemoteAddr)
	if address, err := netip.ParseAddrPort(remote); err == nil {
		return address.Addr().Unmap().String()
	}
	if address, err := netip.ParseAddr(remote); err == nil {
		return address.Unmap().String()
	}
	return ""
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("HTTP request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				s.logger.Error("HTTP handler panicked", "panic", value, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal_error", "Internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
