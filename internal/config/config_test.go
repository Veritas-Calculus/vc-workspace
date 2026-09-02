package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCredentialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pve")
	if err := os.WriteFile(path, []byte("operator@pam / correct-horse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	username, password, err := readCredentialFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if username != "operator@pam" || password != "correct-horse" {
		t.Fatalf("unexpected parsed credential: %q, %q", username, password)
	}
}

func TestReadCredentialFileRejectsUnknownShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pve")
	if err := os.WriteFile(path, []byte("operator@pam:secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readCredentialFile(path); err == nil {
		t.Fatal("expected malformed credential to be rejected")
	}
}

func TestLoadRejectsPartialOIDCConfiguration(t *testing.T) {
	t.Setenv("VC_VDI_OIDC_ISSUER", "https://issuer.example")
	t.Setenv("VC_VDI_OIDC_CLIENT_ID", "")
	t.Setenv("VC_VDI_OIDC_CLIENT_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected partial OIDC configuration to be rejected")
	}
}

func TestLoadBuildsOIDCRedirectFromPublicURL(t *testing.T) {
	t.Setenv("VC_VDI_PUBLIC_URL", "https://vdi.example")
	t.Setenv("VC_VDI_OIDC_ISSUER", "https://issuer.example/")
	t.Setenv("VC_VDI_OIDC_CLIENT_ID", "vc-vdi")
	t.Setenv("VC_VDI_OIDC_CLIENT_SECRET", "secret")
	t.Setenv("VC_VDI_OIDC_REDIRECT_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDC == nil || cfg.OIDC.RedirectURL != "https://vdi.example/api/v1/auth/oidc/callback" {
		t.Fatalf("unexpected OIDC configuration: %#v", cfg.OIDC)
	}
}

func TestLoadEnablesImageBuilderWithPVEWritesAndSupportsDedicatedToken(t *testing.T) {
	t.Setenv("VC_VDI_PVE_ENDPOINT", "https://pve.example.com")
	t.Setenv("VC_VDI_PVE_TOKEN_ID", "control@pve!api")
	t.Setenv("VC_VDI_PVE_TOKEN_SECRET", "control-secret")
	t.Setenv("VC_VDI_PVE_MUTATIONS_ENABLED", "true")
	t.Setenv("VC_VDI_IMAGE_BUILDER_PVE_TOKEN_ID", "builder@pve!packer")
	t.Setenv("VC_VDI_IMAGE_BUILDER_PVE_TOKEN_SECRET", "builder-secret")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ImageBuilder.Enabled || cfg.ImageBuilder.PVETokenID != "builder@pve!packer" || cfg.ImageBuilder.PVETokenSecret != "builder-secret" {
		t.Fatalf("unexpected image builder configuration: %#v", cfg.ImageBuilder)
	}
}

func TestLoadAllowsImageBuilderToBeDisabledSeparately(t *testing.T) {
	t.Setenv("VC_VDI_PVE_MUTATIONS_ENABLED", "true")
	t.Setenv("VC_VDI_IMAGE_BUILDER_ENABLED", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ImageBuilder.Enabled {
		t.Fatal("expected image builder to be disabled")
	}
}
