package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/Veritas-Calculus/vc-workspace/internal/gateway"
	"github.com/Veritas-Calculus/vc-workspace/internal/imagebuilder"
	"github.com/Veritas-Calculus/vc-workspace/internal/oidcauth"
	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

type Config struct {
	HTTPAddr          string
	DatabaseURL       string
	PublicURL         string
	SetupToken        string
	InternalAPIToken  string
	NativeCallbackURL string
	OIDC              *oidcauth.Config
	PVE               pve.Config
	ImageBuilder      imagebuilder.Config
	IdentityFeatures  IdentityFeatures
	GatewayControl    *gateway.ControlListenerConfig
	NativeGateway     *gateway.BrokerRoute
}

type IdentityFeatures struct {
	SSSDOIDCEnabled     bool
	WindowsEntraEnabled bool
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:          envOr("VC_WORKSPACE_HTTP_ADDR", "VC_VDI_HTTP_ADDR", "127.0.0.1:8080"),
		DatabaseURL:       env("VC_WORKSPACE_DATABASE_URL", "VC_VDI_DATABASE_URL"),
		PublicURL:         envOr("VC_WORKSPACE_PUBLIC_URL", "VC_VDI_PUBLIC_URL", "http://127.0.0.1:5173"),
		SetupToken:        env("VC_WORKSPACE_SETUP_TOKEN", "VC_VDI_SETUP_TOKEN"),
		InternalAPIToken:  env("VC_WORKSPACE_INTERNAL_API_TOKEN", "VC_VDI_INTERNAL_API_TOKEN"),
		NativeCallbackURL: envOr("VC_WORKSPACE_NATIVE_CALLBACK_URL", "VC_VDI_NATIVE_CALLBACK_URL", "vc-workspace://auth/callback"),
		PVE: pve.Config{
			Endpoint:         strings.TrimRight(env("VC_WORKSPACE_PVE_ENDPOINT", "VC_VDI_PVE_ENDPOINT"), "/"),
			TokenID:          env("VC_WORKSPACE_PVE_TOKEN_ID", "VC_VDI_PVE_TOKEN_ID"),
			TokenSecret:      env("VC_WORKSPACE_PVE_TOKEN_SECRET", "VC_VDI_PVE_TOKEN_SECRET"),
			MutationsEnabled: env("VC_WORKSPACE_PVE_MUTATIONS_ENABLED", "VC_VDI_PVE_MUTATIONS_ENABLED") == "true",
		},
		IdentityFeatures: IdentityFeatures{
			SSSDOIDCEnabled:     env("VC_WORKSPACE_EXPERIMENTAL_SSSD_OIDC_ENABLED", "VC_VDI_EXPERIMENTAL_SSSD_OIDC_ENABLED") == "true",
			WindowsEntraEnabled: env("VC_WORKSPACE_EXPERIMENTAL_WINDOWS_ENTRA_RDP_ENABLED", "VC_VDI_EXPERIMENTAL_WINDOWS_ENTRA_RDP_ENABLED") == "true",
		},
	}

	if path := env("VC_WORKSPACE_PVE_CREDENTIAL_FILE", "VC_VDI_PVE_CREDENTIAL_FILE"); path != "" {
		username, password, err := readCredentialFile(path)
		if err != nil {
			return Config{}, err
		}
		cfg.PVE.Username = username
		cfg.PVE.Password = password
	}

	cfg.ImageBuilder = imagebuilder.Config{
		Enabled:              cfg.PVE.MutationsEnabled && env("VC_WORKSPACE_IMAGE_BUILDER_ENABLED", "VC_VDI_IMAGE_BUILDER_ENABLED") != "false",
		PackerPath:           envOr("VC_WORKSPACE_PACKER_PATH", "VC_VDI_PACKER_PATH", "packer"),
		RootDir:              envOr("VC_WORKSPACE_IMAGE_ROOT", "VC_VDI_IMAGE_ROOT", "deploy/images"),
		PVEEndpoint:          cfg.PVE.Endpoint,
		PVEUsername:          cfg.PVE.Username,
		PVEPassword:          cfg.PVE.Password,
		PVETokenID:           cfg.PVE.TokenID,
		PVETokenSecret:       cfg.PVE.TokenSecret,
		LinuxAgentBinary:     envOr("VC_WORKSPACE_LINUX_AGENT_BINARY", "VC_VDI_LINUX_AGENT_BINARY", "dist/vc-workspace-guest-agent-linux-amd64"),
		LinuxPAMModule:       os.Getenv("VC_WORKSPACE_LINUX_PAM_MODULE"),
		LinuxNativePAMModule: os.Getenv("VC_WORKSPACE_LINUX_NATIVE_PAM_MODULE"),
		LinuxXRDPBundle:      envOr("VC_WORKSPACE_LINUX_XRDP_BUNDLE", "", "dist/debian-13-xrdp"),
		HTTPBindAddress:      os.Getenv("VC_WORKSPACE_IMAGE_HTTP_BIND_ADDRESS"),
		HTTPInterface:        os.Getenv("VC_WORKSPACE_IMAGE_HTTP_INTERFACE"),
		HTTPPortRange:        envOr("VC_WORKSPACE_IMAGE_HTTP_PORT_RANGE", "", "8840-8847"),
		WindowsAgentBinary:   envOr("VC_WORKSPACE_WINDOWS_AGENT_BINARY", "VC_VDI_WINDOWS_AGENT_BINARY", "dist/vc-workspace-guest-agent-windows-amd64.exe"),
		CloudbaseInitMSI:     envOr("VC_WORKSPACE_CLOUDBASE_INIT_MSI", "VC_VDI_CLOUDBASE_INIT_MSI", "dist/CloudbaseInitSetup.msi"),
	}
	if tokenID, tokenSecret := env("VC_WORKSPACE_IMAGE_BUILDER_PVE_TOKEN_ID", "VC_VDI_IMAGE_BUILDER_PVE_TOKEN_ID"), env("VC_WORKSPACE_IMAGE_BUILDER_PVE_TOKEN_SECRET", "VC_VDI_IMAGE_BUILDER_PVE_TOKEN_SECRET"); tokenID != "" || tokenSecret != "" {
		cfg.ImageBuilder.PVETokenID, cfg.ImageBuilder.PVETokenSecret = tokenID, tokenSecret
		cfg.ImageBuilder.PVEUsername, cfg.ImageBuilder.PVEPassword = "", ""
	}
	if path := env("VC_WORKSPACE_IMAGE_BUILDER_PVE_CREDENTIAL_FILE", "VC_VDI_IMAGE_BUILDER_PVE_CREDENTIAL_FILE"); path != "" {
		username, password, err := readCredentialFile(path)
		if err != nil {
			return Config{}, err
		}
		cfg.ImageBuilder.PVEUsername, cfg.ImageBuilder.PVEPassword = username, password
		cfg.ImageBuilder.PVETokenID, cfg.ImageBuilder.PVETokenSecret = "", ""
	}

	oidcIssuer := strings.TrimRight(env("VC_WORKSPACE_OIDC_ISSUER", "VC_VDI_OIDC_ISSUER"), "/")
	oidcClientID := env("VC_WORKSPACE_OIDC_CLIENT_ID", "VC_VDI_OIDC_CLIENT_ID")
	oidcClientSecret := env("VC_WORKSPACE_OIDC_CLIENT_SECRET", "VC_VDI_OIDC_CLIENT_SECRET")
	if oidcIssuer != "" || oidcClientID != "" || oidcClientSecret != "" {
		if oidcIssuer == "" || oidcClientID == "" || oidcClientSecret == "" {
			return Config{}, fmt.Errorf("OIDC issuer, client ID, and client secret must be configured together")
		}
		cfg.OIDC = &oidcauth.Config{
			Name:         envOr("VC_WORKSPACE_OIDC_NAME", "VC_VDI_OIDC_NAME", "Organization SSO"),
			Issuer:       oidcIssuer,
			ClientID:     oidcClientID,
			ClientSecret: oidcClientSecret,
			RedirectURL:  envOr("VC_WORKSPACE_OIDC_REDIRECT_URL", "VC_VDI_OIDC_REDIRECT_URL", cfg.PublicURL+"/api/v1/auth/oidc/callback"),
			GroupsClaim:  envOr("VC_WORKSPACE_OIDC_GROUPS_CLAIM", "VC_VDI_OIDC_GROUPS_CLAIM", "groups"),
		}
	}

	var err error
	cfg.GatewayControl, err = gateway.LoadControlListenerConfig()
	if err != nil || (cfg.GatewayControl != nil && cfg.DatabaseURL == "") {
		return Config{}, fmt.Errorf("Gateway control listener requires valid TLS configuration and a database")
	}
	cfg.NativeGateway, err = gateway.LoadBrokerRoute(cfg.GatewayControl)
	if err != nil {
		return Config{}, fmt.Errorf("Native Gateway routing requires a valid registered Gateway and private target ranges")
	}
	return cfg, nil
}

func readCredentialFile(path string) (string, string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read PVE credential file: %w", err)
	}
	fields := strings.Fields(string(contents))
	if len(fields) != 3 || fields[1] != "/" {
		return "", "", fmt.Errorf("PVE credential file must contain 'username@realm / password'")
	}
	return fields[0], fields[2], nil
}

func env(primary, legacy string) string {
	if value := os.Getenv(primary); value != "" {
		return value
	}
	return os.Getenv(legacy)
}

func envOr(primary, legacy, fallback string) string {
	if value := env(primary, legacy); value != "" {
		return value
	}
	return fallback
}
