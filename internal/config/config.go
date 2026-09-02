package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/virtual-cable/vc-vdi/internal/imagebuilder"
	"github.com/virtual-cable/vc-vdi/internal/oidcauth"
	"github.com/virtual-cable/vc-vdi/internal/pve"
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
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:          envOr("VC_VDI_HTTP_ADDR", "127.0.0.1:8080"),
		DatabaseURL:       os.Getenv("VC_VDI_DATABASE_URL"),
		PublicURL:         envOr("VC_VDI_PUBLIC_URL", "http://127.0.0.1:5173"),
		SetupToken:        os.Getenv("VC_VDI_SETUP_TOKEN"),
		InternalAPIToken:  os.Getenv("VC_VDI_INTERNAL_API_TOKEN"),
		NativeCallbackURL: envOr("VC_VDI_NATIVE_CALLBACK_URL", "vc-vdi://auth/callback"),
		PVE: pve.Config{
			Endpoint:         strings.TrimRight(os.Getenv("VC_VDI_PVE_ENDPOINT"), "/"),
			TokenID:          os.Getenv("VC_VDI_PVE_TOKEN_ID"),
			TokenSecret:      os.Getenv("VC_VDI_PVE_TOKEN_SECRET"),
			MutationsEnabled: os.Getenv("VC_VDI_PVE_MUTATIONS_ENABLED") == "true",
		},
	}

	if path := os.Getenv("VC_VDI_PVE_CREDENTIAL_FILE"); path != "" {
		username, password, err := readCredentialFile(path)
		if err != nil {
			return Config{}, err
		}
		cfg.PVE.Username = username
		cfg.PVE.Password = password
	}

	cfg.ImageBuilder = imagebuilder.Config{
		Enabled:            cfg.PVE.MutationsEnabled && os.Getenv("VC_VDI_IMAGE_BUILDER_ENABLED") != "false",
		PackerPath:         envOr("VC_VDI_PACKER_PATH", "packer"),
		RootDir:            envOr("VC_VDI_IMAGE_ROOT", "deploy/images"),
		PVEEndpoint:        cfg.PVE.Endpoint,
		PVEUsername:        cfg.PVE.Username,
		PVEPassword:        cfg.PVE.Password,
		PVETokenID:         cfg.PVE.TokenID,
		PVETokenSecret:     cfg.PVE.TokenSecret,
		LinuxAgentBinary:   envOr("VC_VDI_LINUX_AGENT_BINARY", "dist/vc-vdi-guest-agent-linux-amd64"),
		WindowsAgentBinary: envOr("VC_VDI_WINDOWS_AGENT_BINARY", "dist/vc-vdi-guest-agent-windows-amd64.exe"),
		CloudbaseInitMSI:   envOr("VC_VDI_CLOUDBASE_INIT_MSI", "dist/CloudbaseInitSetup.msi"),
	}
	if tokenID, tokenSecret := os.Getenv("VC_VDI_IMAGE_BUILDER_PVE_TOKEN_ID"), os.Getenv("VC_VDI_IMAGE_BUILDER_PVE_TOKEN_SECRET"); tokenID != "" || tokenSecret != "" {
		cfg.ImageBuilder.PVETokenID, cfg.ImageBuilder.PVETokenSecret = tokenID, tokenSecret
		cfg.ImageBuilder.PVEUsername, cfg.ImageBuilder.PVEPassword = "", ""
	}
	if path := os.Getenv("VC_VDI_IMAGE_BUILDER_PVE_CREDENTIAL_FILE"); path != "" {
		username, password, err := readCredentialFile(path)
		if err != nil {
			return Config{}, err
		}
		cfg.ImageBuilder.PVEUsername, cfg.ImageBuilder.PVEPassword = username, password
		cfg.ImageBuilder.PVETokenID, cfg.ImageBuilder.PVETokenSecret = "", ""
	}

	oidcIssuer := strings.TrimRight(os.Getenv("VC_VDI_OIDC_ISSUER"), "/")
	oidcClientID := os.Getenv("VC_VDI_OIDC_CLIENT_ID")
	oidcClientSecret := os.Getenv("VC_VDI_OIDC_CLIENT_SECRET")
	if oidcIssuer != "" || oidcClientID != "" || oidcClientSecret != "" {
		if oidcIssuer == "" || oidcClientID == "" || oidcClientSecret == "" {
			return Config{}, fmt.Errorf("OIDC issuer, client ID, and client secret must be configured together")
		}
		cfg.OIDC = &oidcauth.Config{
			Name:         envOr("VC_VDI_OIDC_NAME", "Organization SSO"),
			Issuer:       oidcIssuer,
			ClientID:     oidcClientID,
			ClientSecret: oidcClientSecret,
			RedirectURL:  envOr("VC_VDI_OIDC_REDIRECT_URL", cfg.PublicURL+"/api/v1/auth/oidc/callback"),
		}
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

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
