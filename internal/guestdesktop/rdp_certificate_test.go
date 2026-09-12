package guestdesktop

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

func certificateFixture(t *testing.T, change func(*x509.Certificate)) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if change != nil {
		change(c)
	}
	der, err := x509.CreateCertificate(rand.Reader, c, c, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

func TestRDPCertificateParsing(t *testing.T) {
	der, _ := certificateFixture(t, nil)
	encoded := base64.StdEncoding.EncodeToString(der)
	for _, raw := range []string{encoded, encoded + "\n"} {
		pin, err := parseRDPCertificate(raw, time.Now())
		if err != nil || pin != sha256.Sum256(der) {
			t.Fatal("valid leaf rejected", err)
		}
	}
	for _, raw := range []string{"", "secret-invalid", encoded + "\n\n", " " + encoded, encoded[:20] + "\n" + encoded[20:], strings.Repeat("A", 16386), base64.StdEncoding.EncodeToString([]byte("not a certificate"))} {
		if pin, err := parseRDPCertificate(raw, time.Now()); !errors.Is(err, ErrRDPCertificate) || pin != [32]byte{} {
			t.Fatal("invalid certificate accepted")
		}
	}
	for name, change := range map[string]func(*x509.Certificate){
		"expired":               func(c *x509.Certificate) { c.NotAfter = time.Now().Add(-time.Second) },
		"expires_before_ticket": func(c *x509.Certificate) { c.NotAfter = time.Now().Add(20 * time.Second) },
		"future":                func(c *x509.Certificate) { c.NotBefore = time.Now().Add(time.Minute) },
		"client_only":           func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} },
		"ca":                    func(c *x509.Certificate) { c.IsCA = true; c.BasicConstraintsValid = true },
		"encryption_only":       func(c *x509.Certificate) { c.KeyUsage = x509.KeyUsageKeyEncipherment },
	} {
		t.Run(name, func(t *testing.T) {
			der, _ := certificateFixture(t, change)
			if _, err := parseRDPCertificate(base64.StdEncoding.EncodeToString(der), time.Now()); err == nil {
				t.Fatal("unsafe leaf accepted")
			}
		})
	}
}

type certificateExecutor func(context.Context, string, int, []string) (pve.GuestExecResult, error)

func (f certificateExecutor) ExecGuest(ctx context.Context, node string, vmid int, command []string) (pve.GuestExecResult, error) {
	return f(ctx, node, vmid, command)
}

func TestRDPCertificateTrustedCommandBoundary(t *testing.T) {
	der, _ := certificateFixture(t, nil)
	result := pve.GuestExecResult{Stdout: base64.StdEncoding.EncodeToString(der) + "\n"}
	var failure error
	calls := 0
	executor := certificateExecutor(func(ctx context.Context, node string, vmid int, argv []string) (pve.GuestExecResult, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second || node != "owned-node" || vmid != 160 || !reflect.DeepEqual(argv, []string{"/usr/bin/python3", "-I", "-c", rdpCertificateProbe}) {
			t.Fatal("unbounded or client-influenced QGA command")
		}
		return result, failure
	})
	machine := pve.VM{Node: "owned-node", VMID: 160}
	if pin, err := RDPCertificate(t.Context(), executor, machine, "linux"); err != nil || pin != sha256.Sum256(der) {
		t.Fatal("trusted observation failed", err)
	}
	for _, osFamily := range []string{"windows", "", "Linux"} {
		if _, err := RDPCertificate(t.Context(), executor, machine, osFamily); err == nil {
			t.Fatal("unsupported Guest accepted")
		}
	}
	if calls != 1 {
		t.Fatal("unsupported OS ran a Guest command")
	}
	for _, mode := range []string{"exit", "stderr", "transport", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			result.ExitCode, result.Stderr, failure = 0, "", nil
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "exit":
				result.ExitCode = 1
			case "stderr":
				result.Stderr = "private detail"
			case "transport":
				failure = errors.New("private transport detail")
			case "canceled":
				cancel()
			}
			if _, err := RDPCertificate(ctx, executor, machine, "linux"); err != ErrRDPCertificate {
				t.Fatal("error not closed/sanitized", err)
			}
		})
	}
}

// Exercise the same embedded probe against actual TLS sockets. A test-only
// function argument selects an ephemeral loopback port; production argv has no
// target arguments and always invokes discover() on 127.0.0.1:3389.
func TestRDPCertificateActualPythonTLS(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 required for certificate regression")
	}
	der, key := certificateFixture(t, nil)
	for _, mode := range []string{"tls", "hybrid", "plain_rdp", "negotiation_failure", "partial", "stalled"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
				request := make([]byte, 19)
				if _, err = io.ReadFull(conn, request); err != nil {
					done <- err
					return
				}
				if fmt.Sprintf("%x", request) != "030000130ee000000000000100080003000000" {
					done <- errors.New("probe offered unexpected negotiation/authentication")
					return
				}
				response := []byte{3, 0, 0, 19, 14, 0xd0, 0, 0, 0, 0, 0, 2, 0, 8, 0, 1, 0, 0, 0}
				switch mode {
				case "hybrid":
					response[15] = 2
				case "plain_rdp":
					response[15] = 0
				case "negotiation_failure":
					response[11] = 3
				case "partial":
					_, err = conn.Write(response[:10])
					done <- err
					return
				case "stalled":
					var one [1]byte
					_, err = conn.Read(one[:])
					if err != io.EOF {
						done <- errors.New("probe did not close stalled peer")
						return
					}
					done <- nil
					return
				}
				_, err = conn.Write(response)
				if err != nil {
					done <- err
					return
				}
				if mode != "tls" && mode != "hybrid" {
					done <- nil
					return
				}
				secure := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
				if err = secure.Handshake(); err != nil {
					done <- err
					return
				}
				var one [1]byte
				if n, err := secure.Read(one[:]); n != 0 || err == nil {
					done <- errors.New("probe sent RDP/CredSSP user data")
					return
				}
				done <- nil
			}()
			port := listener.Addr().(*net.TCPAddr).Port
			source := "__name__ = 'certificate_test'\n" + rdpCertificateProbe + fmt.Sprintf("\nprint(discover(%d))\n", port)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			start := time.Now()
			output, runErr := exec.CommandContext(ctx, python, "-I", "-c", source).Output()
			if mode == "tls" || mode == "hybrid" {
				pin, err := parseRDPCertificate(string(output), time.Now())
				if runErr != nil || err != nil || pin != sha256.Sum256(der) {
					t.Fatal("actual TLS leaf was not returned", runErr, err)
				}
			} else if runErr == nil || len(output) != 0 {
				t.Fatal("invalid negotiation returned a certificate")
			}
			if mode == "stalled" && time.Since(start) > 7*time.Second {
				t.Fatal("probe deadline was not bounded")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
