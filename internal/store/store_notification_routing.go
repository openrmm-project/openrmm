package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/p1ngu/rmm/internal/models"
)

var ErrNotificationRecipients = errors.New("some recipients are unavailable for this scope")

func routingTarget(ctx context.Context, q dbQuerier, scopeType, scopeID string, lock bool) (clientID, siteID, name string, err error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	switch scopeType {
	case models.ScopeClient:
		clientID = scopeID
		err = q.QueryRow(ctx, `SELECT name FROM clients WHERE id=$1`+suffix, scopeID).Scan(&name)
	case models.ScopeSite:
		siteID = scopeID
		err = q.QueryRow(ctx, `SELECT client_id,name FROM sites WHERE id=$1`+suffix, scopeID).Scan(&clientID, &name)
	default:
		err = fmt.Errorf("invalid notification routing scope")
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// One statement resolves Site before Client and reads its complete recipient set
// from the same MVCC snapshot. Returning inherit retains version tombstones.
func effectiveNotificationRoute(ctx context.Context, q dbQuerier, clientID, siteID string) (models.NotificationRouteSnapshot, string, error) {
	route := models.NotificationRouteSnapshot{Mode: "automatic"}
	var name string
	err := q.QueryRow(ctx, `SELECT 'selected',CASE WHEN nr.site_id IS NOT NULL THEN 'site' ELSE 'client' END,
 COALESCE(nr.site_id,nr.client_id),nr.version,COALESCE(si.name,cl.name,''),
 ARRAY(SELECT user_id::text FROM notification_route_users WHERE route_id=nr.id ORDER BY user_id)
 FROM notification_routes nr LEFT JOIN sites si ON si.id=nr.site_id LEFT JOIN clients cl ON cl.id=nr.client_id
 WHERE nr.mode='selected' AND (nr.site_id=$2 OR nr.client_id=$1)
 ORDER BY CASE WHEN nr.site_id IS NOT NULL THEN 0 ELSE 1 END LIMIT 1`, notificationUUID(clientID), notificationUUID(siteID)).
		Scan(&route.Mode, &route.ScopeType, &route.ScopeID, &route.Version, &name, &route.UserIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.NotificationRouteSnapshot{Mode: "automatic"}, "", nil
	}
	return route, name, err
}

// This scope intersection also works for empty sites/departments. A narrower
// recipient is eligible as a candidate, but delivery still checks each incident.
const routeRecipientIntersection = `(NOT EXISTS(SELECT 1 FROM user_scopes us WHERE us.user_id=u.id)
 OR EXISTS(SELECT 1 FROM user_scopes us WHERE us.user_id=u.id AND (
 (us.scope_type='client' AND us.scope_id=$1)
 OR (us.scope_type='site' AND EXISTS(SELECT 1 FROM sites si WHERE si.id=us.scope_id AND si.client_id=$1 AND ($2::uuid IS NULL OR si.id=$2)))
 OR (us.scope_type='group' AND EXISTS(SELECT 1 FROM device_groups g JOIN sites si ON si.id=g.site_id WHERE g.id=us.scope_id AND si.client_id=$1 AND ($2::uuid IS NULL OR si.id=$2)))
 OR (us.scope_type='agent' AND EXISTS(SELECT 1 FROM agents a JOIN sites si ON si.id=a.site_id WHERE a.id=us.scope_id AND si.client_id=$1 AND ($2::uuid IS NULL OR si.id=$2)))
 )))`

func routeRecipients(ctx context.Context, q dbQuerier, clientID, siteID, search string, ids []string, onlyIDs bool) ([]models.NotificationRouteRecipient, error) {
	if ids == nil {
		ids = []string{}
	}
	rows, err := q.Query(ctx, `SELECT u.id,u.username,u.email FROM users u
 WHERE u.disabled_at IS NULL AND u.email IS NOT NULL AND u.email<>''
 AND EXISTS(SELECT 1 FROM role_permissions rp WHERE rp.role_key=u.role AND rp.permission='alerts.view')
 AND `+routeRecipientIntersection+`
 AND (NOT $3 OR u.id=ANY($4::uuid[]))
 AND ($5='' OR strpos(lower(u.username),lower($5))>0 OR strpos(lower(u.email),lower($5))>0)
 ORDER BY lower(u.username),u.id LIMIT 201`, notificationUUID(clientID), notificationUUID(siteID), onlyIDs, ids, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.NotificationRouteRecipient{}
	for rows.Next() {
		var r models.NotificationRouteRecipient
		if err = rows.Scan(&r.ID, &r.Username, &r.Email); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) NotificationRouteRecipientOptions(ctx context.Context, scopeType, scopeID, search string) ([]models.NotificationRouteRecipient, bool, error) {
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 200 {
		return nil, false, fmt.Errorf("recipient search too long")
	}
	clientID, siteID, _, err := routingTarget(ctx, s.pool, scopeType, scopeID, false)
	if err != nil {
		return nil, false, err
	}
	users, err := routeRecipients(ctx, s.pool, clientID, siteID, strings.TrimSpace(search), nil, false)
	more := len(users) > 200
	if more {
		users = users[:200]
	}
	return users, more, err
}

func notificationRoutingView(ctx context.Context, q dbQuerier, scopeType, scopeID string) (*models.NotificationRoutingView, error) {
	clientID, siteID, _, err := routingTarget(ctx, q, scopeType, scopeID, false)
	if err != nil {
		return nil, err
	}
	result := &models.NotificationRoutingView{Local: models.NotificationRouteView{Mode: "inherit", UserIDs: []string{}, Recipients: []models.NotificationRouteRecipient{}}}
	var ids []string
	err = q.QueryRow(ctx, `SELECT mode,version,ARRAY(SELECT user_id::text FROM notification_route_users WHERE route_id=nr.id ORDER BY user_id)
 FROM notification_routes nr WHERE ($1='client' AND client_id=$2) OR ($1='site' AND site_id=$2)`, scopeType, scopeID).Scan(&result.Local.Mode, &result.Local.Version, &ids)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	users, err := routeRecipients(ctx, q, clientID, siteID, "", ids, true)
	if err != nil {
		return nil, err
	}
	result.Local.Recipients = users
	for _, u := range users {
		result.Local.UserIDs = append(result.Local.UserIDs, u.ID)
	}
	result.Local.UnavailableCount = len(ids) - len(users)
	effective, name, err := effectiveNotificationRoute(ctx, q, clientID, siteID)
	if err != nil {
		return nil, err
	}
	users, err = routeRecipients(ctx, q, clientID, siteID, "", effective.UserIDs, true)
	if err != nil {
		return nil, err
	}
	result.Effective = models.EffectiveNotificationRouteView{Mode: effective.Mode, ScopeType: effective.ScopeType, ScopeID: effective.ScopeID, SourceName: name, Version: effective.Version, Recipients: users, UnavailableCount: len(effective.UserIDs) - len(users)}
	return result, nil
}

func (s *Store) GetNotificationRouting(ctx context.Context, scopeType, scopeID string) (*models.NotificationRoutingView, error) {
	// Configuration, inherited origin and visible labels belong to one read snapshot.
	tx, err := s.connections.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	result, err := notificationRoutingView(ctx, tx, scopeType, scopeID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) UpdateNotificationRouting(ctx context.Context, scopeType, scopeID, actor string, expected int64, update models.NotificationRouteUpdate) (*models.NotificationRoutingView, error) {
	if expected < 0 {
		return nil, fmt.Errorf("expected_version must be nonnegative")
	}
	if err := update.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	clientID, siteID, _, err := routingTarget(ctx, tx, scopeType, scopeID, true)
	if err != nil {
		return nil, err
	}
	var routeID string
	var version int64
	err = tx.QueryRow(ctx, `SELECT id,version FROM notification_routes WHERE ($1='client' AND client_id=$2) OR ($1='site' AND site_id=$2)`, scopeType, scopeID).Scan(&routeID, &version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if version != expected {
		return nil, ErrConflict
	}
	users, err := routeRecipients(ctx, tx, clientID, siteID, "", update.UserIDs, true)
	if err != nil {
		return nil, err
	}
	if len(users) != len(update.UserIDs) {
		return nil, ErrNotificationRecipients
	}
	if routeID == "" {
		routeID = newID()
		var client, site any
		if scopeType == models.ScopeClient {
			client = scopeID
		} else {
			site = scopeID
		}
		_, err = tx.Exec(ctx, `INSERT INTO notification_routes(id,client_id,site_id,mode,version,updated_by) VALUES($1,$2,$3,$4,1,$5)`, routeID, client, site, update.Mode, actor)
	} else {
		_, err = tx.Exec(ctx, `UPDATE notification_routes SET mode=$2,version=version+1,updated_at=clock_timestamp(),updated_by=$3 WHERE id=$1`, routeID, update.Mode, actor)
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM notification_route_users WHERE route_id=$1`, routeID); err != nil {
		return nil, err
	}
	for _, id := range update.UserIDs {
		if _, err = tx.Exec(ctx, `INSERT INTO notification_route_users(route_id,user_id) VALUES($1,$2)`, routeID, id); err != nil {
			return nil, err
		}
	}
	detail, _ := json.Marshal(map[string]any{"scope_type": scopeType, "version": version + 1, "changed_fields": []string{"mode", "recipients"}, "mode": update.Mode, "recipient_count": len(update.UserIDs)})
	if err = s.addAuditTx(ctx, tx, actor, "notification.routing.update", scopeID, detail); err != nil {
		return nil, err
	}
	result, err := notificationRoutingView(ctx, tx, scopeType, scopeID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func validateNotificationRouteSnapshot(route models.NotificationRouteSnapshot) error {
	if route.Mode == "automatic" {
		if len(route.UserIDs) != 0 || route.ScopeType != "" || route.ScopeID != "" || route.Version != 0 {
			return fmt.Errorf("invalid automatic route")
		}
		return nil
	}
	if route.Mode != "selected" || (route.ScopeType != models.ScopeClient && route.ScopeType != models.ScopeSite) || route.Version < 1 || len(route.UserIDs) > models.MaxNotificationRouteRecipients {
		return fmt.Errorf("invalid selected route")
	}
	if _, err := uuid.Parse(route.ScopeID); err != nil {
		return fmt.Errorf("invalid route origin")
	}
	seen := map[string]bool{}
	for _, id := range route.UserIDs {
		u, err := uuid.Parse(id)
		if err != nil || seen[u.String()] {
			return fmt.Errorf("invalid route recipient")
		}
		seen[u.String()] = true
	}
	return nil
}

func decodeNotificationRouteSnapshot(raw json.RawMessage) (models.NotificationRouteSnapshot, error) {
	var route models.NotificationRouteSnapshot
	if len(raw) > 16384 {
		return route, fmt.Errorf("notification route snapshot too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&route); err != nil {
		return route, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return route, fmt.Errorf("invalid trailing route snapshot")
	}
	if err := validateNotificationRouteSnapshot(route); err != nil {
		return route, err
	}
	return route, nil
}
