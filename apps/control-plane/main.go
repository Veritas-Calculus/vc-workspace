package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/virtual-cable/vc-vdi/internal/config"
	"github.com/virtual-cable/vc-vdi/internal/httpapi"
	"github.com/virtual-cable/vc-vdi/internal/imagebuilder"
	"github.com/virtual-cable/vc-vdi/internal/oidcauth"
	"github.com/virtual-cable/vc-vdi/internal/pve"
	"github.com/virtual-cable/vc-vdi/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration is invalid", "error", err)
		os.Exit(1)
	}

	var pveClient *pve.Client
	if cfg.PVE.Endpoint != "" {
		pveClient, err = pve.New(cfg.PVE)
		if err != nil {
			logger.Error("PVE client configuration is invalid", "error", err)
			os.Exit(1)
		}
	}

	var database *store.Store
	if cfg.DatabaseURL != "" {
		database, err = store.Open(context.Background(), cfg.DatabaseURL)
		if err != nil {
			logger.Error("database connection failed", "error", err)
			os.Exit(1)
		}
		defer database.Close()
		if err := database.Migrate(context.Background()); err != nil {
			logger.Error("database migration failed", "error", err)
			os.Exit(1)
		}
	}

	var oidcService *oidcauth.Service
	if cfg.OIDC != nil {
		oidcService, err = oidcauth.New(context.Background(), *cfg.OIDC)
		if err != nil {
			logger.Warn("OIDC provider is unavailable; local login remains enabled", "error", err)
		} else if database == nil {
			logger.Warn("OIDC requires the database; local login remains enabled")
			oidcService = nil
		} else if err := database.EnsureOIDCProvider(context.Background(), oidcauth.ProviderID, oidcService.Name(), oidcService.Issuer(), oidcService.ClientID()); err != nil {
			logger.Warn("OIDC provider could not be registered; local login remains enabled", "error", err)
			oidcService = nil
		}
	}

	var imageBuilder *imagebuilder.Builder
	if cfg.ImageBuilder.Enabled {
		imageBuilder, err = imagebuilder.New(cfg.ImageBuilder)
		if err != nil {
			logger.Warn("image builder is unavailable", "error", err)
			imageBuilder = nil
		}
	}

	api := httpapi.New(httpapi.Dependencies{
		Logger:            logger,
		PVE:               pveClient,
		Store:             database,
		PublicURL:         cfg.PublicURL,
		SetupToken:        cfg.SetupToken,
		InternalAPIToken:  cfg.InternalAPIToken,
		NativeCallbackURL: cfg.NativeCallbackURL,
		OIDC:              oidcService,
		ImageBuilder:      imageBuilder,
	})
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      75 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		logger.Info("control plane listening", "address", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped unexpectedly", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown failed", "error", err)
	}
}
