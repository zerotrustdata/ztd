// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Drives the real App against a fake Gateway: routes are matched by method and
// path, and every request is recorded for assertions.

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { App } from '../src/App';
import { SessionProvider } from '../src/state/session';
import { ToastProvider } from '../src/state/toast';

type Handler = (body: unknown, url: URL) => [number, unknown];
interface Call {
  method: string;
  path: string;
  search: string;
  body: unknown;
  credentials?: RequestCredentials;
}

let routes: Record<string, Handler>;
let calls: Call[];

class FakeEventSource {
  static last: FakeEventSource | null = null;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((m: MessageEvent<string>) => void) | null = null;
  constructor(
    public url: string,
    public init?: EventSourceInit,
  ) {
    FakeEventSource.last = this;
  }
  close() {}
  push(id: number, type: string, data: Record<string, unknown>) {
    this.onmessage?.({ data: JSON.stringify({ user_id: 'u1', web_session_id: 'ws1', event: { type, data } }), lastEventId: String(id) } as MessageEvent<string>);
  }
}

function signedInRoutes(extra: Record<string, Handler> = {}): Record<string, Handler> {
  return {
    'GET /api/v1/auth/bootstrap/status': () => [200, { bootstrapped: true }],
    'GET /api/v1/health': () => [200, { version: 'v1.0.0' }],
    'GET /api/v1/users/me': () => [200, { success: true, user: { id: 'u1' } }],
    'GET /api/v1/auth/sessions/me': () => [200, { success: true, user_id: 'u1', web_session_id: 'ws1' }],
    'GET /api/v1/operators': () => [200, { success: true, operators: [] }],
    'GET /api/v1/approvals': () => [200, { transactions: [] }],
    'GET /api/v1/auth/platform-enrollments/pending': () => [200, { requests: [] }],
    ...extra,
  };
}

beforeEach(() => {
  calls = [];
  window.history.replaceState(null, '', '/console/');
  vi.stubGlobal('EventSource', FakeEventSource);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, 'https://gw.test');
      const method = init.method ?? 'GET';
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, path: url.pathname, search: url.search, body, credentials: init.credentials });
      const handler = routes[`${method} ${url.pathname}`];
      const [status, payload] = handler ? handler(body, url) : [404, { error: 'not found' }];
      return new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderApp(intent = {}) {
  return render(
    <ToastProvider>
      <SessionProvider>
        <App intent={intent} />
      </SessionProvider>
    </ToastProvider>,
  );
}

describe('authentication', () => {
  it('offers first-owner enrollment when the Gateway has no owner', async () => {
    routes = {
      'GET /api/v1/auth/bootstrap/status': () => [200, { bootstrapped: false }],
      'GET /api/v1/health': () => [200, { version: 'v1.0.0' }],
      'GET /api/v1/users/me': () => [401, { error: 'unauthorized' }],
    };
    renderApp();
    expect(await screen.findByRole('heading', { name: 'Set up this Gateway' })).toBeInTheDocument();
  });

  it('asks returning users for their user ID and explains deep-link reasons', async () => {
    routes = {
      'GET /api/v1/auth/bootstrap/status': () => [200, { bootstrapped: true }],
      'GET /api/v1/health': () => [200, {}],
      'GET /api/v1/users/me': () => [401, { error: 'unauthorized' }],
    };
    renderApp({ approveTxHash: 'tx1' });
    expect(await screen.findByLabelText('User ID')).toBeInTheDocument();
    expect(screen.getByText('Sign in to approve a suspended transaction.')).toBeInTheDocument();
  });

  it('sends credentials on every Gateway request', async () => {
    routes = signedInRoutes();
    renderApp();
    await screen.findByRole('heading', { name: 'Operators' });
    await waitFor(() => expect(calls.some((c) => c.path === '/api/v1/operators')).toBe(true));
    expect(calls.every((c) => c.credentials === 'include')).toBe(true);
    expect(FakeEventSource.last?.init?.withCredentials).toBe(true);
    expect(FakeEventSource.last?.url).not.toMatch(/user_id|web_session_id/);
  });
});

describe('operators', () => {
  it('binds an Operator to this session', async () => {
    let bound = false;
    routes = signedInRoutes({
      'GET /api/v1/operators': () => [
        200,
        {
          success: true,
          operators: [
            {
              id: 'op1',
              user_id: 'u1',
              status: 'active',
              operator_type: 'remote',
              operator_session_id: 'os1',
              current_hostname: 'web-01',
              bound_web_session_id: bound ? 'ws1' : undefined,
              created_at: '2026-01-01T00:00:00Z',
              updated_at: '2026-01-01T00:00:00Z',
            },
          ],
        },
      ],
      'POST /api/v1/operators/bind': () => {
        bound = true;
        return [200, { success: true, bound_count: 1, failed_count: 0 }];
      },
    });
    window.history.replaceState(null, '', '/console/?view=operators');
    const user = userEvent.setup();
    renderApp();

    await user.click(await screen.findByRole('button', { name: 'Bind' }));
    expect(calls.find((c) => c.path === '/api/v1/operators/bind')!.body).toEqual({ operator_ids: ['op1'] });
    expect(await screen.findByText('This session')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Unbind' })).toBeInTheDocument();
  });
});

describe('session', () => {
  it('signs out when the Gateway rejects the session cookie', async () => {
    routes = signedInRoutes({
      'GET /api/v1/operators': () => [401, { error: 'unauthorized' }],
    });
    renderApp();

    expect(await screen.findByLabelText('User ID')).toBeInTheDocument();
  });
});

describe('API reference', () => {
  const SPEC = {
    swagger: '2.0',
    info: {},
    paths: {
      '/api/v1/operators': {
        get: {
          summary: 'List operators',
          description: 'Lists bound Operators.',
          tags: ['operators'],
          parameters: [{ name: 'status', in: 'query', type: 'string', description: 'Filter by status' }],
          responses: { '200': { description: 'OK', schema: { $ref: '#/definitions/models.Operator' } } },
        },
      },
      '/api/v1/health': { get: { summary: 'Health check', tags: ['health'], responses: { '200': { description: 'OK' } } } },
    },
    definitions: { 'models.Operator': { type: 'object', required: ['id'], properties: { id: { type: 'string', description: 'Operator ID' } } } },
  };

  it('loads the spec with the session cookie and renders operations grouped by tag', async () => {
    window.history.replaceState(null, '', '/console/?view=api');
    routes = signedInRoutes({ 'GET /swagger/doc.json': () => [200, SPEC] });
    const user = userEvent.setup();
    renderApp();

    expect(await screen.findByRole('heading', { name: 'API' })).toBeInTheDocument();
    expect(await screen.findByText('/api/v1/operators')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'health' })).toBeInTheDocument();
    expect(calls.find((c) => c.path === '/swagger/doc.json')?.credentials).toBe('include');

    await user.click(screen.getByRole('button', { name: /List operators/ }));
    expect(screen.getByText('Filter by status')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /models\.Operator/ }));
    expect(screen.getByText('Operator ID')).toBeInTheDocument();
  });

  it('filters by text and by tag', async () => {
    window.history.replaceState(null, '', '/console/?view=api');
    routes = signedInRoutes({ 'GET /swagger/doc.json': () => [200, SPEC] });
    const user = userEvent.setup();
    renderApp();

    await screen.findByText('/api/v1/operators');
    await user.type(screen.getByLabelText('Filter operations'), 'health');
    expect(screen.queryByText('/api/v1/operators')).toBeNull();
    expect(screen.getByText('/api/v1/health')).toBeInTheDocument();

    await user.clear(screen.getByLabelText('Filter operations'));
    await user.click(within(screen.getByRole('group', { name: 'Filter by tag' })).getByRole('button', { name: /operators/ }));
    expect(screen.getByText('/api/v1/operators')).toBeInTheDocument();
    expect(screen.queryByText('/api/v1/health')).toBeNull();

    await user.type(screen.getByLabelText('Filter operations'), 'nothing-matches');
    expect(screen.getByText('No operations match')).toBeInTheDocument();
  });

  it('reports a load failure instead of an empty reference', async () => {
    window.history.replaceState(null, '', '/console/?view=api');
    routes = signedInRoutes({ 'GET /swagger/doc.json': () => [403, { error: 'forbidden' }] });
    renderApp();

    expect(await screen.findByText('Could not load the API spec')).toBeInTheDocument();
    expect(screen.getByText('forbidden')).toBeInTheDocument();
  });
});
