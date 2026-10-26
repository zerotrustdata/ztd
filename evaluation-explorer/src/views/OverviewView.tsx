// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Overview view — the live event stream. Runs live in the shared drawer.

import { useStoreState } from '../state/store';
import { LiveEventStream } from '../components/LiveEventStream';

export function OverviewView() {
  const events = useStoreState((state) => state.events);
  const connection = useStoreState((state) => state.connection);
  const streamConnection = useStoreState((state) => state.streamConnection);
  const isReconciling = useStoreState((state) => state.pendingSnapshot !== null);

  return (
    <div className="overview">
      <LiveEventStream
        events={events}
        connection={connection}
        streamConnection={streamConnection}
        isReconciling={isReconciling}
      />
    </div>
  );
}
