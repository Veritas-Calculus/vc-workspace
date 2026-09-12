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
	t.Setenv("VC_WORKSPACE_OIDC_ISSUER", "https://issuer.example")
	t.Setenv("VC_WORKSPACE_OIDC_CLIENT_ID", "")
	t.Setenv("VC_WORKSPACE_OIDC_CLIENT_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected partial OIDC configuration to be rejected")
	}
}

func TestLoadBuildsOIDCRedirectFromPublicURL(t *testing.T) {
	t.Setenv("VC_WORKSPACE_PUBLIC_URL", "https://workspace.example")
	t.Setenv("VC_WORKSPACE_OIDC_ISSUER", "https://issuer.example/")
	t.Setenv("VC_WORKSPACE_OIDC_CLIENT_ID", "vc-workspace")
	t.Setenv("VC_WORKSPACE_OIDC_CLIENT_SECRET", "secret")
	t.Setenv("VC_WORKSPACE_OIDC_REDIRECT_URL", "")
	t.Setenv("VC_WORKSPACE_OIDC_GROUPS_CLAIM", "roles")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDC == nil || cfg.OIDC.RedirectURL != "https://workspace.example/api/v1/auth/oidc/callback" {
		t.Fatalf("unexpected OIDC configuration: %#v", cfg.OIDC)
	}
	if cfg.OIDC.GroupsClaim != "roles" {
		t.Fatalf("unexpected OIDC groups claim: %q", cfg.OIDC.GroupsClaim)
	}
}

func TestLoadIdentityFeatureGatesDefaultOffAndRequireExplicitTrue(t *testing.T) {
	t.Setenv("VC_WORKSPACE_EXPERIMENTAL_SSSD_OIDC_ENABLED", "")
	t.Setenv("VC_WORKSPACE_EXPERIMENTAL_WINDOWS_ENTRA_RDP_ENABLED", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdentityFeatures.SSSDOIDCEnabled || cfg.IdentityFeatures.WindowsEntraEnabled {
		t.Fatalf("experimental identity features must default off: %#v", cfg.IdentityFeatures)
	}
	t.Setenv("VC_WORKSPACE_EXPERIMENTAL_SSSD_OIDC_ENABLED", "true")
	t.Setenv("VC_WORKSPACE_EXPERIMENTAL_WINDOWS_ENTRA_RDP_ENABLED", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IdentityFeatures.SSSDOIDCEnabled || !cfg.IdentityFeatures.WindowsEntraEnabled {
		t.Fatalf("explicit experimental identity features were not enabled: %#v", cfg.IdentityFeatures)
	}
}

func TestLoadEnablesImageBuilderWithPVEWritesAndSupportsDedicatedToken(t *testing.T) {
	t.Setenv("VC_WORKSPACE_PVE_ENDPOINT", "https://pve.example.com")
	t.Setenv("VC_WORKSPACE_PVE_TOKEN_ID", "control@pve!api")
	t.Setenv("VC_WORKSPACE_PVE_TOKEN_SECRET", "control-secret")
	t.Setenv("VC_WORKSPACE_PVE_MUTATIONS_ENABLED", "true")
	t.Setenv("VC_WORKSPACE_IMAGE_BUILDER_PVE_TOKEN_ID", "builder@pve!packer")
	t.Setenv("VC_WORKSPACE_IMAGE_BUILDER_PVE_TOKEN_SECRET", "builder-secret")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ImageBuilder.Enabled || cfg.ImageBuilder.PVETokenID != "builder@pve!packer" || cfg.ImageBuilder.PVETokenSecret != "builder-secret" {
		t.Fatalf("unexpected image builder configuration: %#v", cfg.ImageBuilder)
	}
}

func TestLoadAllowsImageBuilderToBeDisabledSeparately(t *testing.T) {
	t.Setenv("VC_WORKSPACE_PVE_MUTATIONS_ENABLED", "true")
	t.Setenv("VC_WORKSPACE_IMAGE_BUILDER_ENABLED", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ImageBuilder.Enabled {
		t.Fatal("expected image builder to be disabled")
	}
}

func TestLoadSupportsReviewedDebianXRDPBundle(t *testing.T) {
	t.Setenv("VC_WORKSPACE_LINUX_XRDP_BUNDLE", "")
	cfg, err := Load()
	if err != nil || cfg.ImageBuilder.LinuxXRDPBundle != "dist/debian-13-xrdp" {
		t.Fatal("missing canonical xrdp bundle default", err)
	}
	t.Setenv("VC_WORKSPACE_LINUX_XRDP_BUNDLE", "/reviewed/debian-runtime")
	cfg, err = Load()
	if err != nil || cfg.ImageBuilder.LinuxXRDPBundle != "/reviewed/debian-runtime" {
		t.Fatal("explicit xrdp bundle was not loaded", err)
	}
}

func TestLoadPrefersWorkspaceEnvironmentAndUsesWorkspaceDefaults(t *testing.T) {
	t.Setenv("VC_WORKSPACE_PUBLIC_URL", "https://workspace.example")
	t.Setenv("VC_VDI_PUBLIC_URL", "https://legacy.example")
	t.Setenv("VC_WORKSPACE_PVE_MUTATIONS_ENABLED", "true")
	t.Setenv("VC_VDI_PVE_MUTATIONS_ENABLED", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://workspace.example" || !cfg.PVE.MutationsEnabled {
		t.Fatalf("current environment must take precedence: %#v", cfg)
	}
	if cfg.NativeCallbackURL != "vc-workspace://auth/callback" || cfg.ImageBuilder.LinuxAgentBinary != "dist/vc-workspace-guest-agent-linux-amd64" {
		t.Fatalf("unexpected VC Workspace defaults: %#v", cfg)
	}
}

func TestLoadReadsLegacyEnvironmentDuringMigration(t *testing.T) {
	t.Setenv("VC_VDI_PUBLIC_URL", "https://legacy.example")
	t.Setenv("VC_WORKSPACE_PUBLIC_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://legacy.example" {
		t.Fatalf("legacy environment was not read: %#v", cfg)
	}
}
