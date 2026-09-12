package httpapi

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1" // Keychain's deletion identifier only, never transport trust.
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	linuxguest "github.com/Veritas-Calculus/vc-workspace/deploy/guest/linux"
	"github.com/Veritas-Calculus/vc-workspace/internal/auth"
	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/Veritas-Calculus/vc-workspace/internal/gateway"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
	"github.com/Veritas-Calculus/vc-workspace/internal/testdb"
	"github.com/jackc/pgx/v5"
)

// An extra lab-only boundary around the real PVE transport. It does not mock
// responses, relax TLS, or allow VM configuration/power/clone writes. Even a
// misrouted handler cannot send a Guest command to the business desktop.
type macGatewayPVETransport struct {
	next http.RoundTripper
	host string
}

func macGatewayPVERequestAllowed(r *http.Request, host string) bool {
	if r.URL.Scheme != "https" || r.URL.Host != host || r.URL.RawPath != "" || r.URL.User != nil {
		return false
	}
	path := r.URL.Path
	if r.Method == "POST" {
		return path == "/api2/json/access/ticket" || path == "/api2/json/nodes/infra-node1/qemu/160/agent/exec"
	}
	if r.Method != "GET" {
		return false
	}
	switch path {
	case "/api2/json/version", "/api2/json/cluster/status", "/api2/json/nodes", "/api2/json/storage", "/api2/json/cluster/resources",
		"/api2/json/nodes/infra-node1/qemu/160/config", "/api2/json/nodes/infra-node1/qemu/160/agent/network-get-interfaces",
		"/api2/json/nodes/infra-node1/qemu/160/agent/exec-status":
		return true
	default:
		return false
	}
}

func (g macGatewayPVETransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !macGatewayPVERequestAllowed(r, g.host) {
		return nil, errors.New("PVE request outside isolated Mac Gateway lab")
	}
	return g.next.RoundTrip(r)
}

func TestMacGatewayLabPVEGuard(t *testing.T) {
	for _, v := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/api2/json/cluster/resources?type=vm", true},
		{"POST", "/api2/json/access/ticket", true},
		{"GET", "/api2/json/nodes/infra-node1/qemu/160/agent/exec-status?pid=42", true},
		{"POST", "/api2/json/nodes/infra-node1/qemu/160/agent/exec", true},
		{"POST", "/api2/json/nodes/infra-node6/qemu/158/agent/exec", false},
		{"POST", "/api2/json/nodes/infra-node1/qemu/160/config", false},
		{"POST", "/api2/json/nodes/infra-node1/qemu/160/status/start", false},
		{"DELETE", "/api2/json/nodes/infra-node1/qemu/160", false},
		{"GET", "/api2/json/nodes/infra-node1/qemu/9113/config", false},
		{"POST", "/api2/json/nodes/infra-node1/qemu/%31%36%30/agent/exec", false},
	} {
		r := httptest.NewRequest(v.method, "https://pve.example"+v.path, nil)
		if macGatewayPVERequestAllowed(r, "pve.example") != v.allowed {
			t.Fatal("incorrect lab PVE boundary", v.method, v.path)
		}
		if macGatewayPVERequestAllowed(r, "other.example") {
			t.Fatal("cross-host request permitted")
		}
	}
}

func macGatewayLabTLS(t *testing.T, name string, usage x509.ExtKeyUsage) (tls.Certificate, []byte, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(2 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: name + " peer"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(certificate)
	return tls.Certificate{Certificate: [][]byte{leafDER, der}, PrivateKey: leafKey}, der, pool
}

func macGatewayLabWrite(t *testing.T, directory, name string, bytes []byte) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("cannot create private lab artifact", name)
	}
	defer f.Close()
	if _, err := f.Write(bytes); err != nil {
		t.Fatal("cannot write private lab artifact", name)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
}

// Starts the actual Broker, dedicated mTLS authority and WSS relay against a
// real PVE Guest and a disposable database schema. A human/desktop automation
// drives the unchanged signed Mac app; this harness never auto-accepts a CA.
// Private credentials/CA are written only to an explicitly empty 0700 folder.
// stdin: status, revoke, grant, stop. No arbitrary shell or target API exists.
func TestLiveNativeGatewayMacLab(t *testing.T) {
	if os.Getenv("VC_WORKSPACE_LIVE_GATEWAY_MAC_LAB") != "true" {
		t.Skip("explicit live Mac Gateway lab required")
	}
	if os.Getenv("VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID") != "160" {
		t.Fatal("only isolated VM 160 is allowed")
	}
	databaseURL, err := url.Parse(os.Getenv("VC_WORKSPACE_TEST_DATABASE_URL"))
	if err != nil || databaseURL == nil || databaseURL.Hostname() != "127.0.0.1" || databaseURL.Port() == "55434" || databaseURL.Path != "/vcw_gateway_mac_lab" {
		t.Fatal("dedicated disposable vcw_gateway_mac_lab database required; original database forbidden")
	}
	directory := os.Getenv("VC_WORKSPACE_LIVE_GATEWAY_LAB_DIR")
	resolved, err := filepath.EvalSymlinks(directory)
	meta, statErr := os.Lstat(directory)
	entries, readErr := os.ReadDir(directory)
	if err != nil || resolved != directory || statErr != nil || !meta.IsDir() || meta.Mode().Perm() != 0700 || readErr != nil || len(entries) != 0 {
		t.Fatal("explicit empty canonical 0700 lab directory required")
	}
	endpoint, err := url.Parse(os.Getenv("VC_WORKSPACE_LIVE_PVE_ENDPOINT"))
	if err != nil || endpoint.Scheme != "https" {
		t.Fatal("explicit HTTPS PVE endpoint required")
	}
	credentials, err := os.ReadFile(os.Getenv("VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE"))
	if err != nil {
		t.Fatal("PVE credentials unavailable")
	}
	fields := strings.Fields(string(credentials))
	clear(credentials)
	if len(fields) != 3 || fields[1] != "/" {
		t.Fatal("invalid PVE credential file")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client, err := pve.New(pve.Config{Endpoint: endpoint.String(), Username: fields[0], Password: fields[2], MutationsEnabled: true,
		HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: macGatewayPVETransport{transport, endpoint.Host}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		t.Fatal(err)
	}
	machine := liveMachine(t, client, 160)
	if machine.Node != "infra-node1" || machine.Status != "running" || machine.Template || !isManagedDesktop(machine) || !strings.HasSuffix(machine.Name, "-check") {
		t.Fatal("isolated running VM 160 required")
	}
	qga := func(ctx context.Context, script string) (string, error) {
		result, err := client.ExecGuest(ctx, machine.Node, 160, []string{"/bin/sh", "-c", script})
		if err != nil || result.ExitCode != 0 {
			return "", errors.New("isolated Guest observation failed")
		}
		return strings.TrimSpace(result.Stdout), nil
	}
	if _, err := qga(t.Context(), `set -eu; test "$(. /etc/os-release; echo $VERSION_ID)" = 13; test -z "$(pgrep -x Xorg || true)"; test -z "$(pgrep -x xrdp-sesexec || true)"`); err != nil {
		t.Fatal(err)
	}
	addresses, err := client.GuestNetworkInterfaces(t.Context(), machine.Node, 160)
	if err != nil {
		t.Fatal("Guest address observation failed")
	}
	address := desktopIPv4(addresses)
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		t.Fatal("private Guest IPv4 required")
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	marker := "svc" + hex.EncodeToString(random[:])
	username := store.NativeGuestUsername(marker)
	units := map[string]string{}
	for _, name := range []string{"vc-workspace-agent.service", "vc-workspace-accounts.service", "vc-workspace-accounts.timer"} {
		raw, err := linuxguest.Units.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		units[name] = string(raw)
	}
	fixture := func(ctx context.Context, operation string) error {
		payload, _ := pve.MarshalGuestJSON(map[string]any{"units": units, "login_fence_installer": linuxguest.LoginFenceInstaller})
		result, err := client.ExecGuestWithInput(ctx, machine.Node, 160, []string{"/usr/bin/python3", "-c", guestServiceFixture, operation, marker, "native"}, payload)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("service fixture %s failed; retain recovery marker %s", operation, marker)
		}
		return nil
	}
	accountFixture := func(ctx context.Context, operation string) (string, error) {
		result, err := client.ExecGuest(ctx, machine.Node, 160, []string{"/usr/bin/python3", "-c", nativeGuestFixture, operation, marker, username})
		if err != nil || result.ExitCode != 0 {
			return "", fmt.Errorf("account fixture %s failed; retain recovery marker %s", operation, marker)
		}
		return result.Stdout, nil
	}
	executor := computer.NewPVEExecutor(client)
	var identity computer.AccountIdentity
	setup, bound := false, false
	// Registered before writes. Refuse cleanup if identity ownership is unclear;
	// the marker and Guest manifest remain for explicit recovery, never a sweep.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
		defer cancel()
		if !setup {
			return
		}
		if err := fixture(ctx, "stop"); err != nil {
			t.Error(err)
			return
		}
		if identity.UID != 0 {
			observed, err := executor.InspectNativeAccount(ctx, machine, username)
			if err != nil || observed.Identity != identity {
				t.Error("retaining uncertain account", marker)
				return
			}
			if !bound {
				if _, err := accountFixture(ctx, "bind"); err != nil {
					t.Error(err)
					return
				}
				bound = true
			}
			intent := computer.NativeCredential{SchemaVersion: 1, Identity: identity, ConnectionID: "conn_" + marker, Revision: 1}
			if observed.Lifecycle != nil {
				intent = *observed.Lifecycle
				intent.Revision++
				intent.Phase = ""
				intent.ExpiresUnixSeconds = 0
			}
			if err := executor.ChangeNativeCredential(ctx, machine, intent, "revoke", nil); err != nil {
				t.Error("owned account revoke failed", marker)
				return
			}
			if _, err := accountFixture(ctx, "remove"); err != nil {
				t.Error(err)
				return
			}
		}
		if err := fixture(ctx, "restore"); err != nil {
			t.Error(err)
			return
		}
		t.Log("owned Guest account and temporary canonical services removed; PAM/readiness restored")
	})
	setup = true
	if err := fixture(t.Context(), "setup"); err != nil {
		t.Fatal(err)
	}
	if _, err := accountFixture(t.Context(), "reserve"); err != nil {
		t.Fatal(err)
	}
	identity, err = executor.PrepareNativeAccount(t.Context(), machine, username)
	if err != nil {
		t.Fatal("owned Guest account preparation failed", marker)
	}
	if _, err := accountFixture(t.Context(), "bind"); err != nil {
		t.Fatal(err)
	}
	bound = true

	schemaURL := testdb.URL(t)
	db, err := store.Open(t.Context(), schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	password, err := auth.OpaqueToken(24)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateLocalUser(t.Context(), store.User{ID: marker, Username: "gateway-lab", DisplayName: "Gateway Lab", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertManagedDesktop(t.Context(), store.ManagedDesktop{VMID: 160, Node: machine.Node, DisplayName: machine.Name, OSFamily: "linux"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutDesktopAssignment(t.Context(), store.DesktopAssignment{SubjectType: "user", SubjectID: marker, DesktopVMID: 160}); err != nil {
		t.Fatal(err)
	}
	independent, err := pgx.Connect(t.Context(), schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	defer independent.Close(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	publicPair, publicCA, publicRoots := macGatewayLabTLS(t, "VC Workspace Mac Lab "+marker, x509.ExtKeyUsageServerAuth)
	controlPair, _, controlRoots := macGatewayLabTLS(t, "VC Workspace Control Lab "+marker, x509.ExtKeyUsageServerAuth)
	clientPair, _, clientRoots := macGatewayLabTLS(t, "VC Workspace Peer Lab "+marker, x509.ExtKeyUsageClientAuth)
	peers := gateway.PeerPins{sha256.Sum256(clientPair.Certificate[0]): "gw_" + marker}
	controlHandler, err := gateway.NewControlHandler(db, peers)
	if err != nil {
		t.Fatal(err)
	}
	controlServer := httptest.NewUnstartedServer(controlHandler)
	controlServer.TLS, err = gateway.ControlServerTLS(controlPair, clientRoots)
	if err != nil {
		t.Fatal(err)
	}
	controlServer.StartTLS()
	defer controlServer.Close()
	controlClient, err := gateway.NewControlClient(controlServer.URL, "gw_"+marker, &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: controlRoots, Certificates: []tls.Certificate{clientPair}})
	if err != nil {
		t.Fatal(err)
	}
	defer controlClient.Close()
	if err := controlClient.Ping(t.Context()); err != nil {
		t.Fatal("actual mTLS authority not ready")
	}
	ctx, stop := context.WithTimeout(t.Context(), 40*time.Minute)
	defer stop()
	relay, err := gateway.New(controlClient, gateway.Config{GatewayID: "gw_" + marker, AllowedTargets: []netip.Prefix{netip.PrefixFrom(ip, 32)}, MaxConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	publicServer := httptest.NewUnstartedServer(nil)
	publicHost := publicServer.Listener.Addr().String()
	tunnel, err := gateway.NewTransport(ctx, relay, publicHost, controlClient.Ping)
	if err != nil {
		t.Fatal(err)
	}
	publicServer.Config.Handler = tunnel
	publicServer.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{publicPair}}
	publicServer.StartTLS()
	defer publicServer.Close()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := tunnel.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	route, err := gateway.NewBrokerRoute("gw_"+marker, publicServer.URL, []netip.Prefix{netip.PrefixFrom(ip, 32)}, peers)
	if err != nil {
		t.Fatal(err)
	}
	brokerServer := httptest.NewUnstartedServer(nil)
	brokerURL := "http://" + brokerServer.Listener.Addr().String()
	broker := New(Dependencies{Logger: logger, PVE: client, Store: db, PublicURL: brokerURL, NativeGateway: route})
	brokerServer.Config.Handler = broker.Handler()
	brokerServer.Start()
	defer brokerServer.Close()
	maintenance, cancelMaintenance := context.WithCancel(ctx)
	maintenanceDone := make(chan struct{})
	go func() { defer close(maintenanceDone); broker.RunBackgroundMaintenance(maintenance) }()
	defer func() { cancelMaintenance(); <-maintenanceDone }()
	probe := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: publicRoots}}}
	defer probe.CloseIdleConnections()
	response, err := probe.Get(publicServer.URL + "/ready")
	if err != nil {
		t.Fatal("actual public TLS Gateway not ready")
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("Gateway mTLS/database readiness rejected")
	}
	macGatewayLabWrite(t, directory, "gateway-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: publicCA}))
	private, _ := json.Marshal(map[string]any{"server": brokerURL, "username": "gateway-lab", "password": password, "vmid": 160, "marker": marker, "guest_username": username, "ca_sha1": fmt.Sprintf("%x", sha1.Sum(publicCA))})
	macGatewayLabWrite(t, directory, "client.json", private)
	clear(private)
	t.Logf("Mac lab ready: %s; private artifacts in %s; commands: status, revoke, grant, stop", brokerURL, directory)
	commands := make(chan string)
	go func() {
		defer close(commands)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			select {
			case commands <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			t.Error("Mac lab deadline/cancellation; no acceptance claimed")
			return
		case command, ok := <-commands:
			if !ok {
				t.Error("Mac lab input closed without explicit stop")
				return
			}
			switch command {
			case "stop":
				return
			case "revoke":
				if _, err := db.DeleteDesktopAssignment(t.Context(), "user", marker, 160); err != nil {
					t.Fatal(err)
				}
				t.Log("test user's assignment revoked through the production Store transaction")
			case "grant":
				if _, err := db.PutDesktopAssignment(t.Context(), store.DesktopAssignment{SubjectType: "user", SubjectID: marker, DesktopVMID: 160}); err != nil {
					t.Fatal(err)
				}
				t.Log("test user's assignment restored through the production Store transaction")
			case "status":
				var issued, consumed, active, closed int
				if err := independent.QueryRow(t.Context(), `SELECT count(*),count(consumed_at),count(*) FILTER(WHERE consumed_at IS NOT NULL AND closed_at IS NULL AND lease_until>now()),count(closed_at) FROM gateway_session_tickets`).Scan(&issued, &consumed, &active, &closed); err != nil {
					t.Fatal(err)
				}
				observed, err := executor.InspectNativeAccount(t.Context(), machine, username)
				if err != nil {
					t.Fatal("Guest account observation failed")
				}
				phase := "none"
				if observed.Lifecycle != nil {
					phase = observed.Lifecycle.Phase
				}
				desktop, _ := accountFixture(t.Context(), "desktop")
				var proof struct {
					UID   uint32 `json:"uid"`
					PID   int    `json:"pid"`
					Ticks string `json:"ticks"`
				}
				_ = json.Unmarshal([]byte(desktop), &proof)
				t.Logf("lab status: issued=%d consumed=%d active=%d closed=%d phase=%s disabled=%t absent=%t uid=%d xfce_pid=%d ticks=%s", issued, consumed, active, closed, phase, observed.Disabled, observed.ProcessesAbsent, identity.UID, proof.PID, proof.Ticks)
			default:
				t.Log("unknown lab command rejected")
			}
		}
	}
}
