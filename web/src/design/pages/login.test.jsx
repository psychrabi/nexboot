import React from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LoginPage } from './login.jsx';

// Directly assigning globalThis.fetch is not covered by restoreAllMocks; restore it manually
// to prevent leaking into neighboring test files and causing timeouts.
let realFetch;
beforeEach(() => { realFetch = globalThis.fetch; });
afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

// Before login, the footer may show only the entered server address and public /healthz status.
// Never invent the node name or claim a single-node deployment.
describe('Login page footer', () => {
  it('Shows the actual connection address instead of an invented hostname', async () => {
    globalThis.fetch = vi.fn(async () => ({ ok: true, status: 200 }));
    render(<LoginPage onLogin={() => {}}/>);
    await waitFor(() => expect(screen.getByText(new RegExp(window.location.host))).toBeTruthy());
    expect(screen.queryByText(/srv-master-01/)).toBeNull();
    expect(screen.queryByText(/10\.0\.0\.10/)).toBeNull();
  });

  it('Does not imply a single-node deployment before login', async () => {
    globalThis.fetch = vi.fn(async () => ({ ok: true, status: 200 }));
    render(<LoginPage onLogin={() => {}}/>);
    await waitFor(() => expect(screen.getByText(new RegExp(window.location.host))).toBeTruthy());
    expect(screen.queryByText(/single node/)).toBeNull();
  });

  it('Reports unhealthy services rather than showing a green status', async () => {
    globalThis.fetch = vi.fn(async () => { throw new Error('connection refused'); });
    render(<LoginPage onLogin={() => {}}/>);
    await waitFor(() => expect(screen.getByText(/Cannot reach service|Service unavailable/)).toBeTruthy());
  });
});

describe('Login page security messaging', () => {
  // The server uses ListenAndServe; the login form must not promise encryption unconditionally.
  it('Warns about unencrypted HTTP and private-LAN usage', async () => {
    globalThis.fetch = vi.fn(async () => ({ ok: true, status: 200 }));
    render(<LoginPage onLogin={() => {}}/>);
    expect(screen.queryByText(/TLS/)).toBeNull();
    expect(screen.queryByText(/ENCRYPTED/i)).toBeNull();
    expect(screen.getByText(/UNENCRYPTED\s*HTTP/)).toBeTruthy();
  });

  it('Does not display nonfunctional password reset or remember-device links', async () => {
    globalThis.fetch = vi.fn(async () => ({ ok: true, status: 200 }));
    render(<LoginPage onLogin={() => {}}/>);
    expect(screen.queryByText(/Forgot password/)).toBeNull();
    expect(screen.queryByText(/Remember device/)).toBeNull();
  });
});
