// Selection and wording for moving and deleting devices in bulk, and for the
// proposals the AI writes for the same operations. Kept out of the components
// so it can be tested without a browser. Plain JS: the build concatenates these
// files into blocks, so anything shared goes through window.
const DeviceBulk = {
  // The server's limit for one operation; the console enforces it before
  // sending so the refusal is never a surprise.
  LIMIT: 500,

  toggle(selected, id) {
    return selected.includes(id) ? selected.filter((x) => x !== id) : [...selected, id];
  },

  // The header checkbox acts on what is visible. With a filter applied,
  // "select all" meaning every device in the fleet would put devices the
  // operator cannot see into a delete.
  allVisibleSelected(selected, visibleIds) {
    return visibleIds.length > 0 && visibleIds.every((id) => selected.includes(id));
  },

  toggleAllVisible(selected, visibleIds) {
    if (DeviceBulk.allVisibleSelected(selected, visibleIds)) {
      return selected.filter((id) => !visibleIds.includes(id));
    }
    const add = visibleIds.filter((id) => !selected.includes(id));
    return [...selected, ...add];
  },

  // Devices that disappeared (deleted elsewhere, or out of scope now) must not
  // linger in a selection that is about to be acted on.
  prune(selected, existingIds) {
    const set = new Set(existingIds);
    const kept = selected.filter((id) => set.has(id));
    return kept.length === selected.length ? selected : kept;
  },

  countLabel(n) {
    return tn('bulk.devices', n);
  },

  // The deletion is confirmed by typing how many devices it removes.
  confirmMatches(typed, count) {
    return String(typed == null ? '' : typed).trim() === String(count);
  },

  // Who wrote a proposal, in words: 'ai:alice' is the assistant working for
  // alice, 'mcp:api' an external MCP client.
  proposer(by) {
    const s = String(by || '');
    if (s.startsWith('ai:')) return t('bulk.proposer.ai', { user: s.slice(3) || t('bulk.unknown') });
    if (s.startsWith('mcp:')) return t('bulk.proposer.mcp', { client: s.slice(4) || t('bulk.unknown') });
    return s || t('bulk.unknown');
  },

  // One line describing a proposal, e.g. "Move 3 devices to HQ ▸ Finance".
  describe(op, names) {
    const n = DeviceBulk.countLabel((op && op.agent_ids ? op.agent_ids.length : 0));
    if (!op) return '';
    if (op.kind === 'delete') return t('bulk.delete', { devices: n });
    // Without the site list (the dashboard does not load it) the destination
    // is left out rather than guessed.
    if (!names || !names.site) return t('bulk.move', { devices: n });
    const site = (names && names.site && names.site(op.site_id)) || t('bulk.anotherSite');
    const dept = op.group_id ? ((names && names.group && names.group(op.group_id)) || t('bulk.aDepartment')) : '';
    return t('bulk.moveTo', { devices: n, dest: site + (dept ? ' ▸ ' + dept : '') });
  },

  // The permission deciding a proposal takes: the one the operation itself
  // would need by hand.
  decidePermission(op) {
    return op && op.kind === 'delete' ? 'devices.delete' : 'clients.manage';
  },

  // The dashboard's "Awaiting approval" line under the count: says what is
  // waiting when it is more than one kind of thing.
  approvalSummary(jobs, ops) {
    if (ops > 0 && jobs > 0) return tn('bulk.jobs', jobs) + ' · ' + tn('bulk.ops', ops);
    if (ops > 0) return tn('bulk.ops', ops);
    return t('bulk.needsHuman');
  },

  awaiting(ops) {
    return (ops || []).filter((op) => op.status === 'awaiting_approval');
  },

  // Devices still in a site: the console will not offer to delete it until
  // they are moved or deleted, and says so instead of failing.
  siteDeviceCount(devices, siteId) {
    return (devices || []).filter((d) => d.site_id === siteId).length;
  },
};

window.DeviceBulk = DeviceBulk;
