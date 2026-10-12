// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, it, expect } from 'vitest';
import { applyOperatorHeartbeat, nextStaleAt, operatorStatusAt, parseOperatorHeartbeat } from './liveness';
import type { Operator } from './types';

const T0 = Date.parse('2026-01-15T12:00:00Z');

function op(id: string, fields: Partial<Operator> = {}): Operator {
  return { id, user_id: 'u1', status: 'active', created_at: '2026-01-15T11:00:00Z', updated_at: '2026-01-15T11:00:00Z', ...fields };
}

describe('operatorStatusAt', () => {
  it('reads an Operator past its stale_at as stale and before it as active', () => {
    const o = op('a', { status: 'active', stale_at: '2026-01-15T12:01:00Z' });
    expect(operatorStatusAt(o, T0)).toBe('active');
    expect(operatorStatusAt(o, T0 + 60_000)).toBe('active');
    expect(operatorStatusAt(o, T0 + 60_001)).toBe('stale');
  });

  it('reads a stale Operator whose stale_at moved ahead as active again', () => {
    const o = op('a', { status: 'stale', stale_at: '2026-01-15T12:01:00Z' });
    expect(operatorStatusAt(o, T0)).toBe('active');
  });

  it('reads an Operator without stale_at as its stored status', () => {
    expect(operatorStatusAt(op('a', { status: 'offline' }), T0)).toBe('offline');
    expect(operatorStatusAt(op('b', { status: 'bound' }), T0)).toBe('bound');
  });
});

describe('parseOperatorHeartbeat', () => {
  it('reads the heartbeat payload', () => {
    expect(
      parseOperatorHeartbeat({ operator_id: 'a', last_heartbeat_at: '2026-01-15T12:00:00Z', stale_at: '2026-01-15T12:01:00Z' }),
    ).toEqual({ operator_id: 'a', last_heartbeat_at: '2026-01-15T12:00:00Z', stale_at: '2026-01-15T12:01:00Z' });
    expect(parseOperatorHeartbeat({ operator_id: 'a', last_heartbeat_at: '2026-01-15T12:00:00Z' })).toEqual({
      operator_id: 'a',
      last_heartbeat_at: '2026-01-15T12:00:00Z',
      stale_at: undefined,
    });
  });

  it('rejects data that is not a heartbeat', () => {
    expect(parseOperatorHeartbeat({ operator_id: 'a' })).toBeNull();
    expect(parseOperatorHeartbeat({ last_heartbeat_at: '2026-01-15T12:00:00Z' })).toBeNull();
  });
});

describe('applyOperatorHeartbeat', () => {
  it('updates only the heartbeat Operator, clearing stale_at when the heartbeat has none', () => {
    const ops = [op('a', { stale_at: '2026-01-15T12:01:00Z' }), op('b', { stale_at: '2026-01-15T12:02:00Z' })];
    const next = applyOperatorHeartbeat(ops, { operator_id: 'a', last_heartbeat_at: '2026-01-15T12:05:00Z' });
    expect(next[0]).toMatchObject({ id: 'a', last_heartbeat_at: '2026-01-15T12:05:00Z', stale_at: undefined });
    expect(next[1]).toBe(ops[1]);
  });
});

describe('nextStaleAt', () => {
  it('returns the earliest stale_at not yet passed', () => {
    const ops = [
      op('passed', { stale_at: '2026-01-15T11:59:00Z' }),
      op('later', { stale_at: '2026-01-15T12:03:00Z' }),
      op('sooner', { stale_at: '2026-01-15T12:01:00Z' }),
      op('none', { status: 'offline' }),
    ];
    expect(nextStaleAt(ops, T0)).toBe(Date.parse('2026-01-15T12:01:00Z'));
  });

  it('returns null when no Operator can turn stale', () => {
    expect(nextStaleAt([op('a', { stale_at: '2026-01-15T11:59:00Z' }), op('b', { status: 'offline' })], T0)).toBeNull();
  });
});
