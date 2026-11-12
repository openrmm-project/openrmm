package store

import "context"

// PatchAuditTargetLabels names the patches that audit entries point at by id
// (patch.approve, patch.decline): the title, or the identifier when the update
// mechanism gave none. Display only. Call after authorizing the audit log.
func (s *Store) PatchAuditTargetLabels(ctx context.Context, ids []string) (map[string]string, error) {
	labels := map[string]string{}
	if len(ids) == 0 {
		return labels, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, COALESCE(NULLIF(title,''), identifier) FROM patches WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, label string
		if err := rows.Scan(&id, &label); err != nil {
			return nil, err
		}
		labels[id] = label
	}
	return labels, rows.Err()
}
