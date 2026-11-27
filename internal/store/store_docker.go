package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/p1ngu/rmm/internal/protocol"
)

type DockerInventory struct {
	AgentID       string                     `json:"agent_id"`
	Hostname      string                     `json:"hostname"`
	Description   string                     `json:"description"`
	AgentStatus   string                     `json:"agent_status"`
	Status        string                     `json:"status"`
	ErrorCode     string                     `json:"error_code,omitempty"`
	LastAttemptAt *time.Time                 `json:"last_attempt_at,omitempty"`
	LastSuccessAt *time.Time                 `json:"last_success_at,omitempty"`
	Stale         bool                       `json:"stale"`
	Containers    []protocol.DockerContainer `json:"containers,omitempty"`
}

func (d *DockerInventory) freshness(now time.Time) {
	d.Stale = d.Status != protocol.DockerAvailable || d.AgentStatus != "online" || d.LastSuccessAt == nil || now.Sub(*d.LastSuccessAt) > protocol.DockerFreshFor
}

// A failed observation updates attempt/status only. A successful empty snapshot
// is different: it authoritatively removes the previously observed containers.
func (s *Store) RecordDockerSnapshot(ctx context.Context, id string, snapshot protocol.DockerSnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if snapshot.Containers == nil {
		snapshot.Containers = []protocol.DockerContainer{}
	}
	data, err := json.Marshal(snapshot.Containers)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO docker_inventory(agent_id,status,error_code,containers,last_attempt_at,last_success_at)
 SELECT id,$2,$3,CASE WHEN $2='available' THEN $4::jsonb ELSE '[]'::jsonb END,now(),CASE WHEN $2='available' THEN now() END
 FROM agents WHERE id=$1 AND revoked_at IS NULL
 ON CONFLICT(agent_id) DO UPDATE SET status=EXCLUDED.status,error_code=EXCLUDED.error_code,last_attempt_at=now(),
 containers=CASE WHEN EXCLUDED.status='available' THEN EXCLUDED.containers ELSE docker_inventory.containers END,
 last_success_at=CASE WHEN EXCLUDED.status='available' THEN now() ELSE docker_inventory.last_success_at END`, id, snapshot.Status, snapshot.ErrorCode, data)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

const dockerHostColumns = `a.id,a.hostname,a.description,a.status,COALESCE(d.status,'unknown'),COALESCE(d.error_code,''),d.last_attempt_at,d.last_success_at`

func (s *Store) DockerInventory(ctx context.Context, id string) (*DockerInventory, error) {
	var d DockerInventory
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT `+dockerHostColumns+`,COALESCE(d.containers,'[]'::jsonb) FROM agents a LEFT JOIN docker_inventory d ON d.agent_id=a.id WHERE a.id=$1 AND a.revoked_at IS NULL`, id).Scan(&d.AgentID, &d.Hostname, &d.Description, &d.AgentStatus, &d.Status, &d.ErrorCode, &d.LastAttemptAt, &d.LastSuccessAt, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &d.Containers); err != nil {
		return nil, err
	}
	d.freshness(time.Now())
	return &d, nil
}

type DockerFilter struct {
	Query, State, Health, AgentID string
	Limit, Offset                 int
}
type DockerRow struct {
	protocol.DockerContainer
	AgentID          string     `json:"agent_id"`
	Hostname         string     `json:"hostname"`
	Description      string     `json:"description"`
	AgentStatus      string     `json:"agent_status"`
	CollectionStatus string     `json:"collection_status"`
	LastSuccessAt    *time.Time `json:"last_success_at,omitempty"`
	Stale            bool       `json:"stale"`
}
type DockerFleet struct {
	Hosts      []DockerInventory `json:"hosts"`
	Containers []DockerRow       `json:"containers"`
	Total      int64             `json:"total"`
	Limit      int               `json:"limit"`
	Offset     int               `json:"offset"`
}

func (s *Store) DockerFleet(ctx context.Context, scope ReportFilter, filter DockerFilter) (*DockerFleet, error) {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 200 || filter.Offset < 0 {
		return nil, fmt.Errorf("invalid Docker pagination")
	}
	tx, err := s.connections.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	where, args := scope.where()
	if where == "" {
		where = " WHERE a.revoked_at IS NULL"
	} else {
		where += " AND a.revoked_at IS NULL"
	}
	if filter.AgentID != "" {
		args = append(args, filter.AgentID)
		where += fmt.Sprintf(" AND a.id=$%d", len(args))
	}
	from := ` FROM agents a LEFT JOIN docker_inventory d ON d.agent_id=a.id` + reportJoins
	result := &DockerFleet{Hosts: []DockerInventory{}, Containers: []DockerRow{}, Limit: filter.Limit, Offset: filter.Offset}
	rows, err := tx.Query(ctx, `SELECT `+dockerHostColumns+from+where+` ORDER BY lower(a.hostname),a.id`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d DockerInventory
		if err = rows.Scan(&d.AgentID, &d.Hostname, &d.Description, &d.AgentStatus, &d.Status, &d.ErrorCode, &d.LastAttemptAt, &d.LastSuccessAt); err != nil {
			rows.Close()
			return nil, err
		}
		d.freshness(time.Now())
		result.Hosts = append(result.Hosts, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	from += ` CROSS JOIN LATERAL jsonb_array_elements(COALESCE(d.containers,'[]'::jsonb)) c`
	if filter.Query != "" {
		args = append(args, "%"+strings.ToLower(filter.Query)+"%")
		where += fmt.Sprintf(` AND lower(concat_ws(' ',c->>'name',c->>'image',a.hostname,a.description)) LIKE $%d`, len(args))
	}
	for _, f := range []struct{ field, value string }{{"state", filter.State}, {"health", filter.Health}} {
		if f.value != "" {
			args = append(args, f.value)
			where += fmt.Sprintf(" AND c->>'%s'=$%d", f.field, len(args))
		}
	}
	if err = tx.QueryRow(ctx, `SELECT COUNT(*)`+from+where, args...).Scan(&result.Total); err != nil {
		return nil, err
	}
	args = append(args, filter.Limit, filter.Offset)
	rows, err = tx.Query(ctx, `SELECT c,a.id,a.hostname,a.description,a.status,COALESCE(d.status,'unknown'),d.last_success_at`+from+where+fmt.Sprintf(` ORDER BY lower(a.hostname),c->>'name',c->>'id' LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r DockerRow
		var data []byte
		if err = rows.Scan(&data, &r.AgentID, &r.Hostname, &r.Description, &r.AgentStatus, &r.CollectionStatus, &r.LastSuccessAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &r.DockerContainer); err != nil {
			return nil, err
		}
		r.Stale = r.CollectionStatus != protocol.DockerAvailable || r.AgentStatus != "online" || r.LastSuccessAt == nil || time.Since(*r.LastSuccessAt) > protocol.DockerFreshFor
		result.Containers = append(result.Containers, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return result, tx.Commit(ctx)
}
