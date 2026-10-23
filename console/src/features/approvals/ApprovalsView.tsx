// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useEffect, useState } from 'react';
import { Empty, StatusPill, dateTime, relativeTime, shortId } from '../../components/ui';
import { api } from '../../lib/api';
import { Paths } from '../../lib/paths';
import {
  isPlatformEnrollmentL3Suspension,
  type PlatformEnrollmentDecisionResponse,
  type PlatformEnrollmentRequest,
} from '../../lib/types';
import { approveTransaction } from '../../lib/webauthn';
import { useApprovals } from '../../state/approvals';
import { errorText, useToast } from '../../state/toast';
import { RecoveryCard } from './RecoveryCard';

// Deep-linked approvals prompt for a passkey at most once per page load.
const autoApproved = new Set<string>();

interface Props {
  /** Transaction hash from #approve=…; its ceremony starts automatically once. */
  autoApproveTxHash?: string;
  /** Request ID from #platform-enrollment=…; that request is pinned to the top. */
  focusEnrollmentId?: string;
  recoveryToken?: string;
}

export function ApprovalsView({ autoApproveTxHash, focusEnrollmentId, recoveryToken }: Props) {
  const { transactions, enrollments, canReviewEnrollments, reload } = useApprovals();
  const toast = useToast();
  // pending is the one decision whose passkey ceremony or request is in
  // progress; a browser runs one WebAuthn ceremony at a time. executing holds
  // signed approvals still waiting for their execution, which may suspend
  // another transaction (such as a Tribunal command's review inference) that
  // must stay approvable meanwhile.
  const [pending, setPending] = useState<string | null>(null);
  const [executing, setExecuting] = useState<ReadonlySet<string>>(new Set());
  const [denying, setDenying] = useState(false);

  const approve = async (txHash: string) => {
    setPending(txHash);
    try {
      const out = await approveTransaction(txHash, () => {
        setExecuting((current) => new Set(current).add(txHash));
        setPending(null);
      });
      toast(out.ok ? 'success' : 'error', out.message);
    } catch (err) {
      toast('error', errorText(err));
    } finally {
      setPending((current) => (current === txHash ? null : current));
      setExecuting((current) => {
        const next = new Set(current);
        next.delete(txHash);
        return next;
      });
      await reload();
    }
  };

  // A deny is the Operator-signed L3_DENIED rejection; it needs no passkey
  // because it can only stop the transaction.
  const deny = async (txHash: string) => {
    setPending(txHash);
    setDenying(true);
    try {
      await api.post(Paths.approvalsRespond, { approval_id: txHash, approved: false });
      toast('success', 'Transaction denied. The executing runtime signed the rejection.');
    } catch (err) {
      toast('error', errorText(err));
    } finally {
      setPending(null);
      setDenying(false);
      await reload();
    }
  };

  useEffect(() => {
    if (!autoApproveTxHash || autoApproved.has(autoApproveTxHash)) return;
    autoApproved.add(autoApproveTxHash);
    void approve(autoApproveTxHash);
  }, [autoApproveTxHash]);

  const decide = async (req: PlatformEnrollmentRequest, decision: 'approve' | 'deny') => {
    setPending(req.request_id);
    try {
      const result = await api.post<PlatformEnrollmentDecisionResponse>(Paths.platformEnrollmentDecision, {
        request_id: req.request_id,
        decision,
      });
      if (isPlatformEnrollmentL3Suspension(result)) {
        const out = await approveTransaction(result.tx_hash);
        if (!out.ok) {
          toast('error', out.message);
          return;
        }
      }
      toast('success', decision === 'approve' ? 'Enrollment approved. The workload can now complete enrollment.' : 'Enrollment denied.');
    } catch (err) {
      toast('error', errorText(err));
    } finally {
      setPending(null);
      await reload();
    }
  };

  const ordered = focusEnrollmentId
    ? [...enrollments].sort((a, b) => Number(b.request_id === focusEnrollmentId) - Number(a.request_id === focusEnrollmentId))
    : enrollments;
  const focusMissing = !!focusEnrollmentId && !enrollments.some((e) => e.request_id === focusEnrollmentId);

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Approvals</h1>
          <p>Decisions the Gateway is holding for you. Every approval is signed with your passkey.</p>
        </div>
        <button type="button" className="btn btn-ghost" onClick={() => void reload()}>
          Refresh
        </button>
      </div>

      {recoveryToken && <RecoveryCard token={recoveryToken} />}

      <div className="card">
        <div className="card-head">
          <h2>Suspended transactions</h2>
          <span className="muted">L3 Notary</span>
        </div>
        {transactions.length === 0 ? (
          <Empty title="Nothing waiting">Governed actions that require your authorization appear here.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Action</th>
                <th>Transaction</th>
                <th>Requested</th>
                <th>Expires</th>
                <th className="actions" aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {transactions.map((tx) => (
                <tr key={tx.transaction_hash}>
                  <td style={{ fontWeight: 500 }}>{tx.tool_name || 'Governed action'}</td>
                  <td className="mono muted" title={tx.transaction_hash}>
                    {shortId(tx.transaction_hash, 24)}
                  </td>
                  <td className="text-2">{relativeTime(tx.created_at)}</td>
                  <td className="text-2">{dateTime(tx.expires_at)}</td>
                  <td className="actions">
                    <button
                      type="button"
                      className="btn btn-sm btn-primary"
                      disabled={pending !== null || executing.has(tx.transaction_hash)}
                      onClick={() => void approve(tx.transaction_hash)}
                    >
                      {executing.has(tx.transaction_hash)
                        ? 'Executing…'
                        : pending === tx.transaction_hash && !denying
                          ? 'Waiting for passkey…'
                          : 'Approve'}
                    </button>
                    <button
                      type="button"
                      className="btn btn-sm btn-danger"
                      disabled={pending !== null || executing.has(tx.transaction_hash)}
                      onClick={() => void deny(tx.transaction_hash)}
                    >
                      {pending === tx.transaction_hash && denying ? 'Denying…' : 'Deny'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {canReviewEnrollments && (
        <div className="card">
          <div className="card-head">
            <h2>Platform workload enrollment</h2>
            <span className="muted">Owner only</span>
          </div>
          {focusMissing && (
            <div className="notice" style={{ marginBottom: 12 }}>
              Request <span className="mono">{shortId(focusEnrollmentId, 24)}</span> is no longer pending. It may have been
              decided, expired, or completed.
            </div>
          )}
          {ordered.length === 0 ? (
            <Empty title="No pending requests">Workloads and Operators requesting an identity appear here.</Empty>
          ) : (
            ordered.map((req) => (
              <EnrollmentRequest
                key={req.request_id}
                req={req}
                focused={req.request_id === focusEnrollmentId}
                busy={pending !== null}
                onDecide={(d) => void decide(req, d)}
              />
            ))
          )}
        </div>
      )}
    </div>
  );
}

function EnrollmentRequest({
  req,
  focused,
  busy,
  onDecide,
}: {
  req: PlatformEnrollmentRequest;
  focused: boolean;
  busy: boolean;
  onDecide: (d: 'approve' | 'deny') => void;
}) {
  const fps = req.fingerprints ?? {};
  return (
    <div className={`card ${focused ? 'card-attention' : ''}`} style={{ marginTop: 12 }}>
      <div className="card-head" style={{ marginBottom: 10 }}>
        <h2>
          {req.component_kind || 'workload'} · {req.component_name || req.hostname || ''}
        </h2>
        <StatusPill status={req.state} />
      </div>
      <dl className="kv">
        <dt>Request</dt>
        <dd className="mono">{req.request_id}</dd>
        <dt>Instance</dt>
        <dd className="mono">{req.instance_id}</dd>
        <dt>Hostname</dt>
        <dd className="mono">{req.hostname}</dd>
        {req.system_fingerprint && (
          <>
            <dt>System fingerprint</dt>
            <dd className="mono">{req.system_fingerprint}</dd>
          </>
        )}
        {fps.app && (
          <>
            <dt>App key</dt>
            <dd className="mono">{fps.app}</dd>
          </>
        )}
        {fps.signing_key && (
          <>
            <dt>L2 signing key</dt>
            <dd className="mono">{fps.signing_key}</dd>
          </>
        )}
        {fps.operator && (
          <>
            <dt>Operator key</dt>
            <dd className="mono">{fps.operator}</dd>
          </>
        )}
        {fps.cli && (
          <>
            <dt>CLI key</dt>
            <dd className="mono">{fps.cli}</dd>
          </>
        )}
        <dt>Expires</dt>
        <dd>{dateTime(req.expires_at)}</dd>
      </dl>
      {req.state === 'pending' && (
        <>
          <p className="muted" style={{ margin: '12px 0' }}>
            Compare the key fingerprints with the output on the requesting host. Approve only a workload you started.
          </p>
          <div className="row">
            <button type="button" className="btn btn-primary" disabled={busy} onClick={() => onDecide('approve')}>
              Approve
            </button>
            <button type="button" className="btn btn-danger" disabled={busy} onClick={() => onDecide('deny')}>
              Deny
            </button>
          </div>
        </>
      )}
    </div>
  );
}
