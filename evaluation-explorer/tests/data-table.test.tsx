// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DataTable, type DataTableColumn } from '../src/components/DataTable';

type Row = { id: string; name: string };

const columns: DataTableColumn<Row>[] = [
  { id: 'id', header: 'ID', accessorKey: 'id' },
  { id: 'name', header: 'Name', accessorKey: 'name' },
];

const data: Row[] = [
  { id: 'c', name: 'Charlie' },
  { id: 'a', name: 'Alice' },
  { id: 'b', name: 'Bob' },
];

describe('DataTable', () => {
  afterEach(() => vi.unstubAllGlobals());
  it('sorts and paginates mobile cards with the same row selection', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() }));
    const user = userEvent.setup();
    render(<DataTable data={data} columns={columns} pageSize={2} caption="Mobile models"
      renderCard={(record) => <strong>{record.name}</strong>} isRowSelected={(record) => record.id === 'a'} />);
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    await user.selectOptions(screen.getByRole('combobox', { name: 'Sort cards' }), 'name:asc');
    expect(screen.getByText('Alice').closest('li')).toHaveClass('data-row-selected');
    expect(screen.getByText('Bob')).toBeInTheDocument();
    expect(screen.queryByText('Charlie')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(screen.getByText('Charlie')).toBeInTheDocument();
    expect(screen.queryByText('Alice')).not.toBeInTheDocument();
  });
  it('sorts rows when a sortable column header is clicked', async () => {
    const user = userEvent.setup();

    render(<DataTable data={data} columns={columns} caption="Sortable rows" enableColumnVisibility={false} />);

    const bodyCells = () =>
      Array.from(document.querySelectorAll('.data-table tbody tr')).map((row) =>
        row.querySelector('td')?.textContent,
      );

    expect(bodyCells()).toEqual(['c', 'a', 'b']);

    await user.click(screen.getByRole('button', { name: 'Sort by ID' }));

    expect(bodyCells()).toEqual(['a', 'b', 'c']);
  });

  it('hides a column and its cells when its visibility toggle is cleared', async () => {
    const user = userEvent.setup();

    render(<DataTable data={data} columns={columns} caption="Toggleable columns" />);

    const headerLabels = () =>
      Array.from(document.querySelectorAll('.data-table thead th')).map((cell) => cell.textContent);

    expect(headerLabels()).toEqual(['ID↕', 'Name↕']);

    await user.click(screen.getByRole('button', { name: /Columns/ }));
    await user.click(screen.getByRole('checkbox', { name: 'Name' }));

    expect(headerLabels()).toEqual(['ID↕']);
    expect(document.querySelectorAll('.data-table tbody tr:first-child td')).toHaveLength(1);
    expect(screen.getByRole('checkbox', { name: 'Name' })).not.toBeChecked();
  });
});
