package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestInventoryJSONBNULIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := newIsolatedStore(t, "store_inventory", url)
	a, err := st.CreateAgent(ctx, "windows-11", "windows", "amd64", "0.9.2", "inventory-test-token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"hostname":"windows-11","software":[{"name":"App\u0000"}]}`)
	// Reproduce the original silent insert failure with PostgreSQL itself, then
	// prove the production store path accepts the exact same agent snapshot.
	_, err = st.pool.Exec(ctx, `INSERT INTO inventory (agent_id,data) VALUES ($1,$2)`, a.ID, raw)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "22P05" {
		t.Fatalf("expected PostgreSQL's unsupported Unicode escape error, got %v", err)
	}
	if err := st.UpsertInventory(ctx, a.ID, raw); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := st.pool.QueryRow(ctx, `SELECT data->'software'->0->>'name' FROM inventory WHERE agent_id=$1`, a.ID).Scan(&name); err != nil || name != "App" {
		t.Fatalf("sanitized software name = %q, error %v", name, err)
	}
}
