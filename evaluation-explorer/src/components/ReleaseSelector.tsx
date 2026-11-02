// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';
import { compareReleasesDescending, CURRENT_PLATFORM_RELEASE, toReleaseSeries } from '../content/release';
import { useSelectedRelease } from '../state/dataset';
import { useStoreState } from '../state/store';

export function ReleaseSelector({
  defaultRelease,
  seriesOnly,
}: {
  defaultRelease?: string;
  seriesOnly?: boolean;
}) {
  const [params, setParams] = useSearchParams();
  const rawSelected = useSelectedRelease(defaultRelease);
  const selected = seriesOnly ? toReleaseSeries(rawSelected) : rawSelected;
  const rawReleases = useStoreState((state) => Array.from(new Set([
    CURRENT_PLATFORM_RELEASE,
    ...(rawSelected === 'all' ? [] : [rawSelected]),
    ...Array.from(state.evaluations.values()).flatMap((record) => record.release ? [record.release] : []),
    ...Array.from(state.catalogs.values()).flatMap((record) => record.release ? [record.release] : []),
    ...Array.from(state.models.values()).flatMap((record) => record.release ? [record.release] : []),
  ])));

  const releases = useMemo(() => {
    if (!seriesOnly) {
      return [...rawReleases].sort(compareReleasesDescending);
    }
    const seriesSet = new Set(rawReleases.map(toReleaseSeries));
    if (selected !== 'all') {
      seriesSet.add(selected);
    }
    return Array.from(seriesSet).sort(compareReleasesDescending);
  }, [rawReleases, seriesOnly, selected]);

  return (
    <select aria-label="Filter by release" value={selected} onChange={(event) => {
      const next = new URLSearchParams(params);
      next.set('release', event.target.value);
      next.delete('dataset');
      setParams(next);
    }}>
      <option value="all">All releases</option>
      {releases.map((release) => (
        <option key={release} value={release}>
          {release}{!seriesOnly && release === CURRENT_PLATFORM_RELEASE ? ' (current)' : ''}
        </option>
      ))}
    </select>
  );
}
