package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/oidcauth"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

const (
	liveOIDCCallbackAddress = "127.0.0.1:18081"
	liveOIDCCallbackURL     = "http://127.0.0.1:18081/api/v1/auth/oidc/callback"
	liveOIDCCompleteURL     = "http://127.0.0.1:18081/oidc-complete"
)

var liveOIDCFormPattern = regexp.MustCompile(`(?is)<form\b[^>]*>`) // Keycloak themes may reorder attributes.
var liveOIDCActionPattern = regexp.MustCompile(`(?is)\baction\s*=\s*"([^"]+)"`)

// TestLiveOIDCIdentityProvider is deliberately opt-in because it requires the
// disposable Keycloak and PostgreSQL services from deploy/identity-lab.
func TestLiveOIDCIdentityProvider(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_OIDC_IDENTITY_AUDIT") != "true" {
		t.Skip("VC_WORKSPACE_LIVE_OIDC_IDENTITY_AUDIT is not true")
	}
	issuer := requireLiveEnv(t, "VC_WORKSPACE_LIVE_OIDC_ISSUER")
	clientID := requireLiveEnv(t, "VC_WORKSPACE_LIVE_OIDC_CLIENT_ID")
	clientSecret := requireLiveEnv(t, "VC_WORKSPACE_LIVE_OIDC_CLIENT_SECRET")
	username := requireLiveEnv(t, "VC_WORKSPACE_LIVE_OIDC_USERNAME")
	password := requireLiveEnv(t, "VC_WORKSPACE_LIVE_OIDC_PASSWORD")
	adminUsername := requireLiveEnv(t, "VC_WORKSPACE_LIVE_OIDC_ADMIN_USERNAME")
	adminPassword := requireLiveEnv(t, "VC_WORKSPACE_LIVE_OIDC_ADMIN_PASSWORD")
	databaseURL := requireLiveEnv(t, "VC_WORKSPACE_LIVE_DATABASE_URL")
	requireDisposableIdentityLabDatabase(t, databaseURL)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open identity lab database: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate identity lab database: %v", err)
	}
	initialized, err := database.IsInitialized(ctx)
	if err != nil {
		t.Fatalf("read initialization state: %v", err)
	}
	if !initialized {
		passwordHash, hashErr := auth.HashPassword("identity-lab-local-admin-is-not-used-for-oidc")
		if hashErr != nil {
			t.Fatalf("hash local administrator password: %v", hashErr)
		}
		if err := database.CreateInitialAdmin(ctx, store.User{
			ID: "identity-lab-admin", Username: "local-admin", DisplayName: "Local administrator", PasswordHash: passwordHash,
		}); err != nil {
			t.Fatalf("create local fallback administrator: %v", err)
		}
	}

	oidcService, err := oidcauth.New(ctx, oidcauth.Config{
		Name: "Keycloak identity lab", Issuer: issuer, ClientID: clientID,
		ClientSecret: clientSecret, RedirectURL: liveOIDCCallbackURL, GroupsClaim: "groups",
	})
	if err != nil {
		t.Fatalf("discover live OIDC provider: %v", err)
	}
	if err := database.EnsureOIDCProvider(ctx, oidcauth.ProviderID, oidcService.Name(), oidcService.Issuer(), oidcService.ClientID()); err != nil {
		t.Fatalf("register live OIDC provider: %v", err)
	}

	listener, err := net.Listen("tcp", liveOIDCCallbackAddress)
	if err != nil {
		t.Fatalf("listen on OIDC callback address: %v", err)
	}
	api := New(Dependencies{
		Store: database, PublicURL: liveOIDCCompleteURL, OIDC: oidcService,
		NativeCallbackURL: "vc-workspace://auth/callback",
	})
	mux := http.NewServeMux()
	mux.Handle("/api/", api.Handler())
	mux.HandleFunc("GET /oidc-complete", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- httpServer.Serve(listener) }()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
		select {
		case serveErr := <-serverErrors:
			if serveErr != nil && serveErr != http.ErrServerClosed {
				t.Errorf("OIDC callback server: %v", serveErr)
			}
		case <-time.After(6 * time.Second):
			t.Error("OIDC callback server did not stop")
		}
	}()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create browser cookie jar: %v", err)
	}
	browser := &http.Client{Jar: jar, Timeout: 45 * time.Second}
	performLiveOIDCLogin(t, browser, username, password)
	oidcUserID := assertLiveOIDCMemberships(t, database, []string{"engineering", "workspace-users"})
	assertLiveOIDCSession(t, browser, oidcUserID)

	removeLiveKeycloakGroup(t, issuer, adminUsername, adminPassword, username, "engineering")
	performLiveOIDCLogin(t, browser, username, password)
	if actualUserID := assertLiveOIDCMemberships(t, database, []string{"workspace-users"}); actualUserID != oidcUserID {
		t.Fatalf("repeat OIDC login changed stable user binding: got %q, want %q", actualUserID, oidcUserID)
	}
	assertLiveNativeOIDCExchange(t, browser, oidcUserID)
}

// TestLiveLDAPIdentityReconcile runs the exact command produced for a managed
// Debian desktop inside the systemd-enabled SSSD client container.
func TestLiveLDAPIdentityReconcile(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_LDAP_IDENTITY_AUDIT") != "true" {
		t.Skip("VC_WORKSPACE_LIVE_LDAP_IDENTITY_AUDIT is not true")
	}
	container := requireLiveEnv(t, "VC_WORKSPACE_LIVE_LDAP_CLIENT_CONTAINER")
	alicePassword := requireLiveEnv(t, "VC_WORKSPACE_LIVE_LDAP_ALICE_PASSWORD")
	bobPassword := requireLiveEnv(t, "VC_WORKSPACE_LIVE_LDAP_BOB_PASSWORD")

	profile := store.IdentityProfile{
		Platform: "linux",
		Mode:     "linux_sssd_ldap",
		Config: json.RawMessage(`{
			"domain":"lab.vc-workspace.test",
			"uri":"ldap://ldap",
			"search_base":"dc=lab,dc=vc-workspace,dc=test",
			"access_filter":"(employeeType=vc-workspace)"
		}`),
	}
	plan, err := buildGuestIdentityReconcilePlan(profile, desktopIdentityReconcileRequest{})
	if err != nil {
		t.Fatalf("build live LDAP reconciliation plan: %v", err)
	}
	output, err := liveDockerExec(container, plan.input, plan.command...)
	if err != nil {
		t.Fatalf("apply live LDAP reconciliation plan: %v\n%s", err, output)
	}

	output, err = liveDockerExec(container, nil, "/bin/sh", "-c", "getent passwd alice; getent passwd bob; id alice; getent group workspace-users; sssctl config-check")
	if err != nil {
		t.Fatalf("resolve live LDAP identities: %v\n%s", err, output)
	}
	if !regexp.MustCompile(`(?m)^alice:[^:]*:20001:20001:`).MatchString(output) || !regexp.MustCompile(`(?m)^bob:[^:]*:20002:20002:`).MatchString(output) {
		t.Fatalf("LDAP NSS lookup returned unexpected identities:\n%s", output)
	}

	output, err = liveDockerExec(container, []byte(alicePassword+"\n"), "pamtester", "login", "alice", "authenticate", "acct_mgmt", "open_session", "close_session")
	if err != nil {
		t.Fatalf("authorized LDAP user could not authenticate and open a PAM session: %v\n%s", err, output)
	}
	if output, err = liveDockerExec(container, nil, "test", "-d", "/home/alice"); err != nil {
		t.Fatalf("mkhomedir did not create the authorized user's home: %v\n%s", err, output)
	}

	output, err = liveDockerExec(container, []byte(bobPassword+"\n"), "pamtester", "login", "bob", "authenticate", "acct_mgmt")
	if err == nil {
		t.Fatalf("LDAP access filter allowed unauthorized user bob:\n%s", output)
	}
}

// TestLiveADIdentityReconcile applies the exact VC Workspace AD reconciliation
// plan to a disposable Debian VM, then verifies NSS, Kerberos, PAM, home-directory
// creation, group denial, and live group revocation against a separate Samba AD VM.
func TestLiveADIdentityReconcile(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_AD_IDENTITY_AUDIT") != "true" {
		t.Skip("VC_WORKSPACE_LIVE_AD_IDENTITY_AUDIT is not true")
	}
	domain := requireLiveEnv(t, "VC_WORKSPACE_LIVE_AD_DOMAIN")
	realm := requireLiveEnv(t, "VC_WORKSPACE_LIVE_AD_REALM")
	allowedGroup := requireLiveEnv(t, "VC_WORKSPACE_LIVE_AD_ALLOWED_GROUP")
	joinUsername := requireLiveEnv(t, "VC_WORKSPACE_LIVE_AD_JOIN_USERNAME")
	joinPassword := requireLiveEnv(t, "VC_WORKSPACE_LIVE_AD_JOIN_PASSWORD")
	alicePassword := requireLiveEnv(t, "VC_WORKSPACE_LIVE_AD_ALICE_PASSWORD")
	bobPassword := requireLiveEnv(t, "VC_WORKSPACE_LIVE_AD_BOB_PASSWORD")
	clientVMID := requireLiveVMID(t, "VC_WORKSPACE_LIVE_AD_CLIENT_VMID")
	serverVMID := requireLiveVMID(t, "VC_WORKSPACE_LIVE_AD_SERVER_VMID")

	client := livePrivilegeClient(t)
	clientMachine := requireDisposableLiveIdentityVM(t, client, clientVMID, "vc-workspace-ad-client-")
	serverMachine := requireDisposableLiveIdentityVM(t, client, serverVMID, "vc-workspace-identity-lab-")
	config, err := json.Marshal(map[string]string{
		"domain": domain, "realm": realm, "allowed_groups": allowedGroup + "@" + domain,
	})
	if err != nil {
		t.Fatalf("encode live AD identity profile: %v", err)
	}
	plan, err := buildGuestIdentityReconcilePlan(store.IdentityProfile{
		Platform: "linux", Mode: "linux_sssd_ad", Config: config,
	}, desktopIdentityReconcileRequest{JoinUsername: joinUsername, JoinPassword: joinPassword})
	if err != nil {
		t.Fatalf("build live AD reconciliation plan: %v", err)
	}
	result, err := liveExecGuestWithInput(t, client, clientMachine, plan.command, plan.input)
	requireLiveGuestSuccess(t, "apply live AD reconciliation plan", result, err)

	principalAlice := "alice@" + domain
	principalBob := "bob@" + domain
	result, err = liveExecGuest(t, client, clientMachine, []string{
		"/bin/sh", "-c",
		`set -eu; realm list; sssctl config-check; sssctl domain-status "$4" --online; getent passwd "$1"; getent passwd "$2"; id "$1"; getent group "$3"`,
		"vc-workspace-ad-audit", principalAlice, principalBob, allowedGroup + "@" + domain,
		domain,
	})
	requireLiveGuestSuccess(t, "resolve live AD identities", result, err)

	result, err = liveExecGuestWithInput(t, client, clientMachine,
		[]string{"pamtester", "login", principalAlice, "authenticate", "acct_mgmt", "open_session", "close_session"},
		[]byte(alicePassword+"\n"))
	requireLiveGuestSuccess(t, "authenticate authorized AD user", result, err)
	result, err = liveExecGuest(t, client, clientMachine, []string{
		"/bin/sh", "-c",
		`set -eu; home=$(getent passwd "$1" | cut -d: -f6); test -n "$home"; test -d "$home"; printf '%s\n' "$home"`,
		"vc-workspace-ad-audit", principalAlice,
	})
	requireLiveGuestSuccess(t, "verify AD home-directory creation", result, err)

	result, err = liveExecGuestWithInput(t, client, clientMachine,
		[]string{"pamtester", "login", principalBob, "authenticate", "acct_mgmt"},
		[]byte(bobPassword+"\n"))
	if err != nil {
		t.Fatalf("run unauthorized AD PAM check: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("AD group policy allowed unauthorized user bob: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
	result, err = liveExecGuestWithInput(t, client, clientMachine,
		[]string{"/bin/sh", "-c", `password=$(cat); printf '%s\n' "$password" | kinit "$1"; klist -s; kdestroy`, "vc-workspace-ad-audit", "bob@" + strings.ToUpper(realm)},
		[]byte(bobPassword))
	requireLiveGuestSuccess(t, "prove denied AD user has valid credentials", result, err)

	result, err = liveExecGuestWithInput(t, client, clientMachine,
		[]string{"/bin/sh", "-c", `password=$(cat); printf '%s\n' "$password" | kinit "$1"; klist -s; kdestroy`, "vc-workspace-ad-audit", "alice@" + strings.ToUpper(realm)},
		[]byte(alicePassword))
	requireLiveGuestSuccess(t, "obtain live AD Kerberos ticket", result, err)

	result, err = liveExecGuest(t, client, serverMachine, []string{"samba-tool", "group", "listmembers", allowedGroup})
	requireLiveGuestSuccess(t, "read live AD authorization group", result, err)
	if !containsWord(result.Stdout, "alice") {
		t.Fatalf("live AD authorization group does not contain alice: %q", result.Stdout)
	}

	revoked := false
	defer func() {
		if !revoked {
			return
		}
		restore, restoreErr := liveExecGuest(t, client, serverMachine, []string{"samba-tool", "group", "addmembers", allowedGroup, "alice"})
		if restoreErr != nil || restore.ExitCode != 0 {
			t.Errorf("restore alice AD group membership: result=%#v error=%v", restore, restoreErr)
		}
		_, _ = liveExecGuest(t, client, clientMachine, []string{"sss_cache", "-E"})
	}()
	result, err = liveExecGuest(t, client, serverMachine, []string{"samba-tool", "group", "removemembers", allowedGroup, "alice"})
	requireLiveGuestSuccess(t, "revoke live AD group membership", result, err)
	revoked = true
	result, err = liveExecGuest(t, client, clientMachine, []string{"sss_cache", "-E"})
	requireLiveGuestSuccess(t, "invalidate SSSD cache after revocation", result, err)
	result, err = liveExecGuestWithInput(t, client, clientMachine,
		[]string{"pamtester", "login", principalAlice, "authenticate", "acct_mgmt"},
		[]byte(alicePassword+"\n"))
	if err != nil {
		t.Fatalf("run revoked AD PAM check: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("AD group revocation did not deny alice: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}

	result, err = liveExecGuest(t, client, serverMachine, []string{"samba-tool", "group", "addmembers", allowedGroup, "alice"})
	requireLiveGuestSuccess(t, "restore live AD group membership", result, err)
	revoked = false
	result, err = liveExecGuest(t, client, clientMachine, []string{"sss_cache", "-E"})
	requireLiveGuestSuccess(t, "invalidate SSSD cache after restore", result, err)
	result, err = liveExecGuestWithInput(t, client, clientMachine,
		[]string{"pamtester", "login", principalAlice, "authenticate", "acct_mgmt"},
		[]byte(alicePassword+"\n"))
	requireLiveGuestSuccess(t, "authenticate restored AD user", result, err)
}

func requireLiveVMID(t *testing.T, name string) int {
	t.Helper()
	value := requireLiveEnv(t, name)
	vmid, err := strconv.Atoi(value)
	if err != nil || vmid <= 0 {
		t.Fatalf("%s must be a positive VMID", name)
	}
	return vmid
}

func requireDisposableLiveIdentityVM(t *testing.T, client *pve.Client, vmid int, namePrefix string) pve.VM {
	t.Helper()
	summary, err := client.Summary(t.Context())
	if err != nil {
		t.Fatalf("read PVE summary for identity audit: %v", err)
	}
	for _, machine := range summary.VMs {
		if machine.VMID != vmid {
			continue
		}
		if machine.Kind != "qemu" || machine.Status != "running" || machine.Template ||
			!strings.HasPrefix(machine.Name, namePrefix) || !containsString(machine.Tags, "temporary") ||
			!containsString(machine.Tags, "vc-workspace") {
			t.Fatalf("VMID %d is not a running disposable identity-lab VM: %#v", vmid, machine)
		}
		return machine
	}
	t.Fatalf("identity-lab VMID %d was not found", vmid)
	return pve.VM{}
}

func liveExecGuestWithInput(t *testing.T, client *pve.Client, machine pve.VM, command []string, input []byte) (pve.GuestExecResult, error) {
	t.Helper()
	commandContext, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	return client.ExecGuestWithInput(commandContext, machine.Node, machine.VMID, command, input)
}

func requireLiveGuestSuccess(t *testing.T, operation string, result pve.GuestExecResult, err error) {
	t.Helper()
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("%s: result=%#v error=%v", operation, result, err)
	}
}

func performLiveOIDCLogin(t *testing.T, browser *http.Client, username, password string) {
	t.Helper()
	response, err := browser.Get("http://" + liveOIDCCallbackAddress + "/api/v1/auth/oidc/start")
	if err != nil {
		t.Fatalf("start live OIDC login: %v", err)
	}
	body := readLiveResponse(t, response)
	if response.StatusCode == http.StatusNoContent && response.Request.URL.String() == liveOIDCCompleteURL {
		return // Existing Keycloak SSO session completed without showing the form.
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("load Keycloak login form: status=%d path=%s", response.StatusCode, response.Request.URL.Path)
	}
	action := liveOIDCLoginAction(body)
	if action == "" {
		t.Fatalf("Keycloak response did not contain the login form action: path=%s response_bytes=%d", response.Request.URL.Path, len(body))
	}
	values := url.Values{"username": {username}, "password": {password}, "credentialId": {""}}
	request, err := http.NewRequest(http.MethodPost, action, strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatalf("create Keycloak login request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Referer", response.Request.URL.String())
	response, err = browser.Do(request)
	if err != nil {
		t.Fatalf("submit Keycloak login: %v", err)
	}
	body = readLiveResponse(t, response)
	if response.StatusCode != http.StatusNoContent || response.Request.URL.String() != liveOIDCCompleteURL {
		t.Fatalf("OIDC callback did not complete: status=%d path=%s body=%s", response.StatusCode, response.Request.URL.Path, body)
	}
}

func liveOIDCLoginAction(document string) string {
	for _, form := range liveOIDCFormPattern.FindAllString(document, -1) {
		if !strings.Contains(form, `id="kc-form-login"`) && !strings.Contains(form, `name="login"`) {
			continue
		}
		match := liveOIDCActionPattern.FindStringSubmatch(form)
		if len(match) == 2 {
			return html.UnescapeString(match[1])
		}
	}
	return ""
}

func assertLiveOIDCMemberships(t *testing.T, database *store.Store, expectedExternalIDs []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	users, err := database.Users(ctx)
	if err != nil {
		t.Fatalf("read OIDC users: %v", err)
	}
	var oidcUser store.UserAccount
	for _, user := range users {
		if user.IdentityKind == "oidc" {
			if oidcUser.ID != "" {
				t.Fatalf("expected one OIDC test user, found at least %q and %q", oidcUser.ID, user.ID)
			}
			oidcUser = user
		}
	}
	if oidcUser.ID == "" || oidcUser.Role != "user" || oidcUser.Disabled {
		t.Fatalf("OIDC user was not provisioned as an enabled standard user: %#v", oidcUser)
	}
	groups, err := database.IdentityGroups(ctx)
	if err != nil {
		t.Fatalf("read OIDC groups: %v", err)
	}
	memberships, err := database.IdentityGroupMemberships(ctx)
	if err != nil {
		t.Fatalf("read OIDC memberships: %v", err)
	}
	externalByID := make(map[string]string)
	for _, group := range groups {
		if group.ProviderID == oidcauth.ProviderID && group.Source == "oidc" {
			externalByID[group.ID] = group.ExternalID
		}
	}
	actual := make([]string, 0)
	for _, membership := range memberships {
		if membership.UserID == oidcUser.ID && membership.Source == "oidc" {
			actual = append(actual, externalByID[membership.GroupID])
		}
	}
	sort.Strings(actual)
	expected := append([]string(nil), expectedExternalIDs...)
	sort.Strings(expected)
	if fmt.Sprint(actual) != fmt.Sprint(expected) {
		t.Fatalf("OIDC memberships did not converge: got %v, want %v", actual, expected)
	}
	return oidcUser.ID
}

func assertLiveOIDCSession(t *testing.T, browser *http.Client, expectedUserID string) {
	t.Helper()
	response, err := browser.Get("http://" + liveOIDCCallbackAddress + "/api/v1/me")
	if err != nil {
		t.Fatalf("read OIDC-backed platform session: %v", err)
	}
	body := readLiveResponse(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("OIDC-backed platform session was rejected: status=%d body=%s", response.StatusCode, body)
	}
	var payload struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil || payload.User.ID != expectedUserID {
		t.Fatalf("OIDC-backed platform session resolved unexpected user: user=%q error=%v body=%s", payload.User.ID, err, body)
	}
}

func assertLiveNativeOIDCExchange(t *testing.T, browser *http.Client, expectedUserID string) {
	t.Helper()
	nativeBrowser := &http.Client{
		Jar:     browser.Jar,
		Timeout: 45 * time.Second,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if request.URL.Scheme == "vc-workspace" {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	response, err := nativeBrowser.Get("http://" + liveOIDCCallbackAddress + "/api/v1/auth/oidc/start?client=macos")
	if err != nil {
		t.Fatalf("start native OIDC flow: %v", err)
	}
	body := readLiveResponse(t, response)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("native OIDC flow did not return an app callback: status=%d path=%s body=%s", response.StatusCode, response.Request.URL.Path, body)
	}
	callback, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse native OIDC callback: %v", err)
	}
	if callback.Scheme != "vc-workspace" || callback.Host != "auth" || callback.Path != "/callback" {
		t.Fatalf("native OIDC callback is invalid: scheme=%q host=%q path=%q", callback.Scheme, callback.Host, callback.Path)
	}
	code := callback.Query().Get("code")
	if code == "" {
		t.Fatal("native OIDC callback did not contain a one-time code")
	}
	payload, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatalf("encode native exchange request: %v", err)
	}
	exchange := func() (*http.Response, string) {
		request, requestErr := http.NewRequest(http.MethodPost, "http://"+liveOIDCCallbackAddress+"/api/v1/auth/native/exchange", bytes.NewReader(payload))
		if requestErr != nil {
			t.Fatalf("create native exchange request: %v", requestErr)
		}
		request.Header.Set("Content-Type", "application/json")
		result, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatalf("exchange native OIDC code: %v", requestErr)
		}
		return result, readLiveResponse(t, result)
	}
	response, body = exchange()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("native OIDC code exchange failed: status=%d body=%s", response.StatusCode, body)
	}
	var session struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(body), &session); err != nil || session.AccessToken == "" || session.User.ID != expectedUserID {
		t.Fatalf("native OIDC session is invalid: user=%q has_token=%t error=%v", session.User.ID, session.AccessToken != "", err)
	}
	response, body = exchange()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("native OIDC one-time code was reusable: status=%d body=%s", response.StatusCode, body)
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+liveOIDCCallbackAddress+"/api/v1/auth/native/logout", nil)
	if err != nil {
		t.Fatalf("create native logout request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+session.AccessToken)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("logout native OIDC session: %v", err)
	}
	body = readLiveResponse(t, response)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("native OIDC logout failed: status=%d body=%s", response.StatusCode, body)
	}
}

func removeLiveKeycloakGroup(t *testing.T, issuer, adminUsername, adminPassword, username, groupName string) {
	t.Helper()
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		t.Fatalf("parse live OIDC issuer: %v", err)
	}
	baseURL := issuerURL.Scheme + "://" + issuerURL.Host
	realm := strings.TrimPrefix(issuerURL.Path, "/realms/")
	if realm == "" || strings.Contains(realm, "/") {
		t.Fatalf("live Keycloak issuer must end in one realm: %s", issuer)
	}

	form := url.Values{
		"grant_type": {"password"}, "client_id": {"admin-cli"},
		"username": {adminUsername}, "password": {adminPassword},
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/realms/master/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("create Keycloak admin token request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request Keycloak admin token: %v", err)
	}
	body := readLiveResponse(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Keycloak admin token failed: status=%d body=%s", response.StatusCode, body)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &token); err != nil || token.AccessToken == "" {
		t.Fatalf("decode Keycloak admin token: %v", err)
	}

	var users []struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	liveKeycloakAdminJSON(t, token.AccessToken, baseURL+"/admin/realms/"+url.PathEscape(realm)+"/users?exact=true&username="+url.QueryEscape(username), &users)
	if len(users) != 1 || users[0].Username != username {
		t.Fatalf("resolve Keycloak user %q: %#v", username, users)
	}
	var groups []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	liveKeycloakAdminJSON(t, token.AccessToken, baseURL+"/admin/realms/"+url.PathEscape(realm)+"/groups?exact=true&search="+url.QueryEscape(groupName), &groups)
	groupID := ""
	for _, group := range groups {
		if group.Name == groupName {
			groupID = group.ID
			break
		}
	}
	if groupID == "" {
		t.Fatalf("resolve Keycloak group %q: %#v", groupName, groups)
	}
	request, err = http.NewRequest(http.MethodDelete, baseURL+"/admin/realms/"+url.PathEscape(realm)+"/users/"+url.PathEscape(users[0].ID)+"/groups/"+url.PathEscape(groupID), nil)
	if err != nil {
		t.Fatalf("create Keycloak group removal request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("remove Keycloak user group: %v", err)
	}
	body = readLiveResponse(t, response)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("remove Keycloak user group: status=%d body=%s", response.StatusCode, body)
	}
}

func liveKeycloakAdminJSON(t *testing.T, token, endpoint string, target any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("create Keycloak admin request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call Keycloak admin API: %v", err)
	}
	body := readLiveResponse(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Keycloak admin API failed: status=%d body=%s", response.StatusCode, body)
	}
	if err := json.Unmarshal([]byte(body), target); err != nil {
		t.Fatalf("decode Keycloak admin response: %v body=%s", err, body)
	}
}

func readLiveResponse(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("read live HTTP response: %v", err)
	}
	return string(body)
}

func liveDockerExec(container string, input []byte, command ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	arguments := append([]string{"exec", "-i", container}, command...)
	process := exec.CommandContext(ctx, "docker", arguments...)
	process.Stdin = bytes.NewReader(input)
	output, err := process.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), fmt.Errorf("docker exec timed out: %w", ctx.Err())
	}
	return string(output), err
}

func requireLiveEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

func requireDisposableIdentityLabDatabase(t *testing.T, databaseURL string) {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil || strings.Trim(parsed.Path, "/") != "vc_workspace_identity_lab" {
		t.Fatalf("VC_WORKSPACE_LIVE_DATABASE_URL must target the disposable vc_workspace_identity_lab database")
	}
}
