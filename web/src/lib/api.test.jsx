import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, clearSession, getStoredUser, getToken, onUnauthorized } from './api.js';

// API errors pass through this layer: preserve the backend's detailed rejection as an Error.
// Handle sessions too: clear expired tokens on 401 so stale sessions do not show empty pages.

function respond({ status = 200, body = '', contentType = 'application/json', headers = {} } = {}) {
  return Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    headers: { get: (k) => ({ 'Content-Type': contentType, ...headers })[k] ?? null },
    text: () => Promise.resolve(typeof body === 'string' ? body : JSON.stringify(body)),
    json: () => Promise.resolve(typeof body === 'string' ? JSON.parse(body || 'null') : body),
  });
}

describe('API client', () => {
  beforeEach(() => {
    localStorage.clear();
    vi.stubGlobal('fetch', vi.fn(() => respond({ body: { items: [] } })));
  });
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

  it('Attaches token and JSON headers', async () => {
    await api.login('admin', 'pw');
    globalThis.fetch.mockClear();
    globalThis.fetch.mockImplementation(() => respond({ body: { items: [] } }));

    await api.importImage({ name: 'win11' });
    const [path, init] = globalThis.fetch.mock.calls[0];
    expect(path).toBe('/api/images/import');
    expect(init.headers.Authorization).toMatch(/^Bearer /);
    expect(init.headers['Content-Type']).toBe('application/json');
    expect(JSON.parse(init.body)).toEqual({ name: 'win11' });
  });

  // Preserve actionable backend rejection messages instead of returning a generic failure.
  it('Passes through detailed backend rejection messages', async () => {
    globalThis.fetch.mockImplementation(() => respond({
      status: 409, contentType: 'text/plain',
      body: 'Delete derived configuration forked before modifying its parent',
    }));
    await expect(api.mergeConfig('cfg-1')).rejects.toThrow('Delete derived configuration forked before modifying its parent');
  });

  it('Reports status code if no backend error message exists', async () => {
    globalThis.fetch.mockImplementation(() => respond({ status: 500, contentType: 'text/plain', body: '   ' }));
    await expect(api.listImages()).rejects.toThrow('Request failed (500)');
  });

  // Login 401 indicates bad credentials, not session expiration; surface the backend message.
  it('Reports invalid credentials on login, not expired session', async () => {
    globalThis.fetch.mockImplementation(() => respond({
      status: 401, contentType: 'text/plain', body: 'Invalid username or password',
    }));
    await expect(api.login('admin', 'wrong')).rejects.toThrow('Invalid username or password');
  });

  it('Does not report session expiration for missing login error messages', async () => {
    globalThis.fetch.mockImplementation(() => respond({ status: 401, contentType: 'text/plain', body: '' }));
    await expect(api.login('admin', 'wrong')).rejects.toThrow(/Invalid username or password/);
  });

  // Expired tokens clear the session and notify the app shell.
  it('Clears session and broadcasts on 401', async () => {
    await api.login('admin', 'pw');
    expect(getToken()).not.toBe('');

    const seen = vi.fn();
    const stop = onUnauthorized.subscribe(seen);
    globalThis.fetch.mockImplementation(() => respond({ status: 401, contentType: 'text/plain', body: 'token expired' }));

    await expect(api.listImages()).rejects.toThrow(/Session expired/);
    expect(getToken()).toBe('');
    expect(getStoredUser()).toBeNull();
    expect(seen).toHaveBeenCalledTimes(1);
    stop();
  });

  it('Stores token and user after login', async () => {
    globalThis.fetch.mockImplementation(() => respond({ body: { token: 'jwt-123', user: { username: 'admin' } } }));
    await api.login('admin', 'pw');
    expect(getToken()).toBe('jwt-123');
    expect(getStoredUser()).toEqual({ username: 'admin' });
    clearSession();
    expect(getToken()).toBe('');
  });

  // 204 has no response body; non-JSON downloads should not cause parse errors.
  it('Returns null for 204 and text for non-JSON responses', async () => {
    globalThis.fetch.mockImplementation(() => respond({ status: 204, contentType: '' }));
    expect(await api.deleteImage('img-1')).toBeNull();

    globalThis.fetch.mockImplementation(() => respond({ contentType: 'text/plain', body: '#!ipxe\nsanboot' }));
    expect(await api.getSettings()).toContain('#!ipxe');
  });

  // Browser sets multipart boundaries; do not force a JSON Content-Type for uploads.
  it('Preserves multipart Content-Type handling', async () => {
    const fd = new FormData();
    fd.append('file', new File(['x'], 'pack.zip'));
    globalThis.fetch.mockImplementation(() => respond({ body: {} }));

    await api.uploadDriverPack(fd);
    const [, init] = globalThis.fetch.mock.calls[0];
    expect(init.headers['Content-Type']).toBeUndefined();
    expect(init.body).toBe(fd);
  });

  // URL-encode IDs, including non-ASCII configuration names and colon-separated MACs.
  it('Encodes URL path parameters', async () => {
    globalThis.fetch.mockImplementation(() => respond({ body: {} }));
    await api.getImage('Café/win11');
    expect(globalThis.fetch.mock.calls[0][0]).toBe(`/api/images/${encodeURIComponent('Café/win11')}`);
  });
});
