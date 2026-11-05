// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Poll timers for the public feed. The pointer is polled on a slow cadence while
// the tab is visible. The live status is polled only while the latest pointer says
// a run is active. Every poll runs through one queue, so polls never overlap and a
// live poll never starts before a pointer poll that was queued ahead of it has
// finished applying its read model. Hidden tabs hold no timers.

import { LIVE_STATUS_POLL_LOOPBACK_MS, LIVE_STATUS_POLL_MS, POINTER_POLL_MS } from '../constants';

const LOOPBACK_HOSTS: ReadonlySet<string> = new Set(['127.0.0.1', 'localhost', '[::1]']);

/** Live status cadence for a mirror origin: faster when the mirror runs on this machine. */
export function liveStatusIntervalFor(mirrorOrigin: string): number {
  return LOOPBACK_HOSTS.has(new URL(mirrorOrigin).hostname) ? LIVE_STATUS_POLL_LOOPBACK_MS : LIVE_STATUS_POLL_MS;
}

export interface FeedPollerOptions {
  pollPointer: () => Promise<void>;
  pollLiveStatus: () => Promise<void>;
  liveIntervalMs: number;
}

export interface FeedPoller {
  /** Enables or disables live status polling. Callers enable it only while the pointer says active. */
  setLiveEnabled(enabled: boolean): void;
  /** Clears every timer and the visibility listener. The poller cannot be restarted. */
  stop(): void;
}

function isVisible(): boolean {
  return document.visibilityState === 'visible';
}

/** Starts polling while the tab is visible. The pointer is polled immediately when the tab becomes visible. */
export function startFeedPolling(options: FeedPollerOptions): FeedPoller {
  let stopped = false;
  let liveEnabled = false;
  let pointerTimer: ReturnType<typeof setTimeout> | undefined;
  let liveTimer: ReturnType<typeof setTimeout> | undefined;
  let queue: Promise<void> = Promise.resolve();

  const enqueue = (task: () => Promise<void>): void => {
    queue = queue.then(task).catch(() => undefined);
  };

  const pollPointer = (): void => enqueue(options.pollPointer);

  const pollLive = (): void => {
    enqueue(() => (liveEnabled && isVisible() && !stopped ? options.pollLiveStatus() : Promise.resolve()));
  };

  const schedulePointer = (): void => {
    clearTimeout(pointerTimer);
    pointerTimer = setTimeout(() => {
      pointerTimer = undefined;
      if (stopped || !isVisible()) return;
      pollPointer();
      schedulePointer();
    }, POINTER_POLL_MS);
  };

  const scheduleLive = (): void => {
    if (liveTimer !== undefined) return;
    liveTimer = setTimeout(() => {
      liveTimer = undefined;
      if (stopped || !liveEnabled || !isVisible()) return;
      pollLive();
      scheduleLive();
    }, options.liveIntervalMs);
  };

  const clearTimers = (): void => {
    clearTimeout(pointerTimer);
    clearTimeout(liveTimer);
    pointerTimer = undefined;
    liveTimer = undefined;
  };

  const onVisibilityChange = (): void => {
    if (stopped) return;
    if (!isVisible()) {
      clearTimers();
      return;
    }
    pollPointer();
    schedulePointer();
    if (liveEnabled) scheduleLive();
  };

  document.addEventListener('visibilitychange', onVisibilityChange);
  if (isVisible()) schedulePointer();

  return {
    setLiveEnabled(enabled: boolean): void {
      if (stopped) return;
      liveEnabled = enabled;
      if (!enabled) {
        clearTimeout(liveTimer);
        liveTimer = undefined;
      } else if (isVisible()) {
        scheduleLive();
      }
    },
    stop(): void {
      stopped = true;
      clearTimers();
      document.removeEventListener('visibilitychange', onVisibilityChange);
    },
  };
}
