package httpapi

import (
	"context"
	"time"
)

// RunPVEJobMaintenance resumes durable clone handles after process loss. It is
// independent of credential revocation, so slow PVE I/O cannot delay logout.
func (s *Server) RunPVEJobMaintenance(ctx context.Context) {
	if s.store == nil || s.pve == nil {
		return
	}
	cursor := ""
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		ids, err := s.store.PendingCloneJobIDs(ctx, cursor, 32)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("scan pending clone jobs", "error", err)
			}
		} else {
			if len(ids) == 0 {
				cursor = ""
			}
			for _, id := range ids {
				if ctx.Err() != nil {
					return
				}
				cursor = id
				jobCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				_, err := s.refreshPVEJob(jobCtx, id)
				cancel()
				if err != nil && ctx.Err() == nil {
					s.logger.Warn("resume pending clone job", "job_id", id, "error", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
