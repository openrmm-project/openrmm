package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/scriptvars"
	"github.com/p1ngu/rmm/internal/secretcrypto"
)

// ScriptVariable is a name a library script may read, plus the values stored
// for it. Secret values are never filled in.
type ScriptVariable struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Secret      bool                  `json:"secret"`
	Values      []ScriptVariableValue `json:"values"`
}

// ScriptVariableValue is one scope's value. Value is empty when the variable
// is a secret; Set is true when a value is stored.
type ScriptVariableValue struct {
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id,omitempty"`
	Value     string `json:"value,omitempty"`
	Set       bool   `json:"set"`
}

const scriptVariableRankSQL = `
	CASE scope_type
		WHEN 'agent' THEN 5
		WHEN 'group' THEN 4
		WHEN 'site' THEN 3
		WHEN 'client' THEN 2
		WHEN 'global' THEN 1
		ELSE 0
	END`

// ListScriptVariables returns every variable. Secret plaintext is not included.
func (s *Store) ListScriptVariables(ctx context.Context) ([]ScriptVariable, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.name, d.description, d.secret,
		       COALESCE(v.scope_type, ''), COALESCE(v.scope_id, ''),
		       CASE WHEN d.secret OR v.value IS NULL THEN '' ELSE v.value END,
		       v.value IS NOT NULL
		  FROM script_variables d
		  LEFT JOIN script_variable_values v ON v.variable_id = d.id
		 ORDER BY d.name, `+scriptVariableRankSQL+` DESC, v.scope_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*ScriptVariable{}
	var order []string
	for rows.Next() {
		var id, name, description, scopeType, scopeID, value string
		var secret, set bool
		if err := rows.Scan(&id, &name, &description, &secret, &scopeType, &scopeID, &value, &set); err != nil {
			return nil, err
		}
		item, ok := byID[id]
		if !ok {
			item = &ScriptVariable{ID: id, Name: name, Description: description, Secret: secret, Values: []ScriptVariableValue{}}
			byID[id] = item
			order = append(order, id)
		}
		if set {
			item.Values = append(item.Values, ScriptVariableValue{ScopeType: scopeType, ScopeID: scopeID, Value: value, Set: true})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ScriptVariable, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// CreateScriptVariable inserts a definition. It does not take a value: the
// value is a separate write, so this statement has nothing to leak into the audit.
func (s *Store) CreateScriptVariable(ctx context.Context, name, description string, secret bool) (*ScriptVariable, error) {
	if err := scriptvars.ValidUserName(name); err != nil {
		return nil, err
	}
	id := newID()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO script_variables (id, name, description, secret) VALUES ($1,$2,$3,$4)`,
		id, name, description, secret)
	if err != nil {
		return nil, err
	}
	return &ScriptVariable{ID: id, Name: name, Description: description, Secret: secret, Values: []ScriptVariableValue{}}, nil
}

// UpdateScriptVariable changes the description and the secret flag. Turning
// the flag on encrypts existing values atomically. A secret cannot be downgraded.
func (s *Store) UpdateScriptVariable(ctx context.Context, cfg *config.Config, id, description string, secret bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var current bool
	if err := tx.QueryRow(ctx, `SELECT secret FROM script_variables WHERE id=$1 FOR UPDATE`, id).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if current && !secret {
		return fmt.Errorf("a secret variable cannot be made public; create a new variable")
	}
	if current != secret {
		rows, err := tx.Query(ctx, `SELECT scope_type, scope_id, value FROM script_variable_values WHERE variable_id=$1`, id)
		if err != nil {
			return err
		}
		type cell struct{ scopeType, scopeID, value string }
		var cells []cell
		for rows.Next() {
			var c cell
			if err := rows.Scan(&c.scopeType, &c.scopeID, &c.value); err != nil {
				rows.Close()
				return err
			}
			cells = append(cells, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range cells {
			if err := scriptvars.ValidValue(true, c.value); err != nil {
				return fmt.Errorf("cannot mark this variable secret: %w", err)
			}
			next, err := secretcrypto.Encrypt(cfg, c.value)
			if err != nil {
				return err
			}

			if _, err := tx.Exec(ctx, `UPDATE script_variable_values SET value=$4 WHERE variable_id=$1 AND scope_type=$2 AND scope_id=$3`,
				id, c.scopeType, c.scopeID, next); err != nil {
				return err
			}
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE script_variables SET description=$2, secret=$3, updated_at=now() WHERE id=$1`, id, description, secret)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// DeleteScriptVariable removes a variable and, by cascade, its values.
func (s *Store) DeleteScriptVariable(ctx context.Context, id string) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `DELETE FROM script_variables WHERE id=$1 RETURNING name`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// SetScriptVariableValue stores one scope's value. Secret variables are
// encrypted here; the plaintext is not retained.
func (s *Store) SetScriptVariableValue(ctx context.Context, cfg *config.Config, id, scopeType, scopeID, value string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var secret bool
	err = tx.QueryRow(ctx, `SELECT secret FROM script_variables WHERE id=$1 FOR UPDATE`, id).Scan(&secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := scriptvars.ValidValue(secret, value); err != nil {
		return err
	}
	stored := value
	if secret {
		enc, err := secretcrypto.Encrypt(cfg, value)
		if err != nil {
			return err
		}
		stored = enc
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO script_variable_values (variable_id, scope_type, scope_id, value)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (variable_id, scope_type, scope_id) DO UPDATE SET value=EXCLUDED.value`,
		id, scopeType, scopeID, stored)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE script_variables SET updated_at=now() WHERE id=$1`, id)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ClearScriptVariableValue removes one scope's value.
func (s *Store) ClearScriptVariableValue(ctx context.Context, id, scopeType, scopeID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM script_variable_values WHERE variable_id=$1 AND scope_type=$2 AND scope_id=$3`, id, scopeType, scopeID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UnknownScriptVariables returns names that are not defined.
func (s *Store) UnknownScriptVariables(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT name FROM script_variables WHERE name = ANY($1)`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	known := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		known[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var missing []string
	for _, name := range names {
		if _, ok := known[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// ScriptVariableName returns the name of one variable, for audit lines that
// must not include a value.
func (s *Store) ScriptVariableName(ctx context.Context, id string) (string, bool, error) {
	var name string
	var secret bool
	err := s.pool.QueryRow(ctx, `SELECT name, secret FROM script_variables WHERE id=$1`, id).Scan(&name, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrNotFound
	}
	return name, secret, err
}

// ResolveScriptVariables resolves names for a device and returns the
// environment to send, including built-ins, plus the secret plaintexts that
// must be redacted from output and must not be stored on the job.
func (s *Store) ResolveScriptVariables(ctx context.Context, cfg *config.Config, agentID string, names []string) (map[string]string, []string, error) {
	var hostname, siteID, groupID, clientID, siteName, clientName string
	err := s.pool.QueryRow(ctx, `
		SELECT a.hostname,
		       COALESCE(a.site_id::text, ''),
		       COALESCE(a.group_id::text, ''),
		       COALESCE(si.client_id::text, ''),
		       COALESCE(si.name, ''),
		       COALESCE(c.name, '')
		  FROM agents a
		  LEFT JOIN sites si ON si.id = a.site_id
		  LEFT JOIN clients c ON c.id = si.client_id
		 WHERE a.id=$1`, agentID).Scan(&hostname, &siteID, &groupID, &clientID, &siteName, &clientName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	env := scriptvars.Builtins(scriptvars.Device{
		AgentID: agentID, Hostname: hostname,
		ClientID: clientID, ClientName: clientName,
		SiteID: siteID, SiteName: siteName, GroupID: groupID,
	})
	if len(names) == 0 {
		return env, nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT d.name, d.secret, v.scope_type, v.scope_id, v.value
		  FROM script_variables d
		  JOIN script_variable_values v ON v.variable_id = d.id
		 WHERE d.name = ANY($1) AND (
		(v.scope_type='global' AND v.scope_id='') OR
		(v.scope_type='agent' AND v.scope_id=$2) OR
		(v.scope_type='group' AND v.scope_id=$3 AND $3<>'') OR
		(v.scope_type='site' AND v.scope_id=$4 AND $4<>'') OR
		(v.scope_type='client' AND v.scope_id=$5 AND $5<>''))`, names, agentID, groupID, siteID, clientID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	type candidate struct {
		secret bool
		scope  string
		id     string
		value  string
		rank   int
	}
	best := map[string]candidate{}
	defined := map[string]bool{}
	for rows.Next() {
		var name, scope, id, value string
		var secret bool
		if err := rows.Scan(&name, &secret, &scope, &id, &value); err != nil {
			return nil, nil, err
		}
		defined[name] = true
		rank := scopeRank(scope)
		if !scopeMatches(scope, id, agentID, groupID, siteID, clientID) {
			continue
		}
		if prev, ok := best[name]; ok && prev.rank >= rank {
			continue
		}
		best[name] = candidate{secret: secret, scope: scope, id: id, value: value, rank: rank}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	// A name with no value row is still defined. Distinguish unknown from unset.
	defRows, err := s.pool.Query(ctx, `SELECT name FROM script_variables WHERE name = ANY($1)`, names)
	if err != nil {
		return nil, nil, err
	}
	defer defRows.Close()
	for defRows.Next() {
		var name string
		if err := defRows.Scan(&name); err != nil {
			return nil, nil, err
		}
		defined[name] = true
	}
	if err := defRows.Err(); err != nil {
		return nil, nil, err
	}
	var secrets []string
	for _, name := range names {
		if !defined[name] {
			return nil, nil, fmt.Errorf("unknown script variable %s", name)
		}
		got, ok := best[name]
		if !ok {
			return nil, nil, fmt.Errorf("script variable %s has no value for this device", name)
		}
		plain := got.value
		if got.secret {
			if !strings.HasPrefix(got.value, "enc:v1:") {
				return nil, nil, fmt.Errorf("script variable %s is not encrypted", name)
			}
			decoded, err := secretcrypto.Decrypt(cfg, got.value)
			if err != nil {
				return nil, nil, fmt.Errorf("script variable %s cannot be decrypted", name)
			}
			plain = decoded
			secrets = append(secrets, plain)
		}
		env[name] = plain
	}
	return env, secrets, nil
}

func scopeRank(scope string) int {
	switch scope {
	case models.ScopeAgent:
		return 5
	case models.ScopeGroup:
		return 4
	case models.ScopeSite:
		return 3
	case models.ScopeClient:
		return 2
	case "global":
		return 1
	default:
		return 0
	}
}

func scopeMatches(scope, id, agentID, groupID, siteID, clientID string) bool {
	switch scope {
	case "global":
		return id == ""
	case models.ScopeAgent:
		return id == agentID
	case models.ScopeGroup:
		return groupID != "" && id == groupID
	case models.ScopeSite:
		return siteID != "" && id == siteID
	case models.ScopeClient:
		return clientID != "" && id == clientID
	default:
		return false
	}
}

// SaveJobRedactions stores the ciphertext of the secrets sent with a job.
func (s *Store) SaveJobRedactions(ctx context.Context, cfg *config.Config, jobID string, secrets []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var previous string
	if err := tx.QueryRow(ctx, `SELECT secret_redactions FROM jobs WHERE id=$1 FOR UPDATE`, jobID).Scan(&previous); err != nil {
		return err
	}
	old, err := scriptvars.Open(cfg, previous)
	if err != nil {
		return fmt.Errorf("cannot read job redactions")
	}
	seen := map[string]bool{}
	var all []string
	for _, value := range append(old, secrets...) {
		if !seen[value] {
			all = append(all, value)
			seen[value] = true
		}
	}
	blob, err := scriptvars.Seal(cfg, all)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET secret_redactions=$2 WHERE id=$1`, jobID, blob); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// JobRedactions returns that ciphertext. It is not part of the job JSON.
func (s *Store) JobRedactions(ctx context.Context, jobID string) (string, error) {
	var blob string
	err := s.pool.QueryRow(ctx, `SELECT secret_redactions FROM jobs WHERE id=$1`, jobID).Scan(&blob)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return blob, err
}

func jobVariablesJSON(names []string) []byte {
	if names == nil {
		names = []string{}
	}
	raw, _ := json.Marshal(names)
	return raw
}
