// Lightweight fetch client for the Go backend API.
// Domain structs use PascalCase (ID, Name, OSType, MAC, IP, State, IsSuper, Capacity) without JSON tags.
// List endpoints return { items, total }.

const TOKEN_KEY = 'nd_token';
const USER_KEY = 'nd_user';

export function getToken() {
  return localStorage.getItem(TOKEN_KEY) || '';
}
export function getStoredUser() {
  try {
    return JSON.parse(localStorage.getItem(USER_KEY) || 'null');
  } catch {
    return null;
  }
}
function setSession(token, user) {
  localStorage.setItem(TOKEN_KEY, token);
  if (user) localStorage.setItem(USER_KEY, JSON.stringify(user));
}
export function clearSession() {
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(USER_KEY);
}

// A 401 session-expiration event causes the shell to return to login.
export const onUnauthorized = (() => {
  const target = new EventTarget();
  return {
    emit: () => target.dispatchEvent(new Event('unauthorized')),
    subscribe: (fn) => {
      target.addEventListener('unauthorized', fn);
      return () => target.removeEventListener('unauthorized', fn);
    },
  };
})();

async function request(path, { method = 'GET', body, formData, raw } = {}) {
  const headers = {};
  const token = getToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  let payload;
  if (formData !== undefined) {
    payload = formData; // Browser supplies multipart boundary
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    payload = JSON.stringify(body);
  }
  const res = await fetch(path, { method, headers, body: payload });
  if (res.status === 401) {
    // Login 401 means invalid credentials, not session expiry; show the backend message.
    if (path === '/api/login') {
      const text = (await res.text().catch(() => '')).trim();
      throw new Error(text || 'Invalid username or password');
    }
    clearSession();
    onUnauthorized.emit();
    throw new Error('Session expired. Please sign in again');
  }
  if (!res.ok) {
    const text = (await res.text().catch(() => '')).trim();
    throw new Error(text || `Request failed (${res.status})`);
  }
  if (raw) return res;
  if (res.status === 204) return null;
  const ct = res.headers.get('Content-Type') || '';
  if (!ct.includes('application/json')) return res.text();
  return res.json();
}

export const api = {
  // --- Authentication ---
  async login(username, password) {
    const data = await request('/api/login', { method: 'POST', body: { username, password } });
    setSession(data.token, data.user);
    return data;
  },

  // --- Images / configurations / restore points ---
  listImages: () => request('/api/images'),
  getImage: (id) => request(`/api/images/${encodeURIComponent(id)}`),
  importImage: (body) => request('/api/images/import', { method: 'POST', body }),
  createBlankImage: (body) => request('/api/images/blank', { method: 'POST', body }),
  setImagePurpose: (id, body) => request(`/api/images/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  listImportSources: () => request('/api/images/import-sources'),
  exportImage: (id) => request(`/api/images/${encodeURIComponent(id)}/export`, { method: 'POST' }),
  exportReduction: (id) => request(`/api/reductions/${encodeURIComponent(id)}/export`, { method: 'POST' }),
  deleteImage: (id) => request(`/api/images/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  // Browser image uploads are chunked and resumable; multi-gigabyte files cannot be sent in one POST.
  beginUpload: (body) => request('/api/images/uploads', { method: 'POST', body }),
  uploadStatus: (id) => request(`/api/images/uploads/${encodeURIComponent(id)}`),
  abortUpload: (id) => request(`/api/images/uploads/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  // Upload binary chunks with native fetch and Content-Range; request() serializes JSON bodies.
  uploadChunk: async (id, offset, blob, signal) => {
    const res = await fetch(`/api/images/uploads/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      signal,
      headers: {
        'Authorization': 'Bearer ' + (getToken() || ''),
        'Content-Range': `bytes ${offset}-${offset + blob.size - 1}/${blob.size + offset}`,
      },
      body: blob,
    });
    if (!res.ok) throw new Error((await res.text()) || `Upload failed (${res.status})`);
    return res.json();
  },
  exportImageTicket: (id, compress) =>
    request(`/api/images/${encodeURIComponent(id)}/export-ticket${compress ? '?compress=gzip' : ''}`, { method: 'POST' }),
  getImageHealth: (id) => request(`/api/images/${encodeURIComponent(id)}/health`),
  runImageHealthCheck: (id) =>
    request(`/api/images/${encodeURIComponent(id)}/health-check`, { method: 'POST' }),
  getRuntime: () => request('/api/runtime'),
  getSettings: () => request('/api/settings'),
  saveSettings: (body) => request('/api/settings', { method: 'PUT', body }),
  // Client networking: serving interface, cross-subnet groups, and suggested IP range for clientMax devices.
  getHA: () => request('/api/ha'),
  getReplication: () => request('/api/replication'),
  plannedSwitch: () => request('/api/ha/planned-switch', { method: 'POST' }),
  setHARate: (mbps) => request('/api/ha/rate', { method: 'POST', body: { mbps } }),
  // Cluster creation needs only VIP; the product generates the cluster token.
  createCluster: (body) => request('/api/cluster/create', { method: 'POST', body }),
  // Discover unconfigured nodes on the client subnet that can join the cluster.
  discoverNodes: () => request('/api/cluster/discover'),
  adoptNode: (body) => request('/api/cluster/adopt', { method: 'POST', body }),
  listClusterNodes: () => request('/api/cluster/nodes'),
  listClusterServices: () => request('/api/cluster/services'),
  // Removing a decommissioned node is irreversible; confirm its address in the UI.
  forgetClusterNode: (node) =>
    request(`/api/cluster/nodes/${encodeURIComponent(node)}`, { method: 'DELETE' }),
  listClusterPools: () => request('/api/cluster/pools'),
  clusterServiceAction: (node, key, action) =>
    request(`/api/cluster/nodes/${encodeURIComponent(node)}/services/${encodeURIComponent(key)}/action`,
      { method: 'POST', body: { action } }),
  // Execute pool mutations on the owning node; omitted target means local.
  clusterPoolPath: (node, suffix = '') =>
    `/api/cluster/nodes/${encodeURIComponent(node)}/pools${suffix}`,
  // List target-node disks for pool creation; /dev/sdb refers to different hardware on each node.
  listClusterDisks: (node) => request(`/api/cluster/nodes/${encodeURIComponent(node)}/disks`),
  clusterDestroyPool: (node, id) =>
    request(`/api/cluster/nodes/${encodeURIComponent(node)}/pools/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  clusterPoolAction: (node, id, suffix, body) =>
    request(`/api/cluster/nodes/${encodeURIComponent(node)}/pools/${encodeURIComponent(id)}${suffix}`,
      { method: 'POST', body }),
  clusterCreatePool: (node, body) =>
    request(`/api/cluster/nodes/${encodeURIComponent(node)}/pools`, { method: 'POST', body }),
  clusterServiceLogs: (node, key, lines = 200) =>
    request(`/api/cluster/nodes/${encodeURIComponent(node)}/services/${encodeURIComponent(key)}/logs?lines=${lines}`),
  getNetwork: (clientMax) => request(`/api/network${clientMax ? `?client_max=${encodeURIComponent(clientMax)}` : ''}`),
  saveNetwork: (body) => request('/api/network', { method: 'PUT', body }),
  probeNetwork: (ip) => request('/api/network/probe', { method: 'POST', body: { ip } }),
  previewGroupNetwork: (id, body) => request(`/api/groups/${encodeURIComponent(id)}/network-preview`, { method: 'POST', body }),

  listConfigs: (imageId) => request(`/api/images/${encodeURIComponent(imageId)}/configs`),
  createConfigFromImage: (imageId, body) =>
    request(`/api/images/${encodeURIComponent(imageId)}/configs`, { method: 'POST', body }),
  forkConfig: (configId, body) =>
    request(`/api/configs/${encodeURIComponent(configId)}/fork`, { method: 'POST', body }),
  mergeConfig: (configId) =>
    request(`/api/configs/${encodeURIComponent(configId)}/merge`, { method: 'POST' }),
  deleteConfig: (configId) =>
    request(`/api/configs/${encodeURIComponent(configId)}`, { method: 'DELETE' }),

  listReductions: (configId) => request(`/api/configs/${encodeURIComponent(configId)}/reductions`),
  applyReduction: (id) => request(`/api/reductions/${encodeURIComponent(id)}/apply`, { method: 'POST' }),
  exportReductionTicket: (id, compress) =>
    request(`/api/reductions/${encodeURIComponent(id)}/export-ticket${compress ? '?compress=gzip' : ''}`, { method: 'POST' }),
  saveReductionAsImage: (id, body) =>
    request(`/api/reductions/${encodeURIComponent(id)}/save-as-image`, { method: 'POST', body }),
  overwriteImage: (id) => request(`/api/reductions/${encodeURIComponent(id)}/overwrite-image`, { method: 'POST' }),
  mergeReductions: (configId, body) =>
    request(`/api/configs/${encodeURIComponent(configId)}/reductions/merge`, { method: 'POST', body }),
  deleteReduction: (reductionId) =>
    request(`/api/reductions/${encodeURIComponent(reductionId)}`, { method: 'DELETE' }),

  // --- Groups ---
  listGroups: () => request('/api/groups'),
  getGroup: (id) => request(`/api/groups/${encodeURIComponent(id)}`),
  defaultGroup: () => request('/api/groups/default'),
  createGroup: (body) => request('/api/groups', { method: 'POST', body }),
  updateGroup: (id, body) => request(`/api/groups/${encodeURIComponent(id)}`, { method: 'PUT', body }),
  deleteGroup: (id) => request(`/api/groups/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  setDefaultGroup: (id) => request(`/api/groups/${encodeURIComponent(id)}/default`, { method: 'POST' }),
  listGroupDisks: (groupId) => request(`/api/groups/${encodeURIComponent(groupId)}/disks`),
  createGroupDisk: (groupId, body) => request(`/api/groups/${encodeURIComponent(groupId)}/disks`, { method: 'POST', body }),
  deleteGroupDisk: (id) => request(`/api/group-disks/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // --- Clients ---
  listTerminals: () => request('/api/terminals'),
  getTerminal: (id) => request(`/api/terminals/${encodeURIComponent(id)}`),
  createTerminal: (body) => request('/api/terminals', { method: 'POST', body }),
  updateTerminal: (id, body) => request(`/api/terminals/${encodeURIComponent(id)}`, { method: 'PUT', body }),
  deleteTerminal: (id) => request(`/api/terminals/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  moveTerminals: (body) => request('/api/terminals/move', { method: 'POST', body }),
  importTerminals: (formData) => request('/api/terminals/import', { method: 'POST', formData }),
  exportTerminals: (groupId) => request(`/api/terminals/export${groupId ? `?group_id=${encodeURIComponent(groupId)}` : ''}`, { raw: true }),
  terminalTemplate: () => request('/api/terminals/template', { raw: true }),
  enableSuper: (id) => request(`/api/terminals/${encodeURIComponent(id)}/super`, { method: 'POST' }),
  disableSuper: (id) => request(`/api/terminals/${encodeURIComponent(id)}/super`, { method: 'DELETE' }),
  stopSuper: (id, body) => request(`/api/terminals/${encodeURIComponent(id)}/super/stop`, { method: 'POST', body }),
  // Publish data disks online: briefly take only the selected disk offline.
  superDisks: (id) => request(`/api/terminals/${encodeURIComponent(id)}/super/disks`),
  publishDataDisk: (id, body) => request(`/api/terminals/${encodeURIComponent(id)}/super/publish`, { method: 'POST', body }),
  injectDriver: (id, body) => request(`/api/terminals/${encodeURIComponent(id)}/inject-driver`, { method: 'POST', body }),
  injectResult: (id) => request(`/api/terminals/${encodeURIComponent(id)}/inject-result`),

  // --- Storage Pools ---
  listPools: () => request('/api/pools'),
  listDisks: () => request('/api/storage/disks'),
  getPool: (id) => request(`/api/pools/${encodeURIComponent(id)}`),
  createPool: (body) => request('/api/pools', { method: 'POST', body }),
  deletePool: (id) => request(`/api/pools/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  addPoolDisk: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/disks`, { method: 'POST', body }),
  removePoolDisk: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/disks/remove`, { method: 'POST', body }),
  replacePoolDisk: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/disks/replace`, { method: 'POST', body }),
  mirrorUpgradePool: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/mirror-upgrade`, { method: 'POST', body }),
  addSpecial: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/special`, { method: 'POST', body }),
  removeSpecial: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/special/remove`, { method: 'POST', body }),
  addSpare: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/spares`, { method: 'POST', body }),
  removeSpare: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/spares/remove`, { method: 'POST', body }),
  addReadCache: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/read-cache`, { method: 'POST', body }),
  removeReadCache: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/read-cache/remove`, { method: 'POST', body }),
  addWriteCache: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/write-cache`, { method: 'POST', body }),
  removeWriteCache: (id, body) => request(`/api/pools/${encodeURIComponent(id)}/write-cache/remove`, { method: 'POST', body }),
  flushWriteCache: (id) => request(`/api/pools/${encodeURIComponent(id)}/write-cache/flush`, { method: 'POST' }),

  // --- Drivers ---
  listDriverPacks: () => request('/api/driver-packs'),
  uploadDriverPack: (formData) => request('/api/driver-packs', { method: 'POST', formData }),
  setDriverPackStatus: (id, status) =>
    request(`/api/driver-packs/${encodeURIComponent(id)}/status`, { method: 'POST', body: { status } }),
  recommendDriverPack: (id) =>
    request(`/api/driver-packs/${encodeURIComponent(id)}/recommend`, { method: 'POST' }),
  deleteDriverPack: (id) => request(`/api/driver-packs/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  listDriverBundles: () => request('/api/driver-bundles'),
  createDriverBundle: (body) => request('/api/driver-bundles', { method: 'POST', body }),
  deleteDriverBundle: (id) => request(`/api/driver-bundles/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  downloadDriverBundle: (id) =>
    request(`/api/driver-bundles/${encodeURIComponent(id)}/archive`, { raw: true }),

  // --- Alerts ---
  listAlarms: ({ status = '', severity = '', page = 1, size = 50 } = {}) => {
    const p = new URLSearchParams({ page: String(page), size: String(size) });
    if (status) p.set('status', status);
    if (severity) p.set('severity', severity);
    return request(`/api/alarms?${p.toString()}`);
  },
  ackAlarm: (id) => request(`/api/alarms/${encodeURIComponent(id)}/ack`, { method: 'POST' }),
  deleteAlarm: (id) => request(`/api/alarms/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // --- Audit / Logs ---
  listLogs: ({ type = '', user = '', module = '', status = '', q = '', from = '', to = '', page = 1, size = 20 } = {}) => {
    const p = new URLSearchParams({ page: String(page), size: String(size) });
    if (type) p.set('type', type);
    if (user) p.set('user', user);
    if (module) p.set('module', module);
    if (status) p.set('status', status);
    if (q) p.set('q', q);
    if (from) p.set('from', from);
    if (to) p.set('to', to);
    return request(`/api/logs?${p.toString()}`);
  },

  // --- Servers and Services ---
  getServer: () => request('/api/server'),
  listServices: () => request('/api/services'),
  serviceAction: (key, action) =>
    request(`/api/services/${encodeURIComponent(key)}/action`, { method: 'POST', body: { action } }),
  serviceLogs: (key, lines = 200) =>
    request(`/api/services/${encodeURIComponent(key)}/logs?lines=${lines}`),

  // --- Backup ---
  getBackupStatus: () => request('/api/backup/status'),
  listClusterBackups: () => request('/api/cluster/backups'),
  saveBackupConfig: (body) => request('/api/backup/config', { method: 'PUT', body }),
  runBackup: () => request('/api/backup/run', { method: 'POST' }),

  // --- Users ---
  listUsers: () => request('/api/users'),
  createUser: (body) => request('/api/users', { method: 'POST', body }),
  updateUser: (id, body) => request(`/api/users/${encodeURIComponent(id)}`, { method: 'PUT', body }),
  deleteUser: (id) => request(`/api/users/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  changeUserPassword: (id, body) => request(`/api/users/${encodeURIComponent(id)}/password`, { method: 'POST', body }),


  // --- Hardware Inventory ---

  // --- Tasks ---
  getTask: (id) => request(`/api/tasks/${encodeURIComponent(id)}`),
  listTasks: () => request('/api/tasks'),
  listTaskHistory: ({ page = 1, size = 20, status = '' } = {}) => {
    const q = new URLSearchParams({ page: String(page), size: String(size) });
    if (status) q.set('status', status);
    return request(`/api/tasks/history?${q.toString()}`);
  },
};
