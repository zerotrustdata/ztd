// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { LIVE_EVENT_RETENTION_LIMIT } from '../constants';
import type { AssignmentResult, LiveEvent, ProjectionRecord } from '../contract/types';
import { PanelSection } from './evidence';
import { CopyButton } from './shared';

function JSONDetails({ title, text, filename, open = false }: { title: string; text: string; filename: string; open?: boolean }) {
  return (
    <details className="assignment-json" open={open}>
      <summary>{title}</summary>
      <div className="assignment-json-actions">
        <CopyButton text={text} label={title} />
        <a href={`data:application/json;charset=utf-8,${encodeURIComponent(text)}`} download={filename}>
          Download {title === 'Assignment JSON' ? 'assignment JSON' : title === 'Feed events JSON' ? 'feed events JSON' : 'source JSON'}
        </a>
      </div>
      <pre className="model-response-text"><code>{text}</code></pre>
    </details>
  );
}

/** Every ingested public field stays inspectable, including fields that do not
 *  have a dedicated summary tile. The source envelope precedes normalization;
 *  the assignment JSON contains exactly the view data used by this page. */
export function AssignmentEvidenceData({ assignment, events, source }: { assignment: AssignmentResult; events: LiveEvent[]; source?: ProjectionRecord }) {
  return (
    <>
      <PanelSection className="assignment-events" title="Assignment feed events">
        <p className="panel-note">
          {events.length} events loaded for this assignment from retained public feed history. Each row exposes
          every event field. This history is independent of the live feed’s {LIVE_EVENT_RETENTION_LIMIT}-event display limit.
        </p>
        {events.length > 0 ? <>
          <ol className="assignment-event-list">
            {events.map((event) => (
              <li key={event.event_id}>
                <details>
                  <summary><time dateTime={event.observed_at}>{event.observed_at}</time>{' '}<strong>{event.kind}</strong>{event.stage_label ? ` · ${event.stage_label}` : ''}</summary>
                  <pre className="model-response-text"><code>{JSON.stringify(event, null, 2)}</code></pre>
                </details>
              </li>
            ))}
          </ol>
          <JSONDetails title="Feed events JSON" text={JSON.stringify(events, null, 2)} filename={`${assignment.assignment_id}-events.json`} />
        </> : <p className="model-response-empty">No assignment feed events are available in the loaded public history. This is not evidence that no execution events occurred.</p>}
      </PanelSection>
      <PanelSection className="assignment-published-data" title="Published assignment data">
        <p className="panel-note">Inspect and download the assignment data used by this page. Where available, the public source record includes the original projection envelope, digests, and fields before display normalization.</p>
        <JSONDetails title="Assignment JSON" text={JSON.stringify(assignment, null, 2)} filename={`${assignment.assignment_id}.json`} open />
        {source ? <JSONDetails title="Public source record" text={source.record_bytes} filename={`${assignment.assignment_id}-source.json`} /> : <p className="panel-note">The original source envelope is unavailable for this record.</p>}
      </PanelSection>
    </>
  );
}
