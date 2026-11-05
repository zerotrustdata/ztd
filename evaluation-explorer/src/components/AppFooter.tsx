// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { Link } from 'react-router-dom';
import { useFeedStatus, useStoreState } from '../state/store';
import { useMirrorOrigin } from '../state/mirror';
import { ZTD_REPO_URL } from '../content/platform';
import descriptorUrl from '../contract/descriptor.json?url';
import { formatNumber, formatTimestamp } from '../utils/format';

/** Time of day for today's data; a date as well once it is older, so a stale feed cannot read as current. */
function acceptedLabel(iso: string, now: Date = new Date()): string {
  const date = new Date(iso);
  const time = date.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
  if (date.toDateString() === now.toDateString()) return time;
  return `${date.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })} ${time}`;
}

/** Single-row footer: feed position, mirror transport endpoints, schema, and site links. */
export function AppFooter() {
  const origin = useMirrorOrigin();
  const feedStatus = useFeedStatus();
  const proofsPublished = useStoreState((state) => state.proofArtifactCount > 0);
  const external = { target: '_blank', rel: 'noopener noreferrer' } as const;

  return (
    <footer className="app-footer" aria-label="Public mirror status, endpoints, and site links">
      <span className="app-footer-label" title="Anonymous, read-only. Mirror availability is not verification evidence.">
        Public mirror · read-only
      </span>
      {feedStatus ? (
        <span className="app-footer-telemetry" data-testid="footer-telemetry">
          Seq {formatNumber(feedStatus.highWaterSequence)}
          {feedStatus.lastAcceptedAt ? (
            <>
              {' · Accepted '}
              <time dateTime={feedStatus.lastAcceptedAt} title={formatTimestamp(feedStatus.lastAcceptedAt)}>
                {acceptedLabel(feedStatus.lastAcceptedAt)}
              </time>
            </>
          ) : null}
        </span>
      ) : null}
      <nav className="app-footer-links" aria-label="Mirror transport">
        {origin ? (
          <>
            <a href={`${origin}/pointer.json`} {...external}>Pointer <code>JSON</code></a>
            {proofsPublished ? (
              <a href={`${origin}/proof-head.json`} {...external}>Proof head <code>JSON</code></a>
            ) : null}
            <a href={`${origin}/live/status.json`} {...external}>Live status <code>JSON</code></a>
          </>
        ) : (
          <span className="app-footer-unavailable">Mirror origin unavailable</span>
        )}
        <a href={descriptorUrl} {...external}>Schema <code>v1.5</code></a>
      </nav>
      <nav className="app-footer-links app-footer-site" aria-label="Site">
        <Link to="/methodology">Methodology</Link>
        <Link to="/tasks">Tasks</Link>
        <Link to="/about">About</Link>
        <Link to="/sponsors">Sponsors</Link>
        <a href={ZTD_REPO_URL} {...external}>Source</a>
      </nav>
    </footer>
  );
}
