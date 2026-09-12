package store

import "context"

// PendingCloneJobIDs uses keyset pagination so an old blocked task cannot hide
// newer work. Accepted jobs without a verified handle are never auto-adopted.
func (s *Store) PendingCloneJobIDs(ctx context.Context, after string, limit int) ([]string, error) {
	if limit < 1 || limit > 100 {
		limit = 32
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM pve_jobs WHERE operation='pve.template_clone' AND state='running' AND upid IS NOT NULL AND upid<>'' AND id>$1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
