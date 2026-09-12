package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ControlListenerConfig struct {
	Address string
	TLS     *tls.Config
	Peers   PeerPins
	files   *tlsFiles
}

// The control-plane listener is opt-in and never shares the public API port.
func LoadControlListenerConfig() (*ControlListenerConfig, error) {
	keys := []string{"VC_WORKSPACE_GATEWAY_CONTROL_ADDR", "VC_WORKSPACE_GATEWAY_CONTROL_TLS_CERT_FILE", "VC_WORKSPACE_GATEWAY_CONTROL_TLS_KEY_FILE", "VC_WORKSPACE_GATEWAY_CONTROL_CLIENT_CA_FILE", "VC_WORKSPACE_GATEWAY_CONTROL_PEERS_FILE"}
	values := make([]string, len(keys))
	set := 0
	for i, key := range keys {
		values[i] = os.Getenv(key)
		if values[i] != "" {
			set++
		}
	}
	if set == 0 {
		return nil, nil
	}
	if set != len(keys) || !listenAddress(values[0]) {
		return nil, ErrConfiguration
	}
	files := &tlsFiles{cert: values[1], key: values[2], ca: values[3], peers: values[4]}
	data, err := ReadConfigFile(values[4])
	if err != nil {
		return nil, err
	}
	peers, err := ParsePeerPins(data)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := files.serverConfig()
	if err != nil {
		return nil, err
	}
	return &ControlListenerConfig{Address: values[0], TLS: tlsConfig, Peers: peers, files: files}, nil
}

type RuntimeConfig struct {
	Address      string
	PublicHost   string
	TLS          *tls.Config
	ControlURL   string
	ControlTLS   *tls.Config
	Relay        Config
	controlFiles *tlsFiles
}

func LoadRuntimeConfig() (RuntimeConfig, error) {
	var cfg RuntimeConfig
	cfg.Address = os.Getenv("VC_WORKSPACE_GATEWAY_ADDR")
	if cfg.Address == "" {
		cfg.Address = "127.0.0.1:8443"
	}
	public, err := url.Parse(os.Getenv("VC_WORKSPACE_GATEWAY_PUBLIC_URL"))
	if err != nil || public.Scheme != "https" || public.Hostname() == "" || public.User != nil || public.RawQuery != "" || public.ForceQuery || public.Fragment != "" ||
		(public.Path != "" && public.Path != "/") || public.RawPath != "" || !listenAddress(cfg.Address) {
		return cfg, ErrConfiguration
	}
	cfg.PublicHost = public.Host
	cfg.Relay.GatewayID = os.Getenv("VC_WORKSPACE_GATEWAY_ID")
	cfg.Relay.MaxConnections = 128
	if raw := os.Getenv("VC_WORKSPACE_GATEWAY_MAX_CONNECTIONS"); raw != "" {
		cfg.Relay.MaxConnections, err = strconv.Atoi(raw)
		if err != nil {
			return cfg, ErrConfiguration
		}
	}
	for _, raw := range strings.Split(os.Getenv("VC_WORKSPACE_GATEWAY_ALLOWED_TARGETS"), ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return cfg, ErrConfiguration
		}
		cfg.Relay.AllowedTargets = append(cfg.Relay.AllowedTargets, prefix)
	}
	serverFiles := tlsFiles{cert: os.Getenv("VC_WORKSPACE_GATEWAY_TLS_CERT_FILE"), key: os.Getenv("VC_WORKSPACE_GATEWAY_TLS_KEY_FILE")}
	cfg.TLS, err = serverFiles.serverConfig()
	if err != nil {
		return cfg, err
	}
	cfg.controlFiles = &tlsFiles{cert: os.Getenv("VC_WORKSPACE_GATEWAY_CLIENT_CERT_FILE"), key: os.Getenv("VC_WORKSPACE_GATEWAY_CLIENT_KEY_FILE"),
		ca: os.Getenv("VC_WORKSPACE_GATEWAY_CONTROL_CA_FILE")}
	cfg.ControlTLS, _, err = cfg.controlFiles.load(true)
	if err != nil {
		return cfg, err
	}
	cfg.ControlURL = os.Getenv("VC_WORKSPACE_GATEWAY_CONTROL_URL")
	return cfg, nil
}

func listenAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return false
	}
	_, err = netip.ParseAddr(host)
	number, portErr := strconv.Atoi(port)
	return err == nil && portErr == nil && number > 0 && number <= 65535
}

func RunControlService(ctx context.Context, cfg ControlListenerConfig, backend ControlBackend, logger *slog.Logger) error {
	if cfg.TLS == nil || cfg.TLS.InsecureSkipVerify || cfg.TLS.ClientAuth != tls.RequireAndVerifyClientCert || cfg.TLS.MinVersion < tls.VersionTLS13 {
		return ErrConfiguration
	}
	handler, err := NewControlHandler(backend, cfg.Peers)
	if err != nil {
		return err
	}
	if cfg.files != nil {
		handler.(*controlHandler).identity = cfg.files.identity
	}
	return runTLSService(ctx, cfg.Address, cfg.TLS, handler, 128, logger, nil)
}

func RunService(ctx context.Context, cfg RuntimeConfig, logger *slog.Logger) error {
	if logger == nil {
		return ErrConfiguration
	}
	client, err := NewControlClient(cfg.ControlURL, cfg.Relay.GatewayID, cfg.ControlTLS)
	if err != nil {
		return err
	}
	defer client.Close()
	if cfg.controlFiles != nil {
		if err := client.reloadFrom(*cfg.controlFiles); err != nil {
			return err
		}
	}
	relay, err := New(client, cfg.Relay)
	if err != nil {
		return err
	}
	transport, err := NewTransport(ctx, relay, cfg.PublicHost, client.Ping)
	if err != nil {
		return err
	}
	transport.logger = logger
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = transport.Close(cleanup)
	}()
	return runTLSService(ctx, cfg.Address, cfg.TLS, transport, cfg.Relay.MaxConnections+32, logger, transport.Close)
}

func runTLSService(parent context.Context, address string, config *tls.Config, handler http.Handler, capacity int, logger *slog.Logger, closeHijacked func(context.Context) error) error {
	if config == nil || config.MinVersion < tls.VersionTLS13 || len(config.Certificates) == 0 || config.KeyLogWriter != nil || !listenAddress(address) || logger == nil {
		return ErrConfiguration
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return errors.New("Gateway listener unavailable")
	}
	limited := &limitedListener{Listener: listener, slots: make(chan struct{}, capacity), done: make(chan struct{})}
	defer limited.Close()
	ctx, stop := context.WithCancel(parent)
	defer stop()
	server := &http.Server{Handler: handler, TLSConfig: config.Clone(),
		ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192,
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){}}
	server.TLSConfig.NextProtos = []string{"http/1.1"}
	finished := make(chan error, 1)
	go func() { finished <- server.ServeTLS(limited, "", "") }()
	logger.Info("Gateway TLS listener started", "address", address)
	select {
	case err = <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	case <-ctx.Done():
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if closeHijacked != nil {
		if closeErr := closeHijacked(shutdown); closeErr != nil {
			err = ErrClosure
		}
	}
	if server.Shutdown(shutdown) != nil {
		_ = server.Close()
		return ErrClosure
	}
	if err != nil {
		return errors.New("Gateway TLS listener stopped unexpectedly")
	}
	return nil
}

// Bound accepted sockets before TLS/HTTP parsing, including slow handshakes and
// hijacked WebSockets. HTTP middleware alone cannot bound those resources.
type limitedListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}

func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.slots }}, nil
}
func (l *limitedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
