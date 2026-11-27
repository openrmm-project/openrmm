-- OS observations are separate from requests: sending a reboot is not proof
-- that it happened. Legacy agents retain an unknown observation status.
ALTER TABLE agents ADD COLUMN reboot_status TEXT NOT NULL DEFAULT 'unknown'
 CHECK (reboot_status IN ('unknown','required','not_required'));
ALTER TABLE agents ADD COLUMN reboot_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN reboot_checked_at TIMESTAMPTZ;
ALTER TABLE agents ADD COLUMN reboot_boot_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN reboot_requested_at TIMESTAMPTZ;
ALTER TABLE agents ADD COLUMN reboot_observed_at TIMESTAMPTZ;
ALTER TABLE agents ADD COLUMN reboot_request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN reboot_request_previous_at TIMESTAMPTZ;

-- Before OS observations, pending flags came from patch installation results.
ALTER TABLE agents ADD COLUMN reboot_patch_required BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE agents ADD COLUMN reboot_patch_required_at TIMESTAMPTZ;
UPDATE agents SET reboot_patch_required=TRUE,
 reboot_patch_required_at=COALESCE(reboot_pending_since, now()) WHERE reboot_pending;

-- Keep result identities after a clear: delayed A-B-A delivery must not create
-- another reboot generation. Empty IDs share one conservative lifetime slot.
CREATE TABLE agent_reboot_patch_results (
 agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 request_id TEXT NOT NULL CHECK (octet_length(request_id)<=128),
 PRIMARY KEY (agent_id, request_id)
);
INSERT INTO agent_reboot_patch_results (agent_id,request_id)
 SELECT id,'' FROM agents WHERE reboot_pending;
