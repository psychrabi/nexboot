import React from 'react';
import { render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../lib/api.js';
import { StoreProvider } from '../store.jsx';
import { ConfirmProvider } from '../overlay.jsx';
import { PageSettings } from './settings.jsx';

describe('User Management', () => {
  afterEach(() => vi.restoreAllMocks());

  it('Paginates when there are more than ten users', async () => {
    const users = Array.from({ length: 11 }, (_, i) => ({ id: `u${i}`, username: `user${String(i).padStart(2, '0')}` }));
    vi.spyOn(api, 'listUsers').mockImplementation(async () => ({ items: users, total: users.length }));
    render(<StoreProvider><ConfirmProvider><PageSettings/></ConfirmProvider></StoreProvider>);
    expect(await screen.findByText('Total 11 entries')).toBeTruthy();
    expect(screen.getByText('user09')).toBeTruthy();
    expect(screen.queryByText('user10')).toBeNull();
  });
});
