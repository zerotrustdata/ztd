// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Operator inventory and this web session's bindings. The Gateway registry is
// the source of truth: the console re-lists after every mutation and on
// operator status events instead of patching documents locally. Liveness is
// the one exception: each heartbeat event carries the Operator's new
// last_heartbeat_at and stale_at, which are applied in place, and the status
// is derived from stale_at at render time (lib/liveness.ts).

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { api } from '../lib/api';
import { Ev } from '../lib/events';
import { applyOperatorHeartbeat, nextStaleAt, operatorStatusAt, parseOperatorHeartbeat } from '../lib/liveness';
import { Paths } from '../lib/paths';
import type { BindResponse, Operator } from '../lib/types';
import { useSession } from './session';
import { useStreamEvents } from './stream';

interface OperatorsValue {
  operators: Operator[];
  loaded: boolean;
  error: string | null;
  /** Operators bound to the current browser session. */
  bound: Operator[];
  reload: () => Promise<void>;
  bind: (ids: string[]) => Promise<BindResponse>;
  unbind: (ids: string[]) => Promise<BindResponse>;
  stop: (id: string) => Promise<void>;
}

const OperatorsContext = createContext<OperatorsValue | null>(null);

// Slots that were never claimed by a running Operator are not inventory.
function isInventory(op: Operator): boolean {
  return op.status !== 'terminated';
}

export function OperatorsProvider({ children }: { children: ReactNode }) {
  const { webSessionId } = useSession();
  const [listed, setListed] = useState<Operator[]>([]);
  const [now, setNow] = useState(() => Date.now());
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const reloadTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const reload = useCallback(async () => {
    try {
      const res = await api.get<{ operators?: Operator[] }>(Paths.operators);
      setListed((res.operators ?? []).filter(isInventory));
      setNow(Date.now());
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  // Status events carry only the transition; debounce a re-list so a burst of
  // enrollments or a bind-all fan-out costs one request. A heartbeat carries
  // everything liveness needs, so it is applied without a request.
  useStreamEvents((ev) => {
    if (ev.type === Ev.OperatorHeartbeatReceived) {
      const hb = parseOperatorHeartbeat(ev.data);
      if (hb) {
        setListed((ops) => applyOperatorHeartbeat(ops, hb));
        setNow(Date.now());
      }
      return;
    }
    if (!ev.type.startsWith(Ev.OperatorStatusPrefix)) return;
    if (reloadTimer.current) clearTimeout(reloadTimer.current);
    reloadTimer.current = setTimeout(() => void reload(), 400);
  });

  // Re-render once, when the next Operator's stale_at passes, so a silent
  // Operator turns stale with no event and no request.
  useEffect(() => {
    const next = nextStaleAt(listed, now);
    if (next === null) return;
    const timer = setTimeout(() => setNow(Date.now()), next - now + 1);
    return () => clearTimeout(timer);
  }, [listed, now]);

  const operators = useMemo(
    () => listed.map((op) => (op.stale_at ? { ...op, status: operatorStatusAt(op, now) } : op)),
    [listed, now],
  );

  const bind = useCallback(
    async (ids: string[]) => {
      const res = await api.post<BindResponse>(Paths.operatorsBind, { operator_ids: ids });
      await reload();
      return res;
    },
    [reload],
  );

  const unbind = useCallback(
    async (ids: string[]) => {
      const res = await api.post<BindResponse>(Paths.operatorsUnbind, { operator_ids: ids });
      await reload();
      return res;
    },
    [reload],
  );

  const stop = useCallback(
    async (id: string) => {
      await api.post(Paths.operatorStop(id), {});
      await reload();
    },
    [reload],
  );

  const bound = useMemo(
    () => (webSessionId ? operators.filter((op) => op.bound_web_session_id === webSessionId) : []),
    [operators, webSessionId],
  );

  const value = useMemo(
    () => ({ operators, loaded, error, bound, reload, bind, unbind, stop }),
    [operators, loaded, error, bound, reload, bind, unbind, stop],
  );
  return <OperatorsContext.Provider value={value}>{children}</OperatorsContext.Provider>;
}

export function useOperators(): OperatorsValue {
  const v = useContext(OperatorsContext);
  if (!v) throw new Error('useOperators outside OperatorsProvider');
  return v;
}

export function operatorLabel(op: Operator): string {
  return op.current_hostname || op.name || op.id;
}

const UNBINDABLE: ReadonlySet<string> = new Set(['offline', 'stopped', 'terminated', 'unavailable']);

/**
 * Mirrors RegistrationService.BindOperators: the Operator needs a live session.
 * Binding an Operator held by another of the user's sessions moves it here.
 */
export function canBind(op: Operator): boolean {
  return !!op.operator_session_id && !UNBINDABLE.has(op.status);
}
