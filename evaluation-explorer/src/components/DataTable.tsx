// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Accessible sortable table built on TanStack Table. Renders semantic
// <table> markup with proper <th scope>, sort announcements, and a
// screen-reader table alternative is provided by the semantic markup itself.
// Includes column-visibility controls so users can hide columns on wide
// tables (e.g. the 1,125-row assignment table) and keep the view scannable.

import { type ReactNode, useEffect, useId, useState, useSyncExternalStore } from 'react';
import {
  type ColumnDef,
  type ColumnVisibilityState,
  type OnChangeFn,
  type RowData,
  type SortingState,
  columnVisibilityFeature,
  createSortedRowModel,
  rowSortingFeature,
  sortFn_alphanumeric,
  sortFn_text,
  tableFeatures,
  useTable,
} from '@tanstack/react-table';

// Every DataTable registers exactly the features it renders: column
// visibility toggles and client-side sorting. The sort registry holds the
// built-ins that `sortFn: 'auto'` resolves for string columns; numeric
// columns fall back to the basic comparator.
const dataTableFeatures = tableFeatures({
  columnVisibilityFeature,
  rowSortingFeature,
  sortedRowModel: createSortedRowModel(),
  sortFns: { alphanumeric: sortFn_alphanumeric, text: sortFn_text },
});

/** Column definition accepted by DataTable, bound to its feature set. */
export type DataTableColumn<T extends RowData> = ColumnDef<typeof dataTableFeatures, T>;

export interface DataTableProps<T extends RowData> {
  data: T[];
  columns: DataTableColumn<T>[];
  empty?: ReactNode;
  pageSize?: number;
  caption?: string;
  /** When true, renders the column-visibility toggle panel. Defaults to true. */
  enableColumnVisibility?: boolean;
  /** Cards share the table's sorted, paginated row model below 900px. */
  renderCard?: (record: T) => ReactNode;
  /** Filter controls rendered in one row with the column-visibility toggle; stays visible when data is empty. */
  toolbar?: ReactNode;
  /** Optional controlled visibility for table presets. */
  columnVisibility?: ColumnVisibilityState;
  onColumnVisibilityChange?: OnChangeFn<ColumnVisibilityState>;
}

const cardMedia = '(max-width: 899px)';
function subscribeToCardViewport(onChange: () => void) {
  const media = window.matchMedia?.(cardMedia);
  media?.addEventListener('change', onChange);
  return () => media?.removeEventListener('change', onChange);
}
function cardViewport() { return window.matchMedia?.(cardMedia).matches ?? false; }

export function DataTable<T extends RowData>({
  data,
  columns,
  empty,
  pageSize = 50,
  caption,
  enableColumnVisibility = true,
  renderCard,
  toolbar,
  columnVisibility: controlledVisibility,
  onColumnVisibilityChange,
}: DataTableProps<T>) {
  const mobile = useSyncExternalStore(subscribeToCardViewport, cardViewport, () => false);
  const [sorting, setSorting] = useState<SortingState>([]);
  const [page, setPage] = useState(0);
  const [columnVisibility, setColumnVisibility] = useState<ColumnVisibilityState>({});
  const [showColumns, setShowColumns] = useState(false);
  const columnPanelId = useId();

  const table = useTable({
    features: dataTableFeatures,
    data,
    columns,
    state: { sorting, columnVisibility: controlledVisibility ?? columnVisibility },
    onSortingChange: setSorting,
    onColumnVisibilityChange: onColumnVisibilityChange ?? setColumnVisibility,
  });

  const pageCount = Math.ceil(data.length / pageSize);
  const safePage = Math.min(page, Math.max(0, pageCount - 1));

  useEffect(() => {
    setPage(0);
  }, [sorting]);

  const rows = table.getRowModel().rows;
  const pageRows = rows.slice(safePage * pageSize, safePage * pageSize + pageSize);

  const toggleableColumns = table.getAllLeafColumns().filter((c) => c.getCanHide());
  const columnControl = data.length > 0 && !(mobile && renderCard) && enableColumnVisibility && toggleableColumns.length > 1 ? (
    <div className="column-visibility">
      <button
        type="button"
        className="column-visibility-toggle"
        aria-expanded={showColumns}
        aria-controls={columnPanelId}
        onClick={() => setShowColumns((v) => !v)}
      >
        Columns {showColumns ? '▲' : '▼'}
      </button>
      {showColumns ? (
        <ul id={columnPanelId} className="column-visibility-panel" role="group" aria-label="Toggle columns">
          {toggleableColumns.map((column) => {
            const label = String(column.columnDef.header ?? column.id);
            return (
              <li key={column.id}>
                <label className="column-visibility-item">
                  <input
                    type="checkbox"
                    checked={column.getIsVisible()}
                    onChange={column.getToggleVisibilityHandler()}
                  />
                  <span>{label}</span>
                </label>
              </li>
            );
          })}
        </ul>
      ) : null}
    </div>
  ) : null;
  const toolbarRow = toolbar ? <div className="table-toolbar">{toolbar}{columnControl}</div> : columnControl;

  if (data.length === 0) {
    return <>{toolbarRow}{empty ?? <p className="table-empty">No rows.</p>}</>;
  }

  return (
    <div className="data-table-wrap" data-testid="data-table" tabIndex={0} aria-label={caption ? `${caption} table controls` : undefined}>
      {toolbarRow}
      {mobile && renderCard ? <>
        <label className="card-sort">Sort runs or models
          <select aria-label="Sort cards" value={sorting[0] ? `${sorting[0].id}:${sorting[0].desc ? 'desc' : 'asc'}` : ''}
            onChange={(event) => {
              const [id, direction] = event.target.value.split(':');
              setSorting(id ? [{ id, desc: direction === 'desc' }] : []);
            }}>
            <option value="">Published order</option>
            {table.getAllLeafColumns().filter((column) => column.getCanSort()).flatMap((column) => ['asc', 'desc'].map((direction) =>
              <option key={`${column.id}:${direction}`} value={`${column.id}:${direction}`}>{String(column.columnDef.header ?? column.id)} · {direction === 'asc' ? 'ascending' : 'descending'}</option>))}
          </select>
        </label>
        <ul className="data-card-list" aria-label={caption}>
          {pageRows.map((row) => <li key={row.id}>{renderCard(row.original)}</li>)}
        </ul>
      </> : <table className="data-table">
        {caption ? <caption className="sr-only">{caption}</caption> : null}
        <thead>
          {table.getHeaderGroups().map((group) => (
            <tr key={group.id}>
              {group.headers.map((header) => {
                const sortDir = header.column.getIsSorted();
                const isSorted = sortDir !== false;
                return (
                  <th
                    key={header.id}
                    scope="col"
                    data-column={header.column.id}
                    aria-sort={isSorted ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'}
                  >
                    {header.column.getCanSort() ? (
                      <button
                        type="button"
                        className="th-sort"
                        onClick={header.column.getToggleSortingHandler()}
                        aria-label={`Sort by ${String(header.column.columnDef.header ?? header.id)}`}
                      >
                        <table.FlexRender header={header} />
                        <span className="sort-indicator" aria-hidden="true">
                          {isSorted ? (sortDir === 'asc' ? '▲' : '▼') : '↕'}
                        </span>
                      </button>
                    ) : (
                      <table.FlexRender header={header} />
                    )}
                  </th>
                );
              })}
            </tr>
          ))}
        </thead>
        <tbody>
          {pageRows.map((row) => (
            <tr key={row.id}>
              {row.getVisibleCells().map((cell) => (
                <td key={cell.id} data-column={cell.column.id}>
                  <table.FlexRender cell={cell} />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>}
      {pageCount > 1 ? (
        <div className="table-pagination" data-testid="table-pagination">
          <button
            type="button"
            onClick={() => setPage((p) => Math.max(0, p - 1))}
            disabled={safePage === 0}
            aria-label="Previous page"
          >
            Previous
          </button>
          <span className="page-info">
            Page {safePage + 1} of {pageCount} ({data.length} rows)
          </span>
          <button
            type="button"
            onClick={() => setPage((p) => Math.min(pageCount - 1, p + 1))}
            disabled={safePage >= pageCount - 1}
            aria-label="Next page"
          >
            Next
          </button>
        </div>
      ) : null}
    </div>
  );
}
