package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// Reorganising and deleting devices on the AI's behalf.
//
// The AI cannot move or delete anything. propose_move_devices and
// propose_delete_devices write a device_operations row that does nothing until
// a person approves it in the console; the server then carries it out with the
// APPROVER's permissions and scope, checked again at that moment. The checks
// made here, against the caller's own scope, only keep the AI from proposing
// what its user could not see — they are not what authorises the change.

var deviceToolDefs = []Tool{
	{Type: "function", Function: FunctionDef{
		Name:        "list_org",
		Description: "List the companies (clients), their sites and each site's departments, with ids. Use it to find the destination site_id / group_id before propose_move_devices, and to resolve names the user gives (\"move them to the Porto Alegre site\").",
		Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}},
	{Type: "function", Function: FunctionDef{
		Name:        "propose_move_devices",
		Description: "Propose moving one or more devices to a site, optionally into one of that site's departments. DOES NOT MOVE ANYTHING: it creates a proposal that a person must approve in the console (Devices ▸ Pending operations). Get device ids from list_devices and the site/department ids from list_org. Up to 500 devices per proposal.",
		Parameters: json.RawMessage(`{"type":"object","properties":{
			"agent_ids":{"type":"array","items":{"type":"string"},"description":"device ids to move (from list_devices)"},
			"site_id":{"type":"string","description":"destination site id (from list_org)"},
			"group_id":{"type":"string","description":"optional destination department id; must belong to site_id"},
			"description":{"type":"string","description":"one-line reason shown to the person reviewing it"}
		},"required":["agent_ids","site_id","description"],"additionalProperties":false}`),
	}},
	{Type: "function", Function: FunctionDef{
		Name:        "propose_delete_devices",
		Description: "Propose DELETING devices from the RMM: they disappear from the console and their credentials stop working (the agent stays installed on the machine until it is uninstalled there). DOES NOT DELETE ANYTHING: a person must approve the proposal in the console and type the number of devices to confirm. Use only when the user explicitly asks to remove devices, and say which ones. Up to 500 devices per proposal.",
		Parameters: json.RawMessage(`{"type":"object","properties":{
			"agent_ids":{"type":"array","items":{"type":"string"},"description":"device ids to delete (from list_devices)"},
			"description":{"type":"string","description":"one-line reason shown to the person reviewing it"}
		},"required":["agent_ids","description"],"additionalProperties":false}`),
	}},
}

func deviceToolPermission(name string) (string, bool) {
	switch name {
	case "list_org":
		return models.PermClientsView, true
	case "propose_move_devices":
		return models.PermClientsManage, true
	case "propose_delete_devices":
		return models.PermDevicesDelete, true
	}
	return "", false
}

func executeDeviceTool(ctx context.Context, tc ToolContext, name string, args json.RawMessage) (ToolResult, bool) {
	switch name {
	case "list_org":
		return jsonResult(listOrg(ctx, tc)), true
	case "propose_move_devices":
		return jsonResult(proposeMoveDevices(ctx, tc, args)), true
	case "propose_delete_devices":
		return jsonResult(proposeDeleteDevices(ctx, tc, args)), true
	}
	return ToolResult{}, false
}

func listOrg(ctx context.Context, tc ToolContext) (any, error) {
	var (
		clients []models.Client
		sites   []models.Site
		groups  []models.DeviceGroup
		err     error
	)
	if tc.ScopeUserID != "" {
		if clients, err = tc.Store.ListClientsForUser(ctx, tc.ScopeUserID); err == nil {
			if sites, err = tc.Store.ListSitesForUser(ctx, tc.ScopeUserID, ""); err == nil {
				groups, err = tc.Store.ListGroupsForUser(ctx, tc.ScopeUserID)
			}
		}
	} else {
		if clients, err = tc.Store.ListClients(ctx); err == nil {
			if sites, err = tc.Store.ListSites(ctx, ""); err == nil {
				groups, err = tc.Store.ListGroups(ctx)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	type dept struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type site struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Departments []dept `json:"departments"`
	}
	type client struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Sites []site `json:"sites"`
	}
	bySite := map[string][]dept{}
	for _, g := range groups {
		if g.SiteID != nil {
			bySite[*g.SiteID] = append(bySite[*g.SiteID], dept{g.ID, g.Name})
		}
	}
	byClient := map[string][]site{}
	for _, s := range sites {
		d := bySite[s.ID]
		if d == nil {
			d = []dept{}
		}
		byClient[s.ClientID] = append(byClient[s.ClientID], site{s.ID, s.Name, d})
	}
	out := make([]client, 0, len(clients))
	for _, c := range clients {
		ss := byClient[c.ID]
		if ss == nil {
			ss = []site{}
		}
		out = append(out, client{c.ID, c.Name, ss})
	}
	return map[string]any{"clients": out}, nil
}

// proposalAgents validates the device list the model sent: well-formed,
// de-duplicated, within the limit, existing, and within the caller's scope.
func proposalAgents(ctx context.Context, tc ToolContext, raw []string) ([]string, error) {
	seen := map[string]bool{}
	ids := make([]string, 0, len(raw))
	for _, r := range raw {
		parsed, err := uuid.Parse(strings.TrimSpace(r))
		if err != nil {
			return nil, fmt.Errorf("invalid device id %q; use the ids from list_devices", r)
		}
		if id := parsed.String(); !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("agent_ids is empty")
	}
	if len(ids) > store.MaxBulkDevices {
		return nil, fmt.Errorf("%d devices in one proposal; the limit is %d — split it", len(ids), store.MaxBulkDevices)
	}
	hosts, err := tc.Store.AgentHostnames(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !tc.allows(ctx, id) {
			return nil, fmt.Errorf("device %s is outside your access scope", id)
		}
		if _, ok := hosts[id]; !ok {
			return nil, fmt.Errorf("device %s does not exist", id)
		}
	}
	return ids, nil
}

func (tc ToolContext) scopeUsable(ctx context.Context, scopeType, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("invalid %s id %q; use the ids from list_org", scopeType, id)
	}
	var ok bool
	var err error
	if tc.ScopeUserID != "" {
		ok, err = tc.Store.ScopeAllowedForUser(ctx, tc.ScopeUserID, scopeType, id)
	} else {
		ok, err = tc.Store.ScopeExists(ctx, scopeType, id)
	}
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s %s does not exist or is outside your access scope", scopeType, id)
	}
	return nil
}

func proposeMoveDevices(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		AgentIDs    []string `json:"agent_ids"`
		SiteID      string   `json:"site_id"`
		GroupID     string   `json:"group_id"`
		Description string   `json:"description"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	ids, err := proposalAgents(ctx, tc, in.AgentIDs)
	if err != nil {
		return nil, err
	}
	siteID := strings.TrimSpace(in.SiteID)
	if siteID == "" {
		return nil, fmt.Errorf("site_id is required")
	}
	if err := tc.scopeUsable(ctx, models.ScopeSite, siteID); err != nil {
		return nil, err
	}
	var groupID *string
	if g := strings.TrimSpace(in.GroupID); g != "" {
		if err := tc.scopeUsable(ctx, models.ScopeGroup, g); err != nil {
			return nil, err
		}
		group, err := tc.Store.GetGroup(ctx, g)
		if err != nil {
			return nil, err
		}
		if group.SiteID == nil || *group.SiteID != siteID {
			return nil, fmt.Errorf("department %s does not belong to site %s", g, siteID)
		}
		groupID = &g
	}
	op, err := tc.Store.CreateDeviceOperation(ctx, &models.DeviceOperation{
		Kind: models.DeviceOpMove, AgentIDs: ids, SiteID: &siteID, GroupID: groupID,
		Description: strings.TrimSpace(in.Description), ProposedBy: tc.proposalActor(),
	})
	if err != nil {
		return nil, err
	}
	return deviceOpResult(op), nil
}

func proposeDeleteDevices(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		AgentIDs    []string `json:"agent_ids"`
		Description string   `json:"description"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	ids, err := proposalAgents(ctx, tc, in.AgentIDs)
	if err != nil {
		return nil, err
	}
	op, err := tc.Store.CreateDeviceOperation(ctx, &models.DeviceOperation{
		Kind: models.DeviceOpDelete, AgentIDs: ids,
		Description: strings.TrimSpace(in.Description), ProposedBy: tc.proposalActor(),
	})
	if err != nil {
		return nil, err
	}
	return deviceOpResult(op), nil
}

func deviceOpResult(op *models.DeviceOperation) map[string]any {
	return map[string]any{
		"operation_id": op.ID,
		"kind":         op.Kind,
		"devices":      len(op.AgentIDs),
		"status":       op.Status,
		"message": "PROPOSAL CREATED. NOTHING HAS CHANGED: no device was moved or deleted. " +
			"A person must review and approve it in the console under Devices ▸ Pending operations. " +
			"Tell the user it is waiting for their approval.",
	}
}
