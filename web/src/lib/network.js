// Shared client subnet math: determine whether a group's IP allocation lies inside the server's client subnet.
// Match assets.ClassifyGroupNetwork for form previews; server-side validation remains authoritative.

export const ip4ToInt = (s) => {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(String(s || '').trim());
  if (!m) return null;
  const parts = m.slice(1).map(Number);
  if (parts.some(p => p > 255)) return null;
  return ((parts[0] << 24) >>> 0) + (parts[1] << 16) + (parts[2] << 8) + parts[3];
};

export const intToIp4 = (n) => [n >>> 24, (n >>> 16) & 255, (n >>> 8) & 255, n & 255].join('.');

// prefixContains("192.168.50.1/24", "192.168.50.77") → true
export const prefixContains = (prefix, ip) => {
  const [addr, bitsRaw] = String(prefix || '').split('/');
  const a = ip4ToInt(addr), n = ip4ToInt(ip), bits = Number(bitsRaw);
  if (a === null || n === null || !(bits >= 0 && bits <= 32)) return false;
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
  return ((a & mask) >>> 0) === ((n & mask) >>> 0);
};

// Group allocation range: start through start+max-1.
export const groupWindow = (startIP, clientMax) => {
  const start = ip4ToInt(startIP), max = Number(clientMax) || 0;
  if (start === null || max <= 0) return null;
  return { start, end: start + max - 1 };
};

// classifyGroupNetwork(form-ish {start_ip, client_max}, networkView) →
// 'same' | 'relay' | 'blocked' | 'unknown'
export const classifyGroupNetwork = ({ start_ip, client_max }, network) => {
  if (!network || !network.known || !(network.client_networks || []).length) return 'unknown';
  const win = groupWindow(start_ip, client_max);
  if (!win) return 'unknown';
  const inside = network.client_networks.some(p => prefixContains(p, intToIp4(win.start)) && prefixContains(p, intToIp4(win.end)));
  if (inside) return 'same';
  return network.allow_cross_subnet ? 'relay' : 'blocked';
};

// Example switch-side DHCP relay configuration with the server address filled in.
export const relaySnippet = (serverAddr) => `Configure DHCP relay on the client VLAN's Layer 3 interface, pointing to ${serverAddr}:
  Cisco     ip helper-address ${serverAddr}
  Huawei/H3C dhcp select relay
            dhcp relay server-ip ${serverAddr}
  Linux     dnsmasq: dhcp-relay=<subnet-gateway>,${serverAddr}
Ensure the client subnet can route to ${serverAddr} on ports 18080 (HTTP), 69 (TFTP), and 3260 (iSCSI).
Set the group's Gateway to this VLAN's Layer 3 relay interface address.`;
