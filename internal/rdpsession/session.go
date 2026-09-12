// Package rdpsession owns a headless, certificate-pinned Windows RDP transport.
// It never provisions an account, selects a desktop, grants an Agent lease or
// implicitly reconnects. Those decisions belong to the authenticated broker.
package rdpsession

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var (
	ErrInvalid     = errors.New("invalid headless session configuration")
	ErrUnavailable = errors.New("headless session worker unavailable")
	ErrProtocol    = errors.New("invalid headless session worker receipt")
	ErrClosed      = errors.New("headless session transport closed")
	ErrCertificate = errors.New("headless session certificate pin rejected")
	ErrRedirect    = errors.New("headless session server redirect rejected")
	ErrConnect     = errors.New("headless session RDP negotiation failed")
	userRE         = regexp.MustCompile(`^vca[a-f0-9]{12}$`)
	domainRE       = regexp.MustCompile(`^[A-Za-z0-9-]{1,15}$`)
)

type Config struct {
	// Trusted deployment configuration only; never an HTTP/MCP argument.
	Executable                 string
	Endpoint                   netip.AddrPort
	Username, Domain, Password string
	CertificateSHA256          string
	Width, Height              int
	ExpiresAt                  time.Time
}

func frame(config Config, now time.Time) ([]byte, error) {
	address := config.Endpoint.Addr()
	if !address.Is4() || !address.IsPrivate() || config.Endpoint.Port() == 0 ||
		!userRE.MatchString(config.Username) || !domainRE.MatchString(config.Domain) ||
		len(config.Password) < 24 || len(config.Password) > 256 ||
		config.Width < 640 || config.Width > 1920 || config.Height < 480 || config.Height > 1200 ||
		!config.ExpiresAt.After(now) || config.ExpiresAt.Sub(now) > 8*time.Hour {
		return nil, ErrInvalid
	}
	for _, value := range []byte(config.Password) {
		if value < 33 || value > 126 {
			return nil, ErrInvalid
		}
	}
	digest, err := hex.DecodeString(config.CertificateSHA256)
	if err != nil || len(digest) != 32 {
		return nil, ErrInvalid
	}
	host := address.String()
	payload := make([]byte, 4+55+len(host)+len(config.Username)+len(config.Domain)+len(config.Password))
	binary.BigEndian.PutUint32(payload, uint32(len(payload)-4))
	body := payload[4:]
	copy(body, "VCW1")
	binary.BigEndian.PutUint64(body[4:], uint64(config.ExpiresAt.UnixMilli()))
	binary.BigEndian.PutUint16(body[12:], config.Endpoint.Port())
	binary.BigEndian.PutUint16(body[14:], uint16(config.Width))
	binary.BigEndian.PutUint16(body[16:], uint16(config.Height))
	body[18], body[19], body[20] = byte(len(host)), byte(len(config.Username)), byte(len(config.Domain))
	binary.BigEndian.PutUint16(body[21:], uint16(len(config.Password)))
	copy(body[23:55], digest)
	offset := 55
	for _, value := range []string{host, config.Username, config.Domain, config.Password} {
		offset += copy(body[offset:], value)
	}
	return payload, nil
}

type Receipt struct {
	Version int    `json:"version"`
	Event   string `json:"event"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Code    uint32 `json:"code,omitempty"`
}

type ConnectError struct{ Code uint32 }

func (e ConnectError) Error() string { return fmt.Sprintf("%s (code 0x%08x)", ErrConnect, e.Code) }
func (e ConnectError) Unwrap() error { return ErrConnect }

func parseReceipt(raw []byte) (Receipt, error) {
	var receipt Receipt
	if len(raw) == 0 || len(raw) > 1024 {
		return receipt, ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(new(any)) != io.EOF || receipt.Version != 1 {
		return receipt, ErrProtocol
	}
	switch receipt.Event {
	case "connected":
		if receipt.Width != 0 || receipt.Height != 0 || receipt.Reason != "" || receipt.Code != 0 {
			return receipt, ErrProtocol
		}
	case "frame":
		if receipt.Width < 320 || receipt.Height < 240 || receipt.Width > 4096 || receipt.Height > 2160 || receipt.Reason != "" || receipt.Code != 0 {
			return receipt, ErrProtocol
		}
	case "failed":
		if receipt.Width != 0 || receipt.Height != 0 || (receipt.Reason != "certificate" && receipt.Reason != "redirect" && receipt.Reason != "connect" && receipt.Reason != "setup") {
			return receipt, ErrProtocol
		}
		if receipt.Reason == "setup" && (receipt.Code < 1 || receipt.Code > 4) {
			return receipt, ErrProtocol
		}
	default:
		return receipt, ErrProtocol
	}
	return receipt, nil
}

type Session struct {
	command       *exec.Cmd
	input         *os.File
	done, ready   chan struct{}
	stop          sync.Once
	mu            sync.Mutex
	err           error
	width, height int
}

func workerEnvironment(runtimeDir string) ([]string, error) {
	// WinPR needs the real home path even before explicit settings exist. Keep
	// it unchanged; isolate every XDG/cache path and override FreeRDP's home
	// setting in the native worker before connecting. Never inherit credentials
	// or optional library/loader configuration from the caller environment.
	userHome, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(userHome) {
		return nil, ErrUnavailable
	}
	return []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "WLOG_LEVEL=OFF", "HOME=" + userHome,
		"XDG_CONFIG_HOME=" + runtimeDir, "XDG_CACHE_HOME=" + runtimeDir, "XDG_DATA_HOME=" + runtimeDir, "XDG_RUNTIME_DIR=" + runtimeDir}, nil
}

// Start returns only after both RDP negotiation and a nonempty decoded frame.
// ctx owns the ENTIRE transport lifetime, not just this function call. Closing
// its pipe also stops the native worker if the supervisor itself is killed.
func Start(ctx context.Context, config Config) (*Session, error) {
	return start(ctx, config, exec.Command(config.Executable))
}

func start(ctx context.Context, config Config, command *exec.Cmd) (*Session, error) {
	payload, err := frame(config, time.Now())
	if err != nil {
		return nil, err
	}
	defer clear(payload)
	info, err := os.Stat(config.Executable)
	if err != nil || !filepath.IsAbs(config.Executable) || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtimeDir, err := os.MkdirTemp("", "vcw-session-runtime-")
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() {
		if command.Process == nil {
			os.RemoveAll(runtimeDir)
		}
	}()
	command.Env, err = workerEnvironment(runtimeDir)
	if err != nil {
		return nil, err
	}
	in, input, err := os.Pipe()
	if err != nil {
		return nil, ErrUnavailable
	}
	out, output, err := os.Pipe()
	if err != nil {
		in.Close()
		input.Close()
		return nil, ErrUnavailable
	}
	// No password, login name or arbitrary caller environment in process args,
	// environment, logs or spool files. The native worker has no CLI options.
	command.Stdin, command.Stdout, command.Stderr = in, output, io.Discard
	if err := command.Start(); err != nil {
		in.Close()
		input.Close()
		out.Close()
		output.Close()
		return nil, ErrUnavailable
	}
	in.Close()
	output.Close()
	session := &Session{command: command, input: input, done: make(chan struct{}), ready: make(chan struct{})}
	readDone := make(chan struct{})
	go func() { defer close(readDone); defer out.Close(); session.readReceipts(out) }()
	go func() {
		err := command.Wait()
		input.Close()
		os.RemoveAll(runtimeDir)
		// The worker does not spawn children. Bound a malicious/broken binary
		// retaining a duplicated output descriptor without leaking a goroutine.
		select {
		case <-readDone:
		case <-time.After(time.Second):
			out.Close()
			<-readDone
		}
		session.mu.Lock()
		if session.err == nil && err != nil {
			session.err = ErrClosed
		}
		session.mu.Unlock()
		close(session.done)
	}()
	go func() {
		timer := time.NewTimer(max(time.Duration(0), time.Until(config.ExpiresAt)))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			session.Stop()
		case <-timer.C:
			session.Stop()
		case <-session.done:
		}
	}()
	// Bound an unresponsive substitute worker before readiness. The real C
	// decoder has its own five-second input budget and <=1024-byte frame.
	err = input.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err == nil {
		_, err = input.Write(payload)
	}
	if err != nil {
		session.Stop()
		return nil, ErrUnavailable
	}
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		session.Stop()
		return nil, ctx.Err()
	case <-timer.C:
		session.Stop()
		return nil, ErrUnavailable
	case <-session.done:
		return nil, session.failure()
	case <-session.ready:
		if err := session.Alive(); err != nil {
			session.Stop()
			return nil, err
		}
		return session, nil
	}
}

func (s *Session) readReceipts(output io.Reader) {
	reader := bufio.NewReaderSize(output, 1024)
	connected, frame := false, false
	for {
		raw, err := reader.ReadSlice('\n')
		if err == io.EOF && len(raw) == 0 && connected && frame {
			s.mu.Lock()
			s.err = ErrClosed
			s.mu.Unlock()
			go s.Stop()
			return
		}
		receipt, decodeErr := parseReceipt(raw)
		if err == nil && decodeErr == nil && receipt.Event == "failed" {
			s.mu.Lock()
			s.err = map[string]error{"certificate": ErrCertificate, "redirect": ErrRedirect, "connect": ConnectError{Code: receipt.Code}, "setup": fmt.Errorf("%w (setup %d)", ErrUnavailable, receipt.Code)}[receipt.Reason]
			s.mu.Unlock()
			go s.Stop()
			return
		}
		if err != nil || decodeErr != nil || (receipt.Event == "connected" && connected) || (receipt.Event == "frame" && frame) {
			s.mu.Lock()
			s.err = ErrProtocol
			s.mu.Unlock()
			s.input.Close()
			go s.Stop()
			return
		}
		if receipt.Event == "connected" {
			connected = true
		} else {
			frame = true
			s.mu.Lock()
			s.width, s.height = receipt.Width, receipt.Height
			s.mu.Unlock()
		}
		if connected && frame {
			close(s.ready)
		}
	}
}

func (s *Session) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return ErrClosed
}

func (s *Session) Alive() error {
	select {
	case <-s.done:
		return s.failure()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Session) Dimensions() (int, int) { s.mu.Lock(); defer s.mu.Unlock(); return s.width, s.height }
func (s *Session) Done() <-chan struct{}  { return s.done }

// Stop is idempotent and waits for this exact child. No name/prefix/PID scan or
// attempt to log off an OS account; broker revocation owns that separate step.
func (s *Session) Stop() {
	s.stop.Do(func() {
		s.input.Close()
		select {
		case <-s.done:
			return
		case <-time.After(3 * time.Second):
		}
		_ = s.command.Process.Kill()
		<-s.done
	})
}
