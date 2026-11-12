package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
	// ProposedBy is set when AI Chat or MCP proposed the definition ("ai:…",
	// "mcp:…"). Until ApprovedBy is set too the variable is Pending: no value
	// can be set, no script can bind it and no run resolves it.
	ProposedBy string `json:"proposed_by"`
	ApprovedBy string `json:"approved_by"`
	Pending    bool   `json:"pending"`
}

// ErrScriptVariablePending refuses using a proposal a person has not approved.
var ErrScriptVariablePending = fmt.Errorf("%w: the variable is a proposal awaiting review; approve it first", ErrConflict)

// pendingScriptVariableSQL matches a proposal not yet approved.
const pendingScriptVariableSQL = "(d.proposed_by <> '' AND d.approved_by = '')"

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
		SELECT d.id, d.name, d.description, d.secret, d.proposed_by, d.approved_by,
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
		var id, name, description, proposedBy, approvedBy, scopeType, scopeID, value string
		var secret, set bool
		if err := rows.Scan(&id, &name, &description, &secret, &proposedBy, &approvedBy, &scopeType, &scopeID, &value, &set); err != nil {
			return nil, err
		}
		item, ok := byID[id]
		if !ok {
			item = &ScriptVariable{ID: id, Name: name, Description: description, Secret: secret, Values: []ScriptVariableValue{},
				ProposedBy: proposedBy, ApprovedBy: approvedBy, Pending: proposedBy != "" && approvedBy == ""}
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

// ScriptVariableModelDefinition is metadata only; values and scope bindings are
// never selected by the model-facing query.
type ScriptVariableModelDefinition struct {
	Name                 string `json:"name"`
	Description          string `json:"description"`
	Secret               bool   `json:"secret"`
	Pending              bool   `json:"awaiting_review"`
	DescriptionTruncated bool   `json:"description_truncated,omitempty"`
}

// These are model transport budgets, not limits on stored variable fields.
const (
	ScriptVariableModelLimit            = 200
	ScriptVariableModelNameBytes        = 4096
	ScriptVariableModelDescriptionRunes = 1024
)

// ScriptVariableDefinitionsForModel searches in SQL and selects bounded metadata
// only. Oversized identifiers are omitted, never silently renamed. matched
// includes them so the caller can explain omissions and suggest the console.
func (s *Store) ScriptVariableDefinitionsForModel(ctx context.Context, search string) ([]ScriptVariableModelDefinition, int, error) {
	needle := strings.ToLower(strings.TrimSpace(search))
	const matches = `($1='' OR strpos(lower(name || ' ' || description),$1)>0)`
	var matched int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM script_variables WHERE `+matches, needle).Scan(&matched); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT name, left(description,$3), secret,
  (proposed_by<>'' AND approved_by=''), length(description)>$3
  FROM script_variables WHERE `+matches+` AND octet_length(name)<=$2
  ORDER BY name LIMIT $4`, needle, ScriptVariableModelNameBytes, ScriptVariableModelDescriptionRunes, ScriptVariableModelLimit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ScriptVariableModelDefinition{}
	for rows.Next() {
		var d ScriptVariableModelDefinition
		if err := rows.Scan(&d.Name, &d.Description, &d.Secret, &d.Pending, &d.DescriptionTruncated); err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, matched, rows.Err()
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
// ProposeScriptVariable inserts a definition proposed by AI Chat or MCP. It is
// pending until ApproveScriptVariable: it takes no value and binds to nothing.
func (s *Store) ProposeScriptVariable(ctx context.Context, name, description string, secret bool, proposedBy string) (*ScriptVariable, error) {
	if strings.TrimSpace(proposedBy) == "" {
		return nil, fmt.Errorf("a proposal needs its author")
	}
	if err := scriptvars.ValidUserName(name); err != nil {
		return nil, err
	}
	id := newID()
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO script_variables (id, name, description, secret, proposed_by) VALUES ($1,$2,$3,$4,$5)`,
		id, name, description, secret, proposedBy); err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: variable %s already exists", ErrConflict, name)
		}
		return nil, err
	}
	return &ScriptVariable{ID: id, Name: name, Description: description, Secret: secret, Values: []ScriptVariableValue{}, ProposedBy: proposedBy, Pending: true}, nil
}

// ApproveScriptVariable accepts a pending proposal. Approving twice, or a
// definition a person created, is a conflict: there is nothing to accept.
func (s *Store) ApproveScriptVariable(ctx context.Context, id, approvedBy string) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `
		UPDATE script_variables d SET approved_by=$2, approved_at=now(), updated_at=now()
		 WHERE d.id=$1 AND `+pendingScriptVariableSQL+`
		 RETURNING d.name`, id, approvedBy).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM script_variables WHERE id=$1)`, id).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("%w: this variable is not a proposal awaiting review", ErrConflict)
	}
	return name, err
}

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
	var secret, pending bool
	err = tx.QueryRow(ctx, `SELECT d.secret, `+pendingScriptVariableSQL+` FROM script_variables d WHERE d.id=$1 FOR UPDATE`, id).Scan(&secret, &pending)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if pending {
		return ErrScriptVariablePending
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

// UnknownScriptVariables returns names that are not defined, or are AI/MCP
// proposals nobody approved yet.
func (s *Store) UnknownScriptVariables(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	// A proposal awaiting review is not a variable a script may bind yet.
	rows, err := s.pool.Query(ctx, `SELECT d.name FROM script_variables d WHERE d.name = ANY($1) AND NOT `+pendingScriptVariableSQL, names)
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
		 WHERE d.name = ANY($1) AND NOT `+pendingScriptVariableSQL+` AND (
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
	if err := scriptvars.ValidEnvironmentSize(env); err != nil {
		return nil, nil, err
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

// ScriptVariableScopeRevision must be read BEFORE resolving values. A move
// between this read and resolution is rejected at the final send, including ABA.
func (s *Store) ScriptVariableScopeRevision(ctx context.Context, agentID string) (time.Time, error) {
	var revision time.Time
	err := s.pool.QueryRow(ctx, `SELECT monitoring_scope_since FROM agents WHERE id=$1`, agentID).Scan(&revision)
	return revision, err
}

func lockScriptVariableScope(ctx context.Context, tx pgx.Tx, agentID string, revision time.Time) error {
	var current time.Time
	var status string
	if err := tx.QueryRow(ctx, `SELECT monitoring_scope_since,status FROM agents WHERE id=$1 FOR SHARE`, agentID).Scan(&current, &status); err != nil {
		return err
	}
	if revision.IsZero() || !current.Equal(revision) || status == models.AgentRevoked {
		return fmt.Errorf("device scope changed before script variable dispatch")
	}
	return nil
}

// WithScriptVariablesDispatch serializes the authorization point with device
// moves/deletion. send is only a nonblocking hub enqueue, never a DB operation.
func (s *Store) WithScriptVariablesDispatch(ctx context.Context, agentID string, revision time.Time, send func() bool) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err := lockScriptVariableScope(ctx, tx, agentID, revision); err != nil {
		return false, err
	}
	return send(), nil
}
