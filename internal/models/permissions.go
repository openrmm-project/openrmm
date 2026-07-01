package models

import "time"

// Permission keys used by the backend and the browser UI. Roles are just named
// sets of these permissions; access scope remains separate per user.
const (
	PermAIChatUse = "ai_chat.use"
	PermMCPUse    = "mcp.use"

	PermAlertsView   = "alerts.view"
	PermAlertsManage = "alerts.manage"

	PermJobsView   = "jobs.view"
	PermJobsCreate = "jobs.create"
	PermJobsManage = "jobs.manage"

	PermChecksView   = "checks.view"
	PermChecksManage = "checks.manage"

	PermTasksView   = "tasks.view"
	PermTasksManage = "tasks.manage"

	PermPoliciesView   = "policies.view"
	PermPoliciesManage = "policies.manage"

	PermScriptsView   = "scripts.view"
	PermScriptsManage = "scripts.manage"

	PermPatchesView         = "patches.view"
	PermPatchesManage       = "patches.manage"
	PermPatchPoliciesManage = "patches.policies.manage"

	PermDevicesView     = "devices.view"
	PermDevicesManage   = "devices.manage"
	PermDevicesDelete   = "devices.delete"
	PermDevicesRemote   = "devices.remote"
	PermDevicesFiles    = "devices.files"
	PermDevicesTerminal = "devices.terminal"

	PermClientsView   = "clients.view"
	PermClientsManage = "clients.manage"

	PermReportsView = "reports.view"
	PermAuditView   = "audit.view"

	PermSettingsManage   = "settings.manage"
	PermEnrollmentManage = "enrollment.manage"
	PermAPITokensManage  = "api_tokens.manage"
	PermUsersManage      = "users.manage"
	PermRolesManage      = "roles.manage"
)

type Permission struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Group       string `json:"group"`
}

type Role struct {
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	System      bool      `json:"system"`
	Permissions []string  `json:"permissions"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func PermissionCatalog() []Permission {
	return []Permission{
		{PermAIChatUse, "AI Chat", "Open AI Chat and send messages.", "AI"},
		{PermMCPUse, "MCP", "Connect external MCP clients to AI tools.", "AI"},
		{PermAlertsView, "View alerts", "List and inspect alerts.", "Alerts"},
		{PermAlertsManage, "Manage alerts", "Acknowledge and resolve alerts.", "Alerts"},
		{PermJobsView, "View jobs", "List jobs and approval history.", "Jobs"},
		{PermJobsCreate, "Create jobs", "Create approval-gated jobs and bulk deployments.", "Jobs"},
		{PermJobsManage, "Manage jobs", "Approve, reject, and cancel jobs.", "Jobs"},
		{PermChecksView, "View checks", "List checks and check results.", "Checks"},
		{PermChecksManage, "Manage checks", "Create, edit, and delete checks.", "Checks"},
		{PermTasksView, "View scheduled tasks", "List scheduled automation tasks.", "Scheduled Tasks"},
		{PermTasksManage, "Manage scheduled tasks", "Create, edit, enable, disable, and delete scheduled tasks.", "Scheduled Tasks"},
		{PermPoliciesView, "View policies", "List monitoring policies and assignments.", "Policies"},
		{PermPoliciesManage, "Manage policies", "Create, assign, and delete monitoring policies.", "Policies"},
		{PermScriptsView, "View scripts", "List and inspect script library entries.", "Scripts"},
		{PermScriptsManage, "Manage scripts", "Create, edit, and delete script library entries.", "Scripts"},
		{PermPatchesView, "View patches", "View patch compliance, catalog, and per-device patch state.", "Patches"},
		{PermPatchesManage, "Manage patches", "Scan, approve, install patches, and request reboots.", "Patches"},
		{PermPatchPoliciesManage, "Manage patch policies", "Create, edit, and delete patch maintenance policies.", "Patches"},
		{PermDevicesView, "View devices", "List devices, details, and inventory.", "Devices"},
		{PermDevicesManage, "Manage device metadata", "Edit device description, notes, and remote IDs.", "Devices"},
		{PermDevicesDelete, "Delete devices", "Remove devices from OpenRMM.", "Devices"},
		{PermDevicesRemote, "Remote access", "Start remote desktop sessions.", "Devices"},
		{PermDevicesFiles, "File transfer", "Open device file transfer sessions.", "Devices"},
		{PermDevicesTerminal, "Terminal", "Open elevated terminal sessions.", "Devices"},
		{PermClientsView, "View clients & sites", "List clients, sites, and departments.", "Clients & Sites"},
		{PermClientsManage, "Manage clients & sites", "Create, rename, delete, and assign clients, sites, and departments.", "Clients & Sites"},
		{PermReportsView, "View reports", "Open inventory and compliance reports.", "Reports"},
		{PermAuditView, "View audit log", "Read the audit log.", "Administration"},
		{PermSettingsManage, "Manage configuration", "Edit instance configuration.", "Administration"},
		{PermEnrollmentManage, "Manage enrollment", "Create enrollment tokens.", "Administration"},
		{PermAPITokensManage, "Manage API tokens", "Create and revoke API tokens.", "Administration"},
		{PermUsersManage, "Manage users", "Create, update, scope, and delete users.", "Administration"},
		{PermRolesManage, "Manage roles", "Create, update, and delete custom roles.", "Administration"},
	}
}

func ValidPermission(key string) bool {
	for _, p := range PermissionCatalog() {
		if p.Key == key {
			return true
		}
	}
	return false
}

func DefaultRolePermissions(role string) []string {
	var perms []string
	switch role {
	case RoleReadOnly:
		perms = []string{
			PermDevicesView,
			PermDevicesRemote,
			PermClientsView,
		}
	case RoleTech:
		perms = []string{
			PermAIChatUse, PermMCPUse,
			PermAlertsView, PermAlertsManage,
			PermJobsView, PermJobsCreate, PermJobsManage,
			PermChecksView,
			PermTasksView,
			PermPoliciesView,
			PermScriptsView,
			PermPatchesView, PermPatchesManage,
			PermDevicesView, PermDevicesManage, PermDevicesRemote, PermDevicesFiles, PermDevicesTerminal,
			PermClientsView,
			PermReportsView,
		}
	case RoleAdmin:
		perms = []string{
			PermAIChatUse, PermMCPUse,
			PermAlertsView, PermAlertsManage,
			PermJobsView, PermJobsCreate, PermJobsManage,
			PermChecksView, PermChecksManage,
			PermTasksView, PermTasksManage,
			PermPoliciesView, PermPoliciesManage,
			PermScriptsView, PermScriptsManage,
			PermPatchesView, PermPatchesManage, PermPatchPoliciesManage,
			PermDevicesView, PermDevicesManage, PermDevicesDelete, PermDevicesRemote, PermDevicesFiles, PermDevicesTerminal,
			PermClientsView, PermClientsManage,
			PermReportsView,
			PermAuditView,
			PermSettingsManage, PermEnrollmentManage, PermAPITokensManage, PermUsersManage, PermRolesManage,
		}
	default:
		return nil
	}
	return append([]string(nil), perms...)
}

func DefaultRoleInfo(role string) (name, description string, ok bool) {
	switch role {
	case RoleReadOnly:
		return "Read-only", "View devices, clients, sites, and start remote desktop sessions.", true
	case RoleTech:
		return "Technician", "Operate the fleet: jobs, alerts, scripts, patches, reports, and remote tools.", true
	case RoleAdmin:
		return "Administrator", "Full access to every module and administrative setting.", true
	default:
		return "", "", false
	}
}
