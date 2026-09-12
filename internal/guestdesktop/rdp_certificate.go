package guestdesktop

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

//go:embed rdp_certificate.py
var rdpCertificateProbe string

var ErrRDPCertificate = errors.New("trusted Guest RDP certificate unavailable")

type guestExecutor interface {
	ExecGuest(context.Context, string, int, []string) (pve.GuestExecResult, error)
}

// RDPCertificate observes the actual listener inside the managed Linux Guest,
// rather than trusting a network probe from the Broker or a stale config file.
// This is read-only, never sends OS credentials, and requires a trusted PVE/QGA
// channel. Windows is deliberately unavailable until its gateway lifecycle is
// integrated; there is no fallback to an unpinned connection.
func RDPCertificate(ctx context.Context, guest guestExecutor, machine pve.VM, osFamily string) ([32]byte, error) {
	if guest == nil || osFamily != "linux" || machine.Node == "" || machine.VMID <= 0 {
		return [32]byte{}, ErrRDPCertificate
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := guest.ExecGuest(ctx, machine.Node, machine.VMID, []string{"/usr/bin/python3", "-I", "-c", rdpCertificateProbe})
	if err != nil || ctx.Err() != nil || result.ExitCode != 0 || result.Stderr != "" {
		return [32]byte{}, ErrRDPCertificate
	}
	return parseRDPCertificate(result.Stdout, time.Now())
}

func parseRDPCertificate(raw string, now time.Time) ([32]byte, error) {
	if len(raw) > 16385 {
		return [32]byte{}, ErrRDPCertificate
	}
	raw = strings.TrimSuffix(raw, "\n")
	der, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil || len(der) == 0 || base64.StdEncoding.EncodeToString(der) != raw {
		return [32]byte{}, ErrRDPCertificate
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil || now.Before(cert.NotBefore) || !now.Add(30*time.Second).Before(cert.NotAfter) ||
		len(cert.UnhandledCriticalExtensions) != 0 || cert.IsCA ||
		(cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageDigitalSignature == 0) {
		return [32]byte{}, ErrRDPCertificate
	}
	if len(cert.ExtKeyUsage) != 0 || len(cert.UnknownExtKeyUsage) != 0 {
		server := false
		for _, usage := range cert.ExtKeyUsage {
			server = server || usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny
		}
		if !server {
			return [32]byte{}, ErrRDPCertificate
		}
	}
	return sha256.Sum256(der), nil
}
