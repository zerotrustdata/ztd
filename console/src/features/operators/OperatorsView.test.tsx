// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Operator } from '../../lib/types';
import { OperatorsProvider } from '../../state/operators';
import { SessionProvider } from '../../state/session';
import { StreamProvider } from '../../state/stream';
import { ToastProvider } from '../../state/toast';
import { OperatorsView, operatorHostedProviders } from './OperatorsView';

type Handler = (body: unknown, url: URL) => [number, unknown] | Promise<[number, unknown]>;

function makeOperator(fields: Partial<Operator> = {}): Operator {
  return {
    id: 'op-1',
    user_id: 'u1',
    status: 'active',
    operator_type: 'remote',
    operator_capabilities: ['inference'],
    operator_session_id: 'os-1',
    current_hostname: 'host-1',
    created_at: '2026-01-15T10:30:00Z',
    updated_at: '2026-01-15T11:00:00Z',
    ...fields,
  };
}

describe('operatorHostedProviders', () => {
  it('returns non-empty, trimmed, deduplicated provider names', () => {
    expect(
      operatorHostedProviders(
        makeOperator({ runtime_config: { inference_hosted_providers: ['gemini', ' anthropic ', 'gemini', ''] } }),
      ),
    ).toEqual(['gemini', 'anthropic']);
  });

  it('returns an empty list when runtime_config or inference_hosted_providers is absent or empty', () => {
    expect(operatorHostedProviders(makeOperator())).toEqual([]);
    expect(operatorHostedProviders(makeOperator({ runtime_config: {} }))).toEqual([]);
    expect(operatorHostedProviders(makeOperator({ runtime_config: { inference_hosted_providers: [] } }))).toEqual([]);
    expect(operatorHostedProviders(makeOperator({ runtime_config: { inference_hosted_providers: ['  '] } }))).toEqual([]);
  });
});

describe('OperatorsView hosted inference providers', () => {
  let routes: Record<string, Handler>;

  beforeEach(() => {
    routes = {
      'GET /api/v1/auth/bootstrap/status': () => [200, { bootstrapped: true }],
      'GET /api/v1/health': () => [200, { version: 'v1.10.1' }],
      'GET /api/v1/users/me': () => [200, { success: true, user: { id: 'u1' } }],
      'GET /api/v1/auth/sessions/me': () => [200, { success: true, user_id: 'u1', web_session_id: 'ws1' }],
      'GET /api/v1/operators': () => [200, { success: true, operators: [] }],
    };

    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string, init: RequestInit = {}) => {
        const url = new URL(input, 'https://gw.test');
        const method = init.method ?? 'GET';
        const body = init.body ? JSON.parse(String(init.body)) : undefined;
        const handler = routes[`${method} ${url.pathname}`];
        const [status, payload] = handler ? await handler(body, url) : [404, { error: 'not found' }];
        return new Response(JSON.stringify(payload), {
          status,
          headers: { 'Content-Type': 'application/json' },
        });
      }),
    );
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  function renderOperatorsView() {
    return render(
      <ToastProvider>
        <SessionProvider>
          <StreamProvider enabled={false}>
            <OperatorsProvider>
              <OperatorsView />
            </OperatorsProvider>
          </StreamProvider>
        </SessionProvider>
      </ToastProvider>,
    );
  }

  it('shows hosted inference providers when non-empty and omits them when empty or absent', async () => {
    routes['GET /api/v1/operators'] = () => [
      200,
      {
        success: true,
        operators: [
          makeOperator({
            id: 'op-hosted',
            current_hostname: 'hosted-node',
            bound_web_session_id: 'ws1',
            runtime_config: { inference_hosted_providers: ['gemini', 'anthropic', 'openai'] },
          }),
          makeOperator({
            id: 'op-empty',
            current_hostname: 'local-only-node',
            runtime_config: { inference_hosted_providers: [] },
          }),
          makeOperator({
            id: 'op-absent',
            current_hostname: 'legacy-node',
          }),
        ],
      },
    ];

    renderOperatorsView();

    const hostedRow = (await screen.findByText('hosted-node')).closest('tr')!;
    const hostedList = within(hostedRow).getByLabelText('Hosted inference providers');
    expect(within(hostedList).getByText('Hosted:')).toBeInTheDocument();
    expect(within(hostedList).getByText('Gemini')).toBeInTheDocument();
    expect(within(hostedList).getByText('Anthropic')).toBeInTheDocument();
    expect(within(hostedList).getByText('OpenAI')).toBeInTheDocument();

    const emptyRow = screen.getByText('local-only-node').closest('tr')!;
    expect(within(emptyRow).queryByLabelText('Hosted inference providers')).toBeNull();
    expect(within(emptyRow).queryByText('Hosted:')).toBeNull();

    const absentRow = screen.getByText('legacy-node').closest('tr')!;
    expect(within(absentRow).queryByLabelText('Hosted inference providers')).toBeNull();
    expect(within(absentRow).queryByText('Hosted:')).toBeNull();
  });
});
