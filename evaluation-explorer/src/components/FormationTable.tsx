// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { DataTable, type DataTableColumn } from './DataTable';
import { useStoreState } from '../state/store';
import { MODEL_ROLE_WIRE_ORDER } from '../content/roles';
import { formatParameterCount } from '../content/framework-catalog';
import { roleLabel } from '../views/derived';
import type { FormationGroup, FormationLeaderboardRow } from '../views/formations';
import { formatDuration, formatLatency, formatNumber, formatPercent, formatTokens, UnavailableValue } from './shared';
import { formatCompact } from '../utils/format';
import { OutcomeBreakdown } from './evidence';

export function FormationTable({ formations, leaderboard = false }: { formations: Array<FormationGroup | FormationLeaderboardRow>; leaderboard?: boolean }) {
  const models = useStoreState((state) => Array.from(state.models.values()));
  const columns = useMemo<DataTableColumn<FormationGroup | FormationLeaderboardRow>[]>(() => [
    ...(leaderboard ? [{ id: 'rank', header: 'Rank', accessorFn: (row: FormationGroup | FormationLeaderboardRow) => 'rank' in row ? row.rank : 0 }] : []),
    {
      id: 'formation', header: 'Formation', accessorFn: (row) => row.summary.stack_id,
      cell: ({ row: { original: group } }) => {
        const run = group.sources[0]!.run;
        return <div className="eval-cell-stack" data-testid={`formation-${group.summary.stack_id}`}>
          <Link to={`/evaluations/${run.dataset_id}/${run.run_id}`}>{group.summary.stack_id}</Link>
          <span className="eval-cell-detail">{group.sources.length} runs · {formatNumber(group.summary.pass_rate?.denominator ?? 0)} judged assignments</span>
          <code className="eval-cell-detail" title={`Frozen stack digest: ${group.summary.stack_digest}`}>{group.summary.stack_digest.slice(0, 12)}</code>
          {group.sources.length > 1 ? <details><summary>Contributing runs</summary><ul>
            {group.sources.map(({ run: source }) => <li key={`${source.dataset_id}:${source.run_id}`}>
              <Link to={`/evaluations/${source.dataset_id}/${source.run_id}`}>{source.run_id}</Link>
            </li>)}
          </ul></details> : null}
        </div>;
      },
    },
    ...(!leaderboard ? [{ id: 'suite', header: 'Suite', accessorKey: 'suite_id' as const }] : []),
    ...(!leaderboard ? [{
      id: 'verification', header: 'Verified runs', accessorFn: (group: FormationGroup) => group.sources.filter(({ run }) => run.verifier_state === 'passed').length,
      cell: ({ row }: { row: { original: FormationGroup } }) => `${row.original.sources.filter(({ run }) => run.verifier_state === 'passed').length}/${row.original.sources.length}`,
    }] : []),
    ...MODEL_ROLE_WIRE_ORDER.map((role): DataTableColumn<FormationGroup | FormationLeaderboardRow> => ({
      id: role, header: roleLabel(role), accessorFn: (group) => group.summary.model_role_mapping[role],
      cell: ({ row: { original: group } }) => {
        const variant = group.summary.model_role_mapping[role];
        const source = group.sources.find(({ run }) => models.some((m) => m.dataset_id === run.dataset_id && m.variant_id === variant && m.role === role));
        const model = source && models.find((m) => m.dataset_id === source.run.dataset_id && m.variant_id === variant && m.role === role);
        return model ? <Link to={`/models/${model.dataset_id}/${variant}?role=${role}`}>{model.display_name}</Link> : <span>{variant}</span>;
      },
    })),
    ...(leaderboard ? [{
      id: 'score', header: 'Score', accessorFn: (row: FormationGroup | FormationLeaderboardRow) => 'score' in row ? row.score : 0,
      cell: ({ row }: { row: { original: FormationGroup | FormationLeaderboardRow } }) => 'score' in row.original ? formatNumber(row.original.score, 1) : <UnavailableValue />,
    }] : []),
    {
      id: 'accuracy', header: 'Accuracy', accessorFn: (group) => group.summary.pass_rate?.estimate ?? -1,
      cell: ({ row }) => row.original.summary.pass_rate ? formatPercent(row.original.summary.pass_rate.estimate, 1) : <UnavailableValue />,
    },
    {
      id: 'security', header: 'Security', accessorFn: (group) => group.summary.security_pass_rate?.estimate ?? -1,
      cell: ({ row }) => row.original.summary.security_pass_rate ? formatPercent(row.original.summary.security_pass_rate.estimate) : <UnavailableValue />,
    },
    {
      id: 'outcomes', header: 'Outcomes', enableSorting: false,
      cell: ({ row }) => <OutcomeBreakdown outcomes={row.original.summary.terminal_outcomes} />,
    },
    {
      id: 'latency', header: 'Mean run latency p50', accessorFn: (group) => group.summary.latency_p50_ms.value,
      cell: ({ row }) => row.original.summary.latency_p50_ms.value !== undefined ? formatLatency(row.original.summary.latency_p50_ms.value) : <UnavailableValue />,
    },
    {
      id: 'tokens', header: 'Output tokens', accessorFn: (group) => group.summary.output_tokens.value,
      cell: ({ row }) => row.original.summary.output_tokens.value !== undefined ? formatTokens(row.original.summary.output_tokens.value) : <UnavailableValue />,
    },
    {
      id: 'size', header: 'Parameters', accessorFn: (group) => group.summary.parameter_count,
      cell: ({ row }) => row.original.summary.parameter_count !== undefined ? formatParameterCount(row.original.summary.parameter_count) : <UnavailableValue />,
    },
    {
      id: 'throughput', header: 'Tokens/s', accessorFn: (group) => group.summary.output_throughput_p50.value,
      cell: ({ row }) => row.original.summary.output_throughput_p50.value !== undefined ? formatCompact(row.original.summary.output_throughput_p50.value) : <UnavailableValue />,
    },
    {
      id: 'run_time', header: 'Run time', accessorFn: (group) => group.summary.run_seconds,
      cell: ({ row }) => row.original.summary.run_seconds !== undefined ? formatDuration(row.original.summary.run_seconds) : <UnavailableValue />,
    },
  ], [leaderboard, models]);

  return <DataTable data={formations} columns={columns} pageSize={25} enableColumnVisibility={false}
    caption={leaderboard ? 'Formation leaderboard' : 'Formation catalog'} empty={<p className="table-empty">No formations match these filters.</p>} />;
}
