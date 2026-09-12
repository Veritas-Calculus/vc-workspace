package rdpsession

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"testing"
	"time"
)

// A local fake RDP peer reaches TLS but never authenticates any OS account.
// This tests the actual native binary, not the Go process double. The private
// bind is explicit because the production transport forbids loopback targets.
func TestNativeWorkerGuards(t *testing.T) {
	executable := os.Getenv("VC_WORKSPACE_TEST_SESSION_WORKER")
	if executable == "" {
		t.Skip("opt-in compiled native worker guards")
	}
	bind := os.Getenv("VC_WORKSPACE_TEST_SESSION_WORKER_BIND")
	if bind == "auto" {
		// Only for an isolated test container: select one local interface,
		// never contact a discovery service or another machine.
		interfaces, err := net.InterfaceAddrs()
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range interfaces {
			prefix, err := netip.ParsePrefix(item.String())
			if err == nil && prefix.Addr().Is4() && prefix.Addr().IsPrivate() {
				bind = prefix.Addr().String()
				break
			}
		}
	}
	address, err := netip.ParseAddr(bind)
	if err != nil || !address.Is4() || !address.IsPrivate() {
		t.Fatal("explicit local private IPv4 bind required")
	}
	c := testConfig(t)
	c.Executable = executable
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.IP(address.AsSlice())}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("wrong_certificate", func(t *testing.T) {
		listener, err := net.Listen("tcp4", net.JoinHostPort(address.String(), "0"))
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		peerDone := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				peerDone <- err
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(8 * time.Second))
			var header [4]byte
			if _, err = io.ReadFull(conn, header[:]); err != nil {
				peerDone <- err
				return
			}
			size := binary.BigEndian.Uint16(header[2:])
			if header[0] != 3 || size < 11 || size > 512 {
				peerDone <- ErrProtocol
				return
			}
			if _, err = io.CopyN(io.Discard, conn, int64(size)-4); err != nil {
				peerDone <- err
				return
			}
			// X.224 confirm + RDP_NEG_RSP selecting NLA (HYBRID), no fallback.
			_, err = conn.Write([]byte{3, 0, 0, 19, 14, 0xd0, 0, 0, 0, 0, 0, 2, 0, 8, 0, 2, 0, 0, 0})
			if err != nil {
				peerDone <- err
				return
			}
			secure := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
			if err = secure.Handshake(); err != nil {
				peerDone <- err
				return
			}
			// Pin rejection must close before any CredSSP authentication token.
			var data [1]byte
			n, err := secure.Read(data[:])
			if n != 0 || err == nil {
				peerDone <- ErrProtocol
				return
			}
			peerDone <- nil
		}()
		probe := c
		probe.Endpoint = netip.MustParseAddrPort(listener.Addr().String())
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		s, err := Start(ctx, probe)
		if s != nil {
			s.Stop()
			t.Fatal("untrusted peer connected")
		}
		if !errors.Is(err, ErrCertificate) {
			t.Fatal("pin rejection not enforced by native worker", err)
		}
		if err := <-peerDone; err != nil {
			t.Fatal("peer observed unexpected auth or handshake", err)
		}
	})
	for _, mode := range []string{"pipe_eof", "extra_input", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp4", net.JoinHostPort(address.String(), "0"))
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				conn, err := listener.Accept()
				if err == nil {
					accepted <- conn
				}
			}()
			probe := c
			probe.Endpoint = netip.MustParseAddrPort(listener.Addr().String())
			probe.ExpiresAt = time.Now().Add(3 * time.Second)
			payload, err := frame(probe, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			defer clear(payload)
			ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable)
			runtimeDir := t.TempDir()
			if err := os.Chmod(runtimeDir, 0700); err != nil {
				t.Fatal(err)
			}
			command.Env, err = workerEnvironment(runtimeDir)
			if err != nil {
				t.Fatal(err)
			}
			pipe, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer pipe.Close()
			if err = command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var waitErr error
			go func() { waitErr = command.Wait(); close(done) }()
			t.Cleanup(func() {
				pipe.Close()
				select {
				case <-done:
					return
				default:
					command.Process.Kill()
					<-done
				}
			})
			if _, err = pipe.Write(payload); err != nil {
				t.Fatal(err)
			}
			select {
			case conn := <-accepted:
				defer conn.Close()
			case <-done:
				t.Fatal("native worker did not connect to private peer", waitErr)
			case <-ctx.Done():
				t.Fatal("native connect timed out")
			}
			if mode == "pipe_eof" {
				pipe.Close()
			}
			if mode == "extra_input" {
				pipe.Write([]byte{1})
			}
			select {
			case <-done:
				if ctx.Err() != nil {
					t.Fatal("native lifetime guard required external kill")
				}
			case <-ctx.Done():
				t.Fatal("native lifetime guard did not terminate pending handshake")
			}
		})
	}
}
