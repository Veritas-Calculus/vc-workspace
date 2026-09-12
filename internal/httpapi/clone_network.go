package httpapi

import (
	"context"
	"errors"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
	"github.com/Veritas-Calculus/vc-workspace/internal/store"
)

func (s *Server) ensureCloneNetwork(ctx context.Context, job store.Job, request desktopCloneRequest) error {
	if request.SourceOSFamily != "linux" {
		return nil
	}
	cfg, err := s.pve.VMConfiguration(ctx, job.TargetNode, job.TargetVMID)
	if err != nil {
		return err
	}
	if cfg.Template || cfg.Lock != "" || cfg.Name != request.Name || cfg.Description != "VC Workspace clone job: "+job.ID {
		return errors.New("clone network target is not verified")
	}
	if !cfg.HasCloudInitDisk() || cfg.IPConfigs["ipconfig0"] != "" {
		return nil
	}
	if cfg.NetworkAdapters["net0"] == "" || !pve.ValidConfigurationDigest(cfg.Digest) {
		return errors.New("clone network precondition is not verified")
	}
	if err := s.pve.ConfigureVMPrimaryDHCP(ctx, job.TargetNode, job.TargetVMID, cfg.Digest); err != nil {
		return err
	}
	updated, err := s.pve.VMConfiguration(ctx, job.TargetNode, job.TargetVMID)
	if err != nil {
		return err
	}
	if updated.IPConfigs["ipconfig0"] != "ip=dhcp" {
		return errors.New("clone DHCP configuration was not observed")
	}
	return nil
}
