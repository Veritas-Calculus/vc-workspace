package store

import "context"

// ImageReservedVMIDs keeps template candidates out of the desktop data plane.
// Failed/unfinished builds may leave a VM behind even after a profile is edited;
// retaining their reservation is safer than adopting it as a user's desktop.
func (s *Store) ImageReservedVMIDs(ctx context.Context) (map[int]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT template_vmid FROM image_profiles WHERE template_vmid > 0
		UNION
		SELECT target_vmid FROM pve_jobs
		WHERE operation='image.build' AND state IN ('accepted','running','failed') AND target_vmid > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reserved := make(map[int]bool)
	for rows.Next() {
		var vmid int
		if err := rows.Scan(&vmid); err != nil {
			return nil, err
		}
		reserved[vmid] = true
	}
	return reserved, rows.Err()
}
