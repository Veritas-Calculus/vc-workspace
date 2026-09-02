package httpapi

import (
	"context"
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

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
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
	readiness         ReadinessChecker
	cookieSecure      bool
	startedAt         time.Time
}

type desktopCloneRequest struct {
	Name               string `json:"name"`
	SourceVMID         int    `json:"source_vmid"`
	TargetNode         string `json:"target_node"`
	Storage            string `json:"storage"`
	Full               bool   `json:"full"`
	GPUProfileID       string `json:"gpu_profile_id"`
	PCIResourceMapping string `json:"pci_resource_mapping,omitempty"`
	MDevType           string `json:"mdev_type,omitempty"`
}

type pciResourceMappingRequest struct {
	ID          string                           `json:"id"`
	Description string                           `json:"description"`
	MDev        bool                             `json:"mdev"`
	Entries     []pciResourceMappingEntryRequest `json:"entries"`
}

type desktopAccessPolicyRequest struct {
	PrivilegeMode string `json:"privilege_mode"`
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
	return &Server{
		logger: logger, pve: deps.PVE, store: deps.Store, publicURL: deps.PublicURL,
		setupToken: deps.SetupToken, oidc: deps.OIDC, internalAPIToken: deps.InternalAPIToken, nativeCallbackURL: deps.NativeCallbackURL,
		imageBuilder: deps.ImageBuilder, readiness: readiness,
		cookieSecure: publicURL != nil && publicURL.Scheme == "https", startedAt: time.Now(),
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
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireAuth(s.logout))
	mux.HandleFunc("GET /api/v1/me", s.requireAuth(s.me))
	mux.HandleFunc("GET /api/v1/infrastructure", s.requireAuth(s.infrastructure))
	mux.HandleFunc("GET /api/v1/platform-config", s.requireAuth(s.platformConfig))
	mux.HandleFunc("GET /api/v1/access-control", s.requireAuth(s.accessControl))
	mux.HandleFunc("POST /api/v1/access-control/reconcile", s.requireAuth(s.reconcileAccessControl))
	mux.HandleFunc("POST /api/v1/users", s.requireAuth(s.createLocalUser))
	mux.HandleFunc("PATCH /api/v1/users/{id}", s.requireAuth(s.updateUser))
	mux.HandleFunc("POST /api/v1/agents", s.requireAuth(s.createAgentPrincipal))
	mux.HandleFunc("PATCH /api/v1/agents/{id}", s.requireAuth(s.updateAgentPrincipal))
	mux.HandleFunc("POST /api/v1/agents/{id}/token", s.requireAuth(s.rotateAgentToken))
	mux.HandleFunc("PUT /api/v1/desktop-assignments/{subject_type}/{subject_id}/{vmid}", s.requireAuth(s.putDesktopAssignment))
	mux.HandleFunc("DELETE /api/v1/desktop-assignments/{subject_type}/{subject_id}/{vmid}", s.requireAuth(s.deleteDesktopAssignment))
	mux.HandleFunc("PATCH /api/v1/image-profiles/{id}", s.requireAuth(s.updateImageProfile))
	mux.HandleFunc("POST /api/v1/image-profiles/{id}/builds", s.requireAuth(s.startImageBuild))
	mux.HandleFunc("PATCH /api/v1/gpu-profiles/{id}", s.requireAuth(s.updateGPUProfile))
	mux.HandleFunc("POST /api/v1/pci-resource-mappings", s.requireAuth(s.createPCIResourceMapping))
	mux.HandleFunc("PUT /api/v1/pci-resource-mappings/{id}", s.requireAuth(s.updatePCIResourceMapping))
	mux.HandleFunc("POST /api/v1/desktop-instances", s.requireAuth(s.createDesktopInstance))
	mux.HandleFunc("GET /api/v1/jobs", s.requireAuth(s.jobs))
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.requireAuth(s.job))
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
	s.auditRequest(r, user.ID, "auth.login_succeeded", "user", user.ID, map[string]any{"client": "web"})
	s.startSession(w, r, user)
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
		Name: "vc_vdi_oidc_state", Value: state, Path: "/api/v1/auth/oidc/callback", MaxAge: int(lifetime.Seconds()),
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
	stateCookie, err := r.Cookie("vc_vdi_oidc_state")
	if err != nil || state == "" || code == "" || !auth.ConstantTimeEqual(stateCookie.Value, state) {
		s.auditRequest(r, "", "auth.oidc_login_failed", "", "", map[string]any{"stage": "callback_validation"})
		writeError(w, http.StatusBadRequest, "invalid_oidc_callback", "Single sign-on response is invalid or expired")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "vc_vdi_oidc_state", Value: "", Path: "/api/v1/auth/oidc/callback", MaxAge: -1, HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode})
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
	s.auditRequest(r, user.ID, "auth.native_login_succeeded", "user", user.ID, map[string]any{"client": "macos"})
	s.issueNativeSession(w, r, user)
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
	s.auditRequest(r, user.ID, "auth.native_oidc_login_succeeded", "user", user.ID, map[string]any{"client": "macos"})
	s.issueNativeSession(w, r, user)
}

func (s *Server) issueNativeSession(w http.ResponseWriter, r *http.Request, user store.User) {
	token, err := auth.OpaqueToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create native session")
		return
	}
	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	if err := s.store.CreateNativeSession(r.Context(), auth.TokenDigest(token), user.ID, expiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create native session")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"access_token": token, "token_type": "Bearer", "expires_at": expiresAt, "user": publicUser(user),
	})
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
	if err := s.store.DeleteNativeSession(r.Context(), auth.TokenDigest(token)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to end native session")
		return
	}
	s.auditRequest(r, user.ID, "auth.native_logout_succeeded", "user", user.ID, map[string]any{"client": "macos"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) nativeDesktops(w http.ResponseWriter, r *http.Request, user store.User, _ string) {
	if s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return
	}
	summary, err := s.pve.Summary(r.Context())
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
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 8–128 characters")
		return
	}
	if existing, err := s.store.JobByIdempotencyKey(r.Context(), idempotencyKey); err == nil {
		writeJSON(w, http.StatusAccepted, existing)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to inspect existing request")
		return
	}
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	action := r.PathValue("action")
	if err != nil || vmid <= 0 || (action != "start" && action != "stop") {
		writeError(w, http.StatusBadRequest, "invalid_power_request", "Power request is invalid")
		return
	}
	if !s.requireUserDesktopAccess(w, r, user, vmid) {
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
		ID: "job_" + jobToken, IdempotencyKey: idempotencyKey, Operation: "native.desktop_" + action, State: "accepted",
		TargetVMID: vmid, TargetNode: machine.Node, TaskNode: machine.Node, Request: requestJSON, CreatedBy: user.ID,
	})
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

func (s *Server) createNativeDesktopConnection(w http.ResponseWriter, r *http.Request, user store.User, _ string) {
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	if err != nil || vmid <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_desktop", "Desktop identifier is invalid")
		return
	}
	if !s.requireUserDesktopAccess(w, r, user, vmid) {
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
	if err := readDesktopReady(r.Context(), s.pve, machine.Node, machine.VMID); err != nil {
		writeError(w, http.StatusConflict, "desktop_not_ready", "Desktop services are still starting")
		return
	}
	if _, err := s.reconcileDesktopAccessPolicy(r.Context(), machine); err != nil {
		s.logger.Warn("enforce desktop access policy before connection", "vmid", machine.VMID, "error", err)
		s.auditRequest(r, user.ID, "desktop.access_policy_enforcement_failed", "virtual_machine", strconv.Itoa(machine.VMID), map[string]any{"vmid": machine.VMID})
		writeError(w, http.StatusConflict, "desktop_policy_not_applied", "Desktop permissions are not ready")
		return
	}
	passwordToken, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create desktop credentials")
		return
	}
	password := "Vd1!" + passwordToken
	const username = "vdi"
	if err := s.pve.SetGuestUserPassword(r.Context(), machine.Node, machine.VMID, username, password); err != nil {
		s.logger.Warn("prepare native desktop credentials", "vmid", machine.VMID, "error", err)
		writeError(w, http.StatusConflict, "desktop_not_ready", "Desktop services are still starting")
		return
	}
	connectionToken, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create desktop connection")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.auditRequest(r, user.ID, "native.desktop_connection_created", "virtual_machine", strconv.Itoa(machine.VMID), map[string]any{
		"vmid": machine.VMID, "protocol": "rdp", "host": host,
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         "conn_" + connectionToken,
		"protocol":   "rdp",
		"host":       host,
		"port":       3389,
		"username":   username,
		"password":   password,
		"issued_at":  time.Now().UTC(),
		"desktop_id": strconv.Itoa(machine.VMID),
	})
}

var desktopReadyPaths = []string{
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
		cookie, err := r.Cookie("vc_vdi_session")
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

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user store.User) {
	csrf, ok := s.issueSession(w, r, user)
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": publicUser(user), "csrf_token": csrf})
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
	if err := s.store.CreateSession(r.Context(), auth.TokenDigest(token), user.ID, csrf, expiresAt); err != nil {
		s.logger.Error("create web session", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create session")
		return "", false
	}
	http.SetCookie(w, &http.Cookie{Name: "vc_vdi_session", Value: token, Path: "/", Expires: expiresAt, MaxAge: int((12 * time.Hour).Seconds()), HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode})
	return csrf, true
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request, session store.Session, _ string) {
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(session.User), "csrf_token": session.CSRFToken})
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
	http.SetCookie(w, &http.Cookie{Name: "vc_vdi_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func publicUser(user store.User) map[string]any {
	return map[string]any{"id": user.ID, "username": user.Username, "display_name": user.DisplayName, "role": user.Role}
}

func (s *Server) infrastructure(w http.ResponseWriter, r *http.Request, session store.Session, _ string) {
	if s.pve == nil {
		writeError(w, http.StatusServiceUnavailable, "pve_not_configured", "PVE is not configured")
		return
	}
	summary, err := s.pve.Summary(r.Context())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.logger.Error("read PVE infrastructure", "error", err)
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to read PVE infrastructure")
		return
	}
	for index := range summary.VMs {
		summary.VMs[index].Managed = isManagedDesktop(summary.VMs[index])
	}
	if session.User.Role != "platform_admin" {
		vmids, err := s.store.UserAssignedDesktopVMIDs(r.Context(), session.User.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to read desktop assignments")
			return
		}
		summary.VMs = filterDesktopsByVMID(summary.VMs, vmids)
		summary.Nodes = nil
		summary.Storage = nil
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
		if _, ok := allowed[machine.VMID]; ok && isManagedDesktop(machine) {
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
	s.writeAccessControl(w, r)
}

func (s *Server) writeAccessControl(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, map[string]any{
		"users": users, "agents": agents, "desktops": desktops, "assignments": assignments,
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
	summary, err := s.pve.Summary(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "pve_unavailable", "Unable to reconcile desktop inventory")
		return
	}
	desktops := make([]store.ManagedDesktop, 0)
	for _, machine := range summary.VMs {
		if isManagedDesktop(machine) {
			desktops = append(desktops, store.ManagedDesktop{VMID: machine.VMID, DisplayName: machine.Name, Node: machine.Node, Present: true, Enabled: true})
		}
	}
	if err := s.store.ReconcileManagedDesktops(r.Context(), desktops); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to save desktop inventory")
		return
	}
	s.auditRequest(r, session.User.ID, "desktop_registry.reconciled", "desktop_registry", "pve", map[string]any{"desktop_count": len(desktops)})
	s.writeAccessControl(w, r)
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
	if (subjectType != "user" && subjectType != "agent") || subjectID == "" || vmid <= 0 || err != nil {
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

func (s *Server) requireAdminMutation(w http.ResponseWriter, r *http.Request, session store.Session) bool {
	if session.User.Role != "platform_admin" {
		writeError(w, http.StatusForbidden, "permission_denied", "Administrator role required")
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
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 8–128 characters")
		return
	}
	if existing, err := s.store.JobByIdempotencyKey(r.Context(), idempotencyKey); err == nil {
		writeJSON(w, http.StatusAccepted, existing)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to inspect existing request")
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
		ID: "job_" + jobToken, IdempotencyKey: idempotencyKey, Operation: "image.build", State: "accepted",
		TargetVMID: profile.TemplateVMID, TargetNode: profile.SourceNode, TaskNode: profile.SourceNode,
		Request: sanitizedRequest, CreatedBy: session.User.ID,
	})
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
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 8–128 characters")
		return
	}
	if existing, err := s.store.JobByIdempotencyKey(r.Context(), idempotencyKey); err == nil {
		writeJSON(w, http.StatusAccepted, existing)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to inspect existing request")
		return
	}
	var request desktopCloneRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if !strings.HasPrefix(request.Name, "vc-vdi-") || len(request.Name) > 63 || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*$`).MatchString(request.Name) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", "MVP desktop names must start with vc-vdi- and contain only letters, numbers, and dashes")
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
		imageProfile, imageErr := s.store.ImageProfileByTemplateVMID(r.Context(), request.SourceVMID)
		if imageErr == nil {
			request.GPUProfileID = imageProfile.DefaultGPUProfileID
		} else if !errors.Is(imageErr, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to resolve the image GPU profile")
			return
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
	createdJob, created, err := s.store.CreateJob(r.Context(), store.Job{
		ID: "job_" + jobID, IdempotencyKey: idempotencyKey, Operation: "pve.template_clone", State: "accepted",
		SourceVMID: request.SourceVMID, TargetVMID: targetVMID, TargetNode: request.TargetNode,
		TaskNode: source.Node, Request: encodedRequest, CreatedBy: session.User.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create job")
		return
	}
	if !created {
		writeJSON(w, http.StatusAccepted, createdJob)
		return
	}
	upid, err := s.pve.CloneTemplate(r.Context(), pve.CloneRequest{
		SourceNode: source.Node, SourceVMID: request.SourceVMID, TargetVMID: targetVMID,
		Name: request.Name, TargetNode: request.TargetNode, Storage: request.Storage, Full: request.Full,
	})
	if err != nil {
		_ = s.store.UpdateJobTask(r.Context(), createdJob.ID, "failed", "", err.Error())
		s.logger.Error("clone PVE template", "job_id", createdJob.ID, "source_vmid", request.SourceVMID, "target_vmid", targetVMID, "error", err)
		writeError(w, http.StatusBadGateway, "pve_clone_failed", "PVE rejected the clone request")
		return
	}
	_ = s.store.UpdateJobTask(r.Context(), createdJob.ID, "running", upid, "")
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
			if request.PCIResourceMapping != "" {
				var configureErr error
				if request.MDevType != "" {
					configureErr = s.pve.ConfigureVMMDevResourceMapping(ctx, job.TargetNode, job.TargetVMID, request.PCIResourceMapping, request.MDevType)
				} else {
					configureErr = s.pve.ConfigureVMPCIResourceMapping(ctx, job.TargetNode, job.TargetVMID, request.PCIResourceMapping)
				}
				if configureErr != nil {
					s.logger.Error("configure cloned VM PCI mapping", "job_id", job.ID, "target_vmid", job.TargetVMID, "mapping", request.PCIResourceMapping, "mdev_type", request.MDevType, "error", configureErr)
					state, message = "failed", "PCI resource mapping configuration failed"
				}
			}
			if registryErr := s.store.UpsertManagedDesktop(ctx, store.ManagedDesktop{VMID: job.TargetVMID, DisplayName: request.Name, Node: job.TargetNode, Present: true, Enabled: true}); registryErr != nil {
				s.logger.Error("register cloned desktop", "job_id", job.ID, "target_vmid", job.TargetVMID, "error", registryErr)
				state, message = "failed", "Desktop registry update failed"
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
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 8–128 characters")
		return
	}
	if existing, err := s.store.JobByIdempotencyKey(r.Context(), idempotencyKey); err == nil {
		writeJSON(w, http.StatusAccepted, existing)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to inspect existing request")
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
	summary, err := s.pve.Summary(r.Context())
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
	if machine == nil || !isManagedDesktop(*machine) {
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
		ID: "job_" + jobToken, IdempotencyKey: idempotencyKey, Operation: operation, State: "accepted",
		TargetVMID: vmid, TargetNode: machine.Node, TaskNode: machine.Node, Request: encodedRequest, CreatedBy: session.User.ID,
	})
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
	policy, err := s.store.PutDesktopAccessPolicy(r.Context(), vmid, request.PrivilegeMode, session.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to save desktop permissions")
		return
	}
	s.auditRequest(r, session.User.ID, "desktop.access_policy_updated", "virtual_machine", strconv.Itoa(vmid), map[string]any{
		"vmid": vmid, "privilege_mode": request.PrivilegeMode, "desired_revision": policy.DesiredRevision,
	})
	if machine.Status == "running" {
		if applied, applyErr := s.applyDesktopAccessPolicy(r.Context(), machine, policy); applyErr == nil {
			policy = applied
			s.auditRequest(r, session.User.ID, "desktop.access_policy_applied", "virtual_machine", strconv.Itoa(vmid), map[string]any{"vmid": vmid, "privilege_mode": request.PrivilegeMode, "applied_revision": policy.AppliedRevision})
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
	command, err := desktopAccessPolicyCommand(osFamily, policy.PrivilegeMode)
	if err != nil {
		return store.DesktopAccessPolicy{}, err
	}
	execContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result, err := s.pve.ExecGuest(execContext, machine.Node, machine.VMID, command)
	if err != nil {
		return store.DesktopAccessPolicy{}, err
	}
	if result.ExitCode != 0 {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Stdout)
		}
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return store.DesktopAccessPolicy{}, fmt.Errorf("guest policy command exited with %d: %s", result.ExitCode, detail)
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
	if privilegeMode != "standard" && privilegeMode != "local_admin" {
		return nil, errors.New("desktop privilege mode is invalid")
	}
	switch osFamily {
	case "linux":
		if privilegeMode == "local_admin" {
			return []string{"/bin/sh", "-c", `set -eu
getent passwd vdi >/dev/null
getent group sudo >/dev/null
policy_file=/etc/sudoers.d/vc-workspace-vdi
policy_line='vdi ALL=(ALL:ALL) NOPASSWD: ALL'
changed=0
if ! id -nG vdi | tr ' ' '\n' | grep -qx sudo; then
  usermod -a -G sudo vdi
  changed=1
fi
if [ ! -f "$policy_file" ] || [ "$(cat "$policy_file")" != "$policy_line" ]; then
  printf '%s\n' "$policy_line" >"$policy_file.tmp"
  chmod 0440 "$policy_file.tmp"
  visudo -cf "$policy_file.tmp" >/dev/null
  mv "$policy_file.tmp" "$policy_file"
  changed=1
fi
if [ "$changed" = 1 ]; then
  loginctl terminate-user vdi >/dev/null 2>&1 || true
  pkill -KILL -u vdi >/dev/null 2>&1 || true
fi
id -nG vdi | tr ' ' '\n' | grep -qx sudo
runuser -u vdi -- sudo -n /usr/bin/true`}, nil
		}
		return []string{"/bin/sh", "-c", `set -eu
getent passwd vdi >/dev/null
changed=0
if id -nG vdi | tr ' ' '\n' | grep -Eq '^(sudo|admin)$'; then changed=1; fi
if [ -e /etc/sudoers.d/vc-workspace-vdi ]; then changed=1; fi
if getent group sudo >/dev/null; then gpasswd -d vdi sudo >/dev/null 2>&1 || true; fi
if getent group admin >/dev/null; then gpasswd -d vdi admin >/dev/null 2>&1 || true; fi
rm -f /etc/sudoers.d/vc-workspace-vdi /etc/sudoers.d/vc-workspace-vdi.tmp
if [ "$changed" = 1 ]; then
  loginctl terminate-user vdi >/dev/null 2>&1 || true
  pkill -KILL -u vdi >/dev/null 2>&1 || true
fi
if id -nG vdi | tr ' ' '\n' | grep -Eq '^(sudo|admin)$'; then exit 42; fi
if runuser -u vdi -- sudo -n /usr/bin/true >/dev/null 2>&1; then exit 43; fi`}, nil
	case "windows":
		operation := `if (-not $isMember) { Add-LocalGroupMember -Group $administrators -Member $user.Name; try { Get-Process -IncludeUserName | Where-Object { $_.UserName -match '\\vdi$' } | Stop-Process -Force -ErrorAction SilentlyContinue } catch {} }; if (-not (Get-LocalGroupMember -Group $administrators | Where-Object { $_.SID -eq $user.SID })) { throw 'administrator membership was not applied' }`
		if privilegeMode == "standard" {
			operation = `if ($isMember) { Remove-LocalGroupMember -Group $administrators -Member $user.Name; try { Get-Process -IncludeUserName | Where-Object { $_.UserName -match '\\vdi$' } | Stop-Process -Force -ErrorAction SilentlyContinue } catch {} }; if (Get-LocalGroupMember -Group $administrators | Where-Object { $_.SID -eq $user.SID }) { throw 'administrator membership was not removed' }`
		}
		script := `$ErrorActionPreference='Stop'; $user=Get-LocalUser -Name 'vdi'; $administrators=(Get-LocalGroup -SID 'S-1-5-32-544').Name; $isMember=Get-LocalGroupMember -Group $administrators | Where-Object { $_.SID -eq $user.SID }; ` + operation
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
	summary, err := s.pve.Summary(r.Context())
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
		s.auditRequest(r, "", "agent.desktop_access_denied", "agent", request.AgentID, map[string]any{"desktop_vmid": request.DesktopVMID, "reason": "not_assigned"})
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
	lease, err := s.store.CreateDesktopLease(r.Context(), store.DesktopLease{
		ID: "lease_" + leaseToken, AgentID: request.AgentID, DesktopID: strconv.Itoa(request.DesktopVMID),
		ExpiresAt: time.Now().Add(time.Duration(request.TTLSeconds) * time.Second),
	})
	if errors.Is(err, store.ErrAlreadyLeased) {
		writeError(w, http.StatusConflict, "desktop_already_leased", "Desktop is already assigned to another agent")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create lease")
		return
	}
	s.auditRequest(r, "", "agent.desktop_lease_created", "desktop_lease", lease.ID, map[string]any{"agent_id": lease.AgentID, "desktop_id": lease.DesktopID})
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
	released, err := s.store.ReleaseDesktopLease(r.Context(), lease.ID)
	if err != nil {
		writeError(w, http.StatusConflict, "lease_not_active", "Desktop lease is no longer active")
		return
	}
	s.auditRequest(r, "", "agent.desktop_lease_released", "desktop_lease", lease.ID, map[string]any{"agent_id": lease.AgentID, "desktop_id": lease.DesktopID})
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
	machine, err := s.managedVirtualMachine(r.Context(), vmid)
	if err != nil {
		writeError(w, http.StatusNotFound, "managed_vm_not_found", "Managed VC Workspace virtual machine was not found")
		return
	}
	if (action == "start" && machine.Status == "running") || (action == "stop" && machine.Status == "stopped") {
		writeError(w, http.StatusConflict, "power_state_conflict", "Virtual machine is already in the requested power state")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 8–128 characters")
		return
	}
	if existing, err := s.store.JobByIdempotencyKey(r.Context(), idempotencyKey); err == nil {
		writeJSON(w, http.StatusAccepted, existing)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to inspect existing request")
		return
	}
	requestJSON, _ := json.Marshal(map[string]any{"lease_id": lease.ID, "agent_id": lease.AgentID, "vmid": vmid, "action": action})
	jobToken, err := auth.OpaqueToken(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to create job")
		return
	}
	job, created, err := s.store.CreateJob(r.Context(), store.Job{
		ID: "job_" + jobToken, IdempotencyKey: idempotencyKey, Operation: "agent.desktop_" + action, State: "accepted",
		TargetVMID: vmid, TargetNode: machine.Node, TaskNode: machine.Node, Request: requestJSON,
	})
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
	s.auditRequest(r, "", "agent.desktop_"+action+"_requested", "desktop_lease", lease.ID, map[string]any{"agent_id": lease.AgentID, "vmid": vmid})
	job.State, job.UPID = "running", upid
	writeJSON(w, http.StatusAccepted, job)
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
	summary, err := s.pve.Summary(ctx)
	if err != nil {
		return pve.VM{}, err
	}
	for _, machine := range summary.VMs {
		if machine.VMID == vmid && isManagedDesktop(machine) {
			return machine, nil
		}
	}
	return pve.VM{}, store.ErrNotFound
}

func isManagedDesktop(machine pve.VM) bool {
	return machine.Kind == "qemu" && !machine.Template && containsString(machine.Tags, "vc-vdi")
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
