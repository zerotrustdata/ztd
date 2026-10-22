// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { isPlatformEnrollmentL3Suspension, type PlatformEnrollmentRequest } from '../../lib/types';
import * as webauthn from '../../lib/webauthn';
import { ApprovalsProvider } from '../../state/approvals';
import { StreamProvider } from '../../state/stream';
import { ToastProvider } from '../../state/toast';
import { ApprovalsView } from './ApprovalsView';

type Handler = (body: unknown, url: URL) => [number, unknown];

interface RecordedCall {
  method: string;
  path: string;
  body: unknown;
}

const bytes = (...b: number[]) => new Uint8Array(b).buffer;

function fakeAssertion() {
  return {
    id: 'cred-1',
    rawId: bytes(1),
    response: {
      clientDataJSON: bytes(2),
      authenticatorData: bytes(3),
      signature: bytes(4),
      userHandle: null,
    },
  } as unknown as PublicKeyCredential;
}

const PENDING_REQ: PlatformEnrollmentRequest = {
  request_id: 'req-1',
  component_kind: 'application',
  component_name: 'claude',
  instance_id: 'inst-1',
  hostname: 'dev-host',
  state: 'pending',
  expires_at: '2026-10-17T20:00:00Z',
  fingerprints: { app: 'SHA256:app-fp' },
};

describe('isPlatformEnrollmentL3Suspension', () => {
  it('identifies valid L3 suspension responses and rejects enrollment records', () => {
    expect(
      isPlatformEnrollmentL3Suspension({
        status: 'l3_required',
        posture: 'ratify',
        tx_hash: 'abcd1234',
        approval_url: 'https://gw.test/console/#approve=abcd1234',
        expires_at: '2026-10-17T20:00:00Z',
        message: 'Ratify posture requires human passkey approval for enrollment and policy changes.',
      }),
    ).toBe(true);
    expect(isPlatformEnrollmentL3Suspension(PENDING_REQ)).toBe(false);
    expect(isPlatformEnrollmentL3Suspension({ status: 'l3_required', tx_hash: '' })).toBe(false);
    expect(isPlatformEnrollmentL3Suspension(null)).toBe(false);
  });
});

describe('ApprovalsView platform enrollment decisions', () => {
  let routes: Record<string, Handler>;
  let calls: RecordedCall[];

  beforeEach(() => {
    calls = [];
    routes = {};
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string, init: RequestInit = {}) => {
        const url = new URL(input, 'https://gw.test');
        const method = init.method ?? 'GET';
        const body = init.body ? JSON.parse(String(init.body)) : undefined;
        calls.push({ method, path: url.pathname, body });
        const handler = routes[`${method} ${url.pathname}`];
        const [status, payload] = handler ? handler(body, url) : [404, { error: 'not found' }];
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

  function renderApprovalsView() {
    return render(
      <ToastProvider>
        <StreamProvider enabled={false}>
          <ApprovalsProvider>
            <ApprovalsView />
          </ApprovalsProvider>
        </StreamProvider>
      </ToastProvider>,
    );
  }

  it('completes immediately on 200 OK without invoking approveTransaction', async () => {
    let decided = false;
    const approveSpy = vi.spyOn(webauthn, 'approveTransaction');

    routes = {
      'GET /api/v1/approvals': () => [200, { transactions: [] }],
      'GET /api/v1/auth/platform-enrollments/pending': () => [
        200,
        { requests: decided ? [] : [PENDING_REQ] },
      ],
      'POST /api/v1/auth/platform-enrollments/decision': () => {
        decided = true;
        return [200, { ...PENDING_REQ, state: 'approved' }];
      },
    };

    const user = userEvent.setup();
    renderApprovalsView();

    await user.click(await screen.findByRole('button', { name: 'Approve' }));

    expect(
      await screen.findByText('Enrollment approved. The workload can now complete enrollment.'),
    ).toBeInTheDocument();
    expect(approveSpy).not.toHaveBeenCalled();
    expect(calls.find((c) => c.path === '/api/v1/auth/platform-enrollments/decision')?.body).toEqual({
      request_id: 'req-1',
      decision: 'approve',
    });
    await waitFor(() => expect(screen.queryByText('req-1')).toBeNull());
  });

  it('runs the WebAuthn passkey ceremony when decision returns 202 l3_required and completes on verify', async () => {
    let verified = false;
    const approveSpy = vi.spyOn(webauthn, 'approveTransaction');
    const getCredMock = vi.fn().mockResolvedValue(fakeAssertion());
    vi.stubGlobal('navigator', { credentials: { get: getCredMock } });

    routes = {
      'GET /api/v1/approvals': () => [200, { transactions: [] }],
      'GET /api/v1/auth/platform-enrollments/pending': () => [
        200,
        { requests: verified ? [] : [PENDING_REQ] },
      ],
      'POST /api/v1/auth/platform-enrollments/decision': () => [
        202,
        {
          status: 'l3_required',
          posture: 'ratify',
          tx_hash: 'tx-decide-1',
          approval_url: 'https://gw.test/console/approve/tx-decide-1',
          expires_at: '2026-10-17T20:00:00Z',
          message: 'Ratify posture requires human passkey approval for enrollment and policy changes.',
        },
      ],
      'GET /api/v1/approvals/tx-decide-1/challenge': () => [
        200,
        { publicKey: { challenge: 'AQ', allowCredentials: [] } },
      ],
      'POST /api/v1/approvals/tx-decide-1/verify': () => {
        verified = true;
        return [200, { event_receipt: {} }];
      },
    };

    const user = userEvent.setup();
    renderApprovalsView();

    await user.click(await screen.findByRole('button', { name: 'Approve' }));

    expect(
      await screen.findByText('Enrollment approved. The workload can now complete enrollment.'),
    ).toBeInTheDocument();
    expect(approveSpy).toHaveBeenCalledWith('tx-decide-1');
    expect(getCredMock).toHaveBeenCalledTimes(1);
    expect(calls.some((c) => c.path === '/api/v1/approvals/tx-decide-1/challenge')).toBe(true);
    expect(calls.some((c) => c.path === '/api/v1/approvals/tx-decide-1/verify')).toBe(true);
    await waitFor(() => expect(screen.queryByText('req-1')).toBeNull());
  });

  it('surfaces the error and keeps the enrollment pending when approveTransaction fails or is cancelled', async () => {
    const getCredMock = vi.fn().mockResolvedValue(null);
    vi.stubGlobal('navigator', { credentials: { get: getCredMock } });

    routes = {
      'GET /api/v1/approvals': () => [200, { transactions: [] }],
      'GET /api/v1/auth/platform-enrollments/pending': () => [200, { requests: [PENDING_REQ] }],
      'POST /api/v1/auth/platform-enrollments/decision': () => [
        202,
        {
          status: 'l3_required',
          posture: 'notary',
          tx_hash: 'tx-decide-cancel',
          approval_url: 'https://gw.test/console/approve/tx-decide-cancel',
          expires_at: '2026-10-17T20:00:00Z',
          message: 'Notary posture requires human passkey approval.',
        },
      ],
      'GET /api/v1/approvals/tx-decide-cancel/challenge': () => [
        200,
        { publicKey: { challenge: 'AQ', allowCredentials: [] } },
      ],
    };

    const user = userEvent.setup();
    renderApprovalsView();

    await user.click(await screen.findByRole('button', { name: 'Approve' }));

    expect(await screen.findByText('Passkey prompt was cancelled.')).toBeInTheDocument();
    expect(
      screen.queryByText('Enrollment approved. The workload can now complete enrollment.'),
    ).toBeNull();
    expect(screen.getByText('req-1')).toBeInTheDocument();
  });

  it('surfaces verification rejection when approveTransaction returns ok: false', async () => {
    const getCredMock = vi.fn().mockResolvedValue(fakeAssertion());
    vi.stubGlobal('navigator', { credentials: { get: getCredMock } });

    routes = {
      'GET /api/v1/approvals': () => [200, { transactions: [] }],
      'GET /api/v1/auth/platform-enrollments/pending': () => [200, { requests: [PENDING_REQ] }],
      'POST /api/v1/auth/platform-enrollments/decision': () => [
        202,
        {
          status: 'l3_required',
          posture: 'ratify',
          tx_hash: 'tx-decide-reject',
          approval_url: 'https://gw.test/console/approve/tx-decide-reject',
          expires_at: '2026-10-17T20:00:00Z',
          message: 'Ratify posture requires human passkey approval.',
        },
      ],
      'GET /api/v1/approvals/tx-decide-reject/challenge': () => [
        200,
        { publicKey: { challenge: 'AQ', allowCredentials: [] } },
      ],
      'POST /api/v1/approvals/tx-decide-reject/verify': () => [
        403,
        { error: 'Passkey signature verification failed.' },
      ],
    };

    const user = userEvent.setup();
    renderApprovalsView();

    await user.click(await screen.findByRole('button', { name: 'Approve' }));

    expect(await screen.findByText('Passkey signature verification failed.')).toBeInTheDocument();
    expect(
      screen.queryByText('Enrollment approved. The workload can now complete enrollment.'),
    ).toBeNull();
    expect(screen.getByText('req-1')).toBeInTheDocument();
  });
});
