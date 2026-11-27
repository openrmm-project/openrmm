package store

import (
	"context"
	"github.com/jackc/pgx/v5"
	"log"
)

func (s *Store) addAuditTx(ctx context.Context, tx pgx.Tx, actor, action, target string, detail []byte) error {
	if detail == nil {
		detail = []byte("{}")
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (id,actor,action,target,detail) VALUES ($1,$2,$3,$4,$5)`, newID(), actor, action, target, detail)
	if err != nil {
		auditWriteFailures.Add(1)
		log.Printf("audit: FAILED to record %q by %q on %q: %v", action, actor, target, err)
	}
	return err
}
