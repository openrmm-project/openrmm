package store

import (
	"context"
	_ "embed"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed testdata/legacy_schema.sql
var legacySchemaSQL string

func TestMigrationFilesAreSeparatedFromSnapshot(t *testing.T) {
	if err := validateMigrationDefinitions(migrations); err != nil {
		t.Fatalf("validate migration definitions: %v", err)
	}
	if strings.Contains(migrationV1SQL, "totp_pending_secret") {
		t.Fatal("immutable v1 contains the v2 TOTP change")
	}
	if strings.TrimSpace(migrationV2SQL) != "ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_pending_secret TEXT" {
		t.Fatalf("unexpected immutable v2 SQL: %q", migrationV2SQL)
	}
	snapshot := canonicalSchemaSnapshot(t)
	if !strings.Contains(snapshot, "ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_pending_secret TEXT;") {
		t.Fatal("canonical snapshot does not include v2")
	}
	if snapshot == migrationV1SQL {
		t.Fatal("canonical snapshot must not double as immutable v1")
	}
}

func TestMigrationsPostgres(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}

	t.Run("empty and reapply", func(t *testing.T) {
		st := newMigrationTestStore(t, databaseURL)
		ctx := context.Background()
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate empty database: %v", err)
		}
		assertCurrentMigrationState(t, st)

		userID := uuid.NewString()
		if _, err := st.pool.Exec(ctx, `INSERT INTO users (id, username, password_hash) VALUES ($1, $2, 'hash')`, userID, "migration-sentinel"); err != nil {
			t.Fatalf("insert sentinel: %v", err)
		}
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("reapply migrations: %v", err)
		}
		var users int
		if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=$1`, userID).Scan(&users); err != nil {
			t.Fatalf("read sentinel: %v", err)
		}
		if users != 1 {
			t.Fatalf("sentinel rows = %d, want 1", users)
		}
		assertCurrentMigrationState(t, st)
	})

	t.Run("v1 already applied upgrades to v2", func(t *testing.T) {
		st := newMigrationTestStore(t, databaseURL)
		ctx := context.Background()
		if _, err := st.pool.Exec(ctx, migrationV1SQL); err != nil {
			t.Fatalf("install v1: %v", err)
		}
		if _, err := st.pool.Exec(ctx, `CREATE TABLE schema_migrations (
			version BIGINT PRIMARY KEY, name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
			t.Fatalf("create legacy migration table: %v", err)
		}
		if _, err := st.pool.Exec(ctx, `INSERT INTO schema_migrations(version, name) VALUES (1, 'baseline')`); err != nil {
			t.Fatalf("record legacy v1: %v", err)
		}
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("upgrade v1 to v2: %v", err)
		}
		assertCurrentMigrationState(t, st)
	})

	t.Run("adopt realistic legacy schema", func(t *testing.T) {
		st := newMigrationTestStore(t, databaseURL)
		ctx := context.Background()
		if _, err := st.pool.Exec(ctx, legacySchemaSQL); err != nil {
			t.Fatalf("install legacy fixture: %v", err)
		}
		agentID := uuid.NewString()
		jobID := uuid.NewString()
		if _, err := st.pool.Exec(ctx, `INSERT INTO agents (id, token_hash, rustdesk_id) VALUES ($1, 'hash', 'legacy-peer')`, agentID); err != nil {
			t.Fatalf("insert legacy agent: %v", err)
		}
		if _, err := st.pool.Exec(ctx, `INSERT INTO jobs (id, agent_id) VALUES ($1, $2)`, jobID, agentID); err != nil {
			t.Fatalf("insert legacy job: %v", err)
		}
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("adopt legacy fixture: %v", err)
		}
		assertCurrentMigrationState(t, st)
		var rustDeskID string
		if err := st.pool.QueryRow(ctx, `SELECT rustdesk_id FROM agents WHERE id=$1`, agentID).Scan(&rustDeskID); err != nil {
			t.Fatalf("read preserved legacy column: %v", err)
		}
		if rustDeskID != "legacy-peer" {
			t.Fatalf("rustdesk_id = %q, want legacy-peer", rustDeskID)
		}
		var jobs int
		if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE id=$1`, jobID).Scan(&jobs); err != nil || jobs != 1 {
			t.Fatalf("preserved jobs = %d, err=%v", jobs, err)
		}
	})

	t.Run("reject incompatible legacy schema", func(t *testing.T) {
		st := newMigrationTestStore(t, databaseURL)
		if _, err := st.pool.Exec(context.Background(), `CREATE TABLE users (id UUID PRIMARY KEY)`); err != nil {
			t.Fatalf("create partial users: %v", err)
		}
		err := st.Migrate(context.Background())
		assertMigrationError(t, err, "pre-migration schema is incompatible")
		assertRelationMissing(t, st, "schema_migrations")
	})

	t.Run("reject unknown version", func(t *testing.T) {
		st := newMigrationTestStore(t, databaseURL)
		installMigrationHistory(t, st, appliedMigration{version: 99, name: "future", checksum: stringPtr("future")})
		assertMigrationError(t, st.Migrate(context.Background()), "unknown migration 99")
	})

	t.Run("reject name mismatch", func(t *testing.T) {
		st := newMigrationTestStore(t, databaseURL)
		installMigrationHistory(t, st, appliedMigration{version: 1, name: "renamed", checksum: stringPtr(migrationChecksum(migrations[0]))})
		assertMigrationError(t, st.Migrate(context.Background()), "name mismatch")
	})

	t.Run("reject checksum mismatch", func(t *testing.T) {
		st := newMigrationTestStore(t, databaseURL)
		installMigrationHistory(t, st, appliedMigration{version: 1, name: "baseline", checksum: stringPtr("tampered")})
		assertMigrationError(t, st.Migrate(context.Background()), "checksum mismatch")
	})

	t.Run("reject migration gap", func(t *testing.T) {
		st := newMigrationTestStore(t, databaseURL)
		installMigrationHistory(t, st, appliedMigration{version: 2, name: "pending_totp_secret", checksum: stringPtr(migrationChecksum(migrations[1]))})
		assertMigrationError(t, st.Migrate(context.Background()), "history has a gap")
	})

	t.Run("rollback failed migration", func(t *testing.T) {
		st := newMigrationTestStore(t, databaseURL)
		definitions := []migration{{
			version: 1,
			name:    "broken",
			sql:     `CREATE TABLE rollback_sentinel (id INTEGER); SELECT * FROM migration_table_that_does_not_exist`,
		}}
		assertMigrationError(t, st.migrate(context.Background(), definitions), "apply migration 1 (broken)")
		assertRelationMissing(t, st, "rollback_sentinel")
		assertRelationMissing(t, st, "schema_migrations")
	})

	t.Run("advisory lock serializes concurrent runner", func(t *testing.T) {
		st := newMigrationTestStore(t, databaseURL)
		ctx := context.Background()
		lockTx, err := st.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin lock holder: %v", err)
		}
		if _, err := lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
			_ = lockTx.Rollback(ctx)
			t.Fatalf("hold advisory lock: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			done <- st.migrate(context.Background(), []migration{{version: 1, name: "concurrent", sql: `CREATE TABLE concurrent_sentinel (id INTEGER)`}})
		}()
		select {
		case err := <-done:
			_ = lockTx.Rollback(ctx)
			t.Fatalf("migration returned while advisory lock was held: %v", err)
		case <-time.After(150 * time.Millisecond):
		}
		if err := lockTx.Rollback(ctx); err != nil {
			t.Fatalf("release advisory lock: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("migration after advisory unlock: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("migration did not continue after advisory unlock")
		}
	})

	t.Run("canonical snapshot matches migrations", func(t *testing.T) {
		migrated := newMigrationTestStore(t, databaseURL)
		snapshot := newMigrationTestStore(t, databaseURL)
		ctx := context.Background()
		if err := migrated.Migrate(ctx); err != nil {
			t.Fatalf("apply migrations: %v", err)
		}
		if _, err := snapshot.pool.Exec(ctx, canonicalSchemaSnapshot(t)); err != nil {
			t.Fatalf("apply canonical snapshot: %v", err)
		}
		got := schemaObjectSignature(t, migrated)
		want := schemaObjectSignature(t, snapshot)
		if !slices.Equal(got, want) {
			t.Fatalf("canonical snapshot differs from migrations\nmigrations:\n%s\nsnapshot:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})
}

func newMigrationTestStore(t *testing.T, databaseURL string) *Store {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Fatalf("ping postgres: %v", err)
	}
	schemaName := "migration_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatalf("create test schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatalf("parse postgres URL: %v", err)
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatalf("open test schema: %v", err)
	}
	st := &Store{pool: pool}
	t.Cleanup(func() {
		st.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
	})
	return st
}

func canonicalSchemaSnapshot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migrations test")
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "db", "schema.sql"))
	if err != nil {
		t.Fatalf("read db/schema.sql: %v", err)
	}
	return string(b)
}

func installMigrationHistory(t *testing.T, st *Store, rows ...appliedMigration) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.pool.Exec(ctx, `CREATE TABLE schema_migrations (
		version BIGINT PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, row := range rows {
		if row.checksum == nil {
			t.Fatal("test migration history requires a checksum")
		}
		if _, err := st.pool.Exec(ctx, `INSERT INTO schema_migrations(version, name, checksum) VALUES($1,$2,$3)`, row.version, row.name, *row.checksum); err != nil {
			t.Fatalf("insert schema migration %d: %v", row.version, err)
		}
	}
}

func assertCurrentMigrationState(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	rows, err := st.pool.Query(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("read schema migrations: %v", err)
	}
	defer rows.Close()
	var count int
	for rows.Next() {
		if count >= len(migrations) {
			t.Fatal("database has more migrations than binary")
		}
		var version int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			t.Fatalf("scan schema migration: %v", err)
		}
		want := migrations[count]
		if version != want.version || name != want.name || checksum != migrationChecksum(want) {
			t.Fatalf("migration row = (%d,%q,%q), want (%d,%q,%q)", version, name, checksum, want.version, want.name, migrationChecksum(want))
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read schema migrations: %v", err)
	}
	if count != len(migrations) {
		t.Fatalf("schema migration count = %d, want %d", count, len(migrations))
	}
	assertColumnExists(t, st, "jobs", "task_id")
	assertColumnExists(t, st, "users", "totp_pending_secret")
	var taskIndex *string
	if err := st.pool.QueryRow(ctx, `SELECT to_regclass('idx_jobs_task')::text`).Scan(&taskIndex); err != nil {
		t.Fatalf("check idx_jobs_task: %v", err)
	}
	if taskIndex == nil {
		t.Fatal("idx_jobs_task was not created")
	}
}

func assertColumnExists(t *testing.T, st *Store, table, column string) {
	t.Helper()
	var exists bool
	if err := st.pool.QueryRow(context.Background(), `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name=$1 AND column_name=$2
	)`, table, column).Scan(&exists); err != nil {
		t.Fatalf("check %s.%s: %v", table, column, err)
	}
	if !exists {
		t.Fatalf("%s.%s was not created", table, column)
	}
}

func assertMigrationError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("migration succeeded, want error containing %q", contains)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("migration error = %q, want substring %q", err, contains)
	}
}

func assertRelationMissing(t *testing.T, st *Store, name string) {
	t.Helper()
	var relation *string
	if err := st.pool.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, name).Scan(&relation); err != nil {
		t.Fatalf("check relation %s: %v", name, err)
	}
	if relation != nil {
		t.Fatalf("relation %s exists after rollback", name)
	}
}

func schemaObjectSignature(t *testing.T, st *Store) []string {
	t.Helper()
	rows, err := st.pool.Query(context.Background(), `
		SELECT signature FROM (
			SELECT format('column|%s|%s|%s|%s|%s|%s', table_name, ordinal_position,
				column_name, udt_name, is_nullable, COALESCE(column_default, '')) AS signature
			FROM information_schema.columns
			WHERE table_schema=current_schema() AND table_name <> 'schema_migrations'
			UNION ALL
			SELECT format('constraint|%s|%s|%s', t.relname, c.conname,
				replace(pg_get_constraintdef(c.oid), current_schema() || '.', ''))
			FROM pg_constraint c
			JOIN pg_class t ON t.oid=c.conrelid
			JOIN pg_namespace n ON n.oid=t.relnamespace
			WHERE n.nspname=current_schema() AND t.relname <> 'schema_migrations'
			UNION ALL
			SELECT format('index|%s|%s|%s', tablename, indexname,
				replace(indexdef, current_schema() || '.', ''))
			FROM pg_indexes
			WHERE schemaname=current_schema() AND tablename <> 'schema_migrations'
		) objects
		ORDER BY signature`)
	if err != nil {
		t.Fatalf("read schema objects: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var signature string
		if err := rows.Scan(&signature); err != nil {
			t.Fatalf("scan schema object: %v", err)
		}
		out = append(out, signature)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read schema objects: %v", err)
	}
	return out
}

func stringPtr(v string) *string { return &v }
