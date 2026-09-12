package httpapi

import (
	"context"
	"errors"

	"github.com/Veritas-Calculus/vc-workspace/internal/pve"
)

// Keep the complete infrastructure inventory, but classify desktop eligibility
// using both PVE metadata and the control plane's image reservations. A Packer
// VM has normal workspace tags long before PVE sets its template flag.
func (s *Server) desktopInventory(ctx context.Context) (pve.Summary, error) {
	if s.store == nil || s.pve == nil {
		return pve.Summary{}, errors.New("desktop inventory dependencies unavailable")
	}
	summary, err := s.pve.Summary(ctx)
	if err != nil {
		return pve.Summary{}, err
	}
	reserved, err := s.store.ImageReservedVMIDs(ctx)
	if err != nil {
		return pve.Summary{}, err
	}
	for i := range summary.VMs {
		summary.VMs[i].Managed = isManagedDesktop(summary.VMs[i]) && !reserved[summary.VMs[i].VMID]
	}
	return summary, nil
}
