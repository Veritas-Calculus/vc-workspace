package rdpsession

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Config{Executable: path, Endpoint: netip.MustParseAddrPort("10.31.0.2:3389"),
		Username: "vca0123456789ab", Domain: "VCW-PC", Password: "Abc1!2345678901234567890",
		CertificateSHA256: strings.Repeat("00", 32), Width: 1280, Height: 720, ExpiresAt: time.Now().Add(time.Minute)}
}

func TestFrame(t *testing.T) {
	c := testConfig(t)
	c.ExpiresAt = time.UnixMilli(10000)
	got, err := frame(c, time.UnixMilli(1000))
	if err != nil {
		t.Fatal(err)
	}
	// Same public test vector as apps/session-worker/tests/protocol.c.
	body := make([]byte, 109)
	copy(body, "VCW1")
	body[10] = 0x27
	body[11] = 0x10
	body[12] = 0x0d
	body[13] = 0x3d
	body[14] = 5
	body[16] = 2
	body[17] = 0xd0
	body[18] = 9
	body[19] = 15
	body[20] = 6
	body[22] = 24
	copy(body[55:], "10.31.0.2vca0123456789abVCW-PCAbc1!2345678901234567890")
	if binary.BigEndian.Uint32(got[:4]) != 109 || !bytes.Equal(got[4:], body) {
		t.Fatal("C/Go launch frame mismatch")
	}
	for name, edit := range map[string]func(*Config){
		"public":            func(c *Config) { c.Endpoint = netip.MustParseAddrPort("1.1.1.1:3389") },
		"loopback":          func(c *Config) { c.Endpoint = netip.MustParseAddrPort("127.0.0.1:3389") },
		"ipv6":              func(c *Config) { c.Endpoint = netip.MustParseAddrPort("[fd00::1]:3389") },
		"mapped":            func(c *Config) { c.Endpoint = netip.MustParseAddrPort("[::ffff:10.31.0.2]:3389") },
		"zero_port":         func(c *Config) { c.Endpoint = netip.MustParseAddrPort("10.31.0.2:0") },
		"human_user":        func(c *Config) { c.Username = "Administrator" },
		"user_suffix":       func(c *Config) { c.Username += "a" },
		"domain_empty":      func(c *Config) { c.Domain = "" },
		"domain_path":       func(c *Config) { c.Domain = "../VCW" },
		"domain_long":       func(c *Config) { c.Domain = strings.Repeat("A", 16) },
		"password_short":    func(c *Config) { c.Password = c.Password[:23] },
		"password_long":     func(c *Config) { c.Password = strings.Repeat("a", 257) },
		"password_nul":      func(c *Config) { c.Password += "\x00" },
		"password_newline":  func(c *Config) { c.Password += "\n" },
		"password_unicode":  func(c *Config) { c.Password += "密" },
		"fingerprint":       func(c *Config) { c.CertificateSHA256 = strings.Repeat("G", 64) },
		"fingerprint_short": func(c *Config) { c.CertificateSHA256 = strings.Repeat("00", 31) },
		"width":             func(c *Config) { c.Width = 1921 },
		"height":            func(c *Config) { c.Height = 479 },
		"expired":           func(c *Config) { c.ExpiresAt = time.UnixMilli(1000) },
		"unbounded":         func(c *Config) { c.ExpiresAt = time.UnixMilli(1001).Add(8 * time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := c
			edit(&bad)
			if data, err := frame(bad, time.UnixMilli(1000)); err != ErrInvalid || data != nil {
				t.Fatal("invalid launch accepted")
			}
		})
	}
}

func TestReceipt(t *testing.T) {
	for _, raw := range []string{`{"version":1,"event":"connected"}`, `{"version":1,"event":"frame","width":1280,"height":720}`} {
		if _, err := parseReceipt([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"", "null", "[]", strings.Repeat(" ", 1025),
		`{"version":1,"event":"failed","reason":"setup","code":0}`,
		`{"version":1,"event":"failed","reason":"setup","code":5}`,
		`{"version":1,"event":"failed","reason":"server text","code":1}`,
		`{"version":1,"event":"failed","reason":"connect","code":4294967296}`,
		`{"version":2,"event":"connected"}`, `{"event":"connected"}`,
		`{"version":1,"event":"connected","secret":"never log"}`,
		`{"version":1,"event":"connected","width":1280}`, `{"version":1,"event":"connected"}{}`,
		`{"version":1,"event":"frame","width":4097,"height":720}`, `{"version":1,"event":"frame","width":1280,"height":0}`,
		`{"version":1,"event":"frame","width":1280,"height":720.1}`} {
		if _, err := parseReceipt([]byte(raw)); err != ErrProtocol {
			t.Fatal("bad receipt accepted")
		}
	}
}

// The production entry point has no args/env hooks. This private subprocess
// double exercises the same pipe, environment sealing and lifetime supervisor.
func TestWorkerProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--worker-double" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if os.Getenv("VCW_TEST_AMBIENT_SECRET") != "" || os.Getenv("HOME") == "" || os.Getenv("XDG_CONFIG_HOME") == "" || os.Getenv("PATH") != "/usr/bin:/bin" {
		os.Exit(3)
	}
	var header [4]byte
	if _, err := io.ReadFull(os.Stdin, header[:]); err != nil {
		os.Exit(4)
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > 1024 {
		os.Exit(5)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(os.Stdin, body); err != nil {
		os.Exit(6)
	}
	if !bytes.HasPrefix(body, []byte("VCW1")) {
		os.Exit(7)
	}
	clear(body)
	connected := "{\"version\":1,\"event\":\"connected\"}\n"
	frame := "{\"version\":1,\"event\":\"frame\",\"width\":1280,\"height\":720}\n"
	switch mode {
	case "ready":
		io.WriteString(os.Stdout, connected+frame)
	case "reversed":
		io.WriteString(os.Stdout, frame+connected)
	case "only_connected":
		io.WriteString(os.Stdout, connected)
	case "only_frame":
		io.WriteString(os.Stdout, frame)
	case "duplicate":
		io.WriteString(os.Stdout, connected+connected)
	case "junk":
		io.WriteString(os.Stdout, "library output with secret\n")
	case "oversize":
		io.WriteString(os.Stdout, strings.Repeat("a", 2048))
	case "truncated":
		io.WriteString(os.Stdout, connected[:10])
		os.Exit(1)
	case "empty":
		os.Exit(1)
	case "certificate", "redirect", "connect":
		io.WriteString(os.Stdout, "{\"version\":1,\"event\":\"failed\",\"reason\":\""+mode+"\",\"code\":131085}\n")
	case "close_stdout":
		io.WriteString(os.Stdout, connected+frame)
		os.Stdout.Close()
	case "stubborn":
		io.WriteString(os.Stdout, connected+frame)
		for {
			time.Sleep(time.Second)
		}
	default:
		os.Exit(8)
	}
	io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func startDouble(t *testing.T, ctx context.Context, c Config, mode string) (*Session, error) {
	t.Helper()
	return start(ctx, c, exec.Command(c.Executable, "-test.run=^TestWorkerProcess$", "--", "--worker-double", mode))
}

func TestWorkerLifetime(t *testing.T) {
	t.Setenv("VCW_TEST_AMBIENT_SECRET", "must-not-reach-child")
	for _, mode := range []string{"ready", "reversed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s, err := startDouble(t, ctx, testConfig(t), mode)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Stop)
			if w, h := s.Dimensions(); w != 1280 || h != 720 {
				t.Fatal("missing frame dimensions")
			}
			if s.Alive() != nil {
				t.Fatal("ready worker not alive")
			}
			cancel()
			select {
			case <-s.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation did not reap child")
			}
			if s.Alive() == nil {
				t.Fatal("closed worker alive")
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(s.Stop)
			}
			wg.Wait()
		})
	}
	t.Run("expiry", func(t *testing.T) {
		c := testConfig(t)
		c.ExpiresAt = time.Now().Add(2 * time.Second)
		s, err := startDouble(t, t.Context(), c, "ready")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Stop)
		select {
		case <-s.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("expired worker not reaped")
		}
	})
	t.Run("kill_fallback", func(t *testing.T) {
		s, err := startDouble(t, t.Context(), testConfig(t), "stubborn")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Stop)
		s.Stop()
		if s.command.ProcessState == nil || s.command.ProcessState.Success() {
			t.Fatal("stubborn child not killed and reaped")
		}
	})
}

func TestWorkerBadReceipts(t *testing.T) {
	for mode, want := range map[string]error{"certificate": ErrCertificate, "redirect": ErrRedirect, "connect": ErrConnect} {
		t.Run(mode, func(t *testing.T) {
			s, err := startDouble(t, t.Context(), testConfig(t), mode)
			if s != nil || !errors.Is(err, want) {
				t.Fatal("typed worker rejection lost", err)
			}
		})
	}
	for _, mode := range []string{"duplicate", "junk", "oversize", "truncated", "empty", "close_stdout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			s, err := startDouble(t, ctx, testConfig(t), mode)
			if s != nil {
				defer s.Stop()
				select {
				case <-s.Done():
				case <-ctx.Done():
					t.Fatal("lost output did not close worker")
				}
				err = s.Alive()
			}
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("invalid worker receipt did not fail closed", err)
			}
		})
	}
	for _, mode := range []string{"only_connected", "only_frame"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			s, err := startDouble(t, ctx, testConfig(t), mode)
			if s != nil || err != context.DeadlineExceeded {
				t.Fatal("incomplete handshake accepted", err)
			}
		})
	}
	c := testConfig(t)
	c.Executable = "relative-worker"
	if _, err := Start(t.Context(), c); err != ErrUnavailable {
		t.Fatal("relative executable allowed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Start(ctx, testConfig(t)); err != context.Canceled {
		t.Fatal("canceled start allowed")
	}
}
