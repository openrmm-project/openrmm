package store

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	migrationLockID           int64 = 0x524d4d5f4d494752 // "RMM_MIGR"
	migrationLockTimeout            = 30 * time.Second
	migrationStatementTimeout       = 4 * time.Minute
	migrationRollbackTimeout        = 5 * time.Second
)

type migration struct {
	version int64
	name    string
	sql     string
}

type appliedMigration struct {
	version  int64
	name     string
	checksum *string
}

//go:embed migrations/001_baseline.sql
var migrationV1SQL string

//go:embed migrations/002_pending_totp_secret.sql
var migrationV2SQL string

//go:embed migrations/003_email_notifications.sql
var migrationV3SQL string

var migrations = []migration{
	{version: 1, name: "baseline", sql: migrationV1SQL},
	{version: 2, name: "pending_totp_secret", sql: migrationV2SQL},
	{version: 3, name: "email_notifications", sql: migrationV3SQL},
}

// Migrate applies pending migrations atomically. The transaction-scoped lock
// serializes startup across server replicas and is released on commit or rollback.
func (s *Store) Migrate(ctx context.Context) (err error) {
	return s.migrate(ctx, migrations)
}

func (s *Store) migrate(ctx context.Context, definitions []migration) (err error) {
	if err := validateMigrationDefinitions(definitions); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), migrationRollbackTimeout)
		defer cancel()
		if rollbackErr := tx.Rollback(rollbackCtx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("rollback migrations: %w", rollbackErr))
		}
	}()

	if _, err = tx.Exec(ctx, fmt.Sprintf(`SET LOCAL lock_timeout = '%dms'`, migrationLockTimeout.Milliseconds())); err != nil {
		return fmt.Errorf("configure migration lock timeout: %w", err)
	}
	if _, err = tx.Exec(ctx, fmt.Sprintf(`SET LOCAL statement_timeout = '%dms'`, migrationStatementTimeout.Milliseconds())); err != nil {
		return fmt.Errorf("configure migration statement timeout: %w", err)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration advisory lock within %s: %w", migrationLockTimeout, err)
	}
	var versionTableExists bool
	if err = tx.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&versionTableExists); err != nil {
		return fmt.Errorf("detect schema_migrations: %w", err)
	}
	if !versionTableExists {
		if err := validatePreMigrationSchema(ctx, tx); err != nil {
			return err
		}
	}
	if !versionTableExists {
		if _, err = tx.Exec(ctx, `CREATE TABLE schema_migrations (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}
	} else {
		var checksumColumnExists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema=current_schema() AND table_name='schema_migrations' AND column_name='checksum'
		)`).Scan(&checksumColumnExists); err != nil {
			return fmt.Errorf("inspect schema_migrations checksum: %w", err)
		}
		// Upgrade the short-lived pre-checksum runner without invalidating databases
		// that already recorded v1 or v2. Missing checksums are trusted once, under
		// the advisory lock, then made mandatory for every subsequent startup.
		if !checksumColumnExists {
			if _, err = tx.Exec(ctx, `ALTER TABLE schema_migrations ADD COLUMN checksum TEXT`); err != nil {
				return fmt.Errorf("add schema_migrations checksum: %w", err)
			}
		}
	}

	applied, err := appliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrations(applied, definitions); err != nil {
		return err
	}
	for i, a := range applied {
		if a.checksum != nil {
			continue
		}
		checksum := migrationChecksum(definitions[i])
		if _, err = tx.Exec(ctx, `UPDATE schema_migrations SET checksum=$2 WHERE version=$1 AND checksum IS NULL`, a.version, checksum); err != nil {
			return fmt.Errorf("backfill checksum for migration %d (%s): %w", a.version, a.name, err)
		}
	}
	var checksumNullable bool
	if err = tx.QueryRow(ctx, `SELECT is_nullable='YES' FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='schema_migrations' AND column_name='checksum'`).Scan(&checksumNullable); err != nil {
		return fmt.Errorf("inspect schema_migrations checksum nullability: %w", err)
	}
	if checksumNullable {
		if _, err = tx.Exec(ctx, `ALTER TABLE schema_migrations ALTER COLUMN checksum SET NOT NULL`); err != nil {
			return fmt.Errorf("require schema_migrations checksum: %w", err)
		}
	}

	for _, m := range definitions[len(applied):] {
		if _, err = tx.Exec(ctx, m.sql); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`, m.version, m.name, migrationChecksum(m)); err != nil {
			return fmt.Errorf("record migration %d (%s): %w", m.version, m.name, err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func validateMigrationDefinitions(definitions []migration) error {
	for i, m := range definitions {
		expected := int64(i + 1)
		if m.version != expected {
			return fmt.Errorf("migration definitions must be contiguous: expected version %d, got %d", expected, m.version)
		}
		if m.name == "" || m.sql == "" {
			return fmt.Errorf("migration %d must have a name and SQL", m.version)
		}
	}
	return nil
}

func validateAppliedMigrations(applied []appliedMigration, definitions []migration) error {
	for i, a := range applied {
		if a.version < 1 || a.version > int64(len(definitions)) {
			return fmt.Errorf("database has unknown migration %d (%s)", a.version, a.name)
		}
		expectedVersion := int64(i + 1)
		if a.version != expectedVersion {
			return fmt.Errorf("database migration history has a gap: expected version %d, got %d", expectedVersion, a.version)
		}
		known := definitions[i]
		if a.name != known.name {
			return fmt.Errorf("migration %d name mismatch: database=%q binary=%q", a.version, a.name, known.name)
		}
		if a.checksum != nil {
			expectedChecksum := migrationChecksum(known)
			if *a.checksum != expectedChecksum {
				return fmt.Errorf("migration %d (%s) checksum mismatch: database=%q binary=%q", a.version, a.name, *a.checksum, expectedChecksum)
			}
		}
	}
	return nil
}

func migrationChecksum(m migration) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(m.sql)))
}

func validatePreMigrationSchema(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `
		WITH required(table_name, column_name, udt_name, nullable) AS (VALUES
			('users',  'id',            'uuid', 'NO'),
			('users',  'username',      'text', 'NO'),
			('users',  'password_hash', 'text', 'NO'),
			('agents', 'id',            'uuid', 'NO'),
			('agents', 'token_hash',    'text', 'NO'),
			('jobs',   'id',            'uuid', 'NO'),
			('jobs',   'agent_id',      'uuid', 'NO')
		)
		SELECT r.table_name, r.column_name, r.udt_name, r.nullable
		FROM required r
		WHERE EXISTS (
			SELECT 1 FROM information_schema.tables t
			WHERE t.table_schema=current_schema() AND t.table_name=r.table_name
		) AND NOT EXISTS (
			SELECT 1 FROM information_schema.columns c
			WHERE c.table_schema=current_schema() AND c.table_name=r.table_name
			  AND c.column_name=r.column_name AND c.udt_name=r.udt_name AND c.is_nullable=r.nullable
		)
		ORDER BY r.table_name, r.column_name`)
	if err != nil {
		return fmt.Errorf("validate pre-migration schema: %w", err)
	}
	defer rows.Close()

	var incompatible []string
	for rows.Next() {
		var table, column, typ, nullable string
		if err := rows.Scan(&table, &column, &typ, &nullable); err != nil {
			return fmt.Errorf("scan pre-migration schema validation: %w", err)
		}
		incompatible = append(incompatible, fmt.Sprintf("%s.%s %s nullable=%s", table, column, typ, nullable))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("validate pre-migration schema: %w", err)
	}
	if len(incompatible) > 0 {
		return fmt.Errorf("pre-migration schema is incompatible; missing or mismatched required columns: %v", incompatible)
	}
	return nil
}

func appliedMigrations(ctx context.Context, tx pgx.Tx) ([]appliedMigration, error) {
	rows, err := tx.Query(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	var applied []appliedMigration
	for rows.Next() {
		var a appliedMigration
		if err := rows.Scan(&a.version, &a.name, &a.checksum); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied = append(applied, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	return applied, nil
}
