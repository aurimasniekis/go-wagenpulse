import { Group, ScrollArea, Table, Text, UnstyledButton } from '@mantine/core'
import { IconArrowDown, IconArrowUp, IconSelector } from '@tabler/icons-react'
import {
  flexRender,
  getCoreRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type SortingState,
} from '@tanstack/react-table'
import { useState, type ReactNode } from 'react'
import { EmptyBlock } from './ui'

/**
 * Accessible data table on TanStack Table with client-side sorting. Large server-side lists paginate with cursors in
 * their pages; this component renders the current page.
 */
export function DataTable<T>({
  data,
  columns,
  empty,
  onRowClick,
  initialSort,
  caption,
  testId,
}: {
  data: T[]
  columns: ColumnDef<T>[]
  empty?: ReactNode
  onRowClick?: (row: T) => void
  initialSort?: SortingState
  caption: string
  testId?: string
}) {
  const [sorting, setSorting] = useState<SortingState>(initialSort ?? [])
  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Table returns non-memoizable functions by design
  const table = useReactTable({
    data,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  })
  if (data.length === 0) return <>{empty ?? <EmptyBlock title="Nothing here yet" />}</>
  return (
    <ScrollArea type="auto">
      <Table className="wp-table" highlightOnHover={Boolean(onRowClick)} data-testid={testId} miw={640}>
        <caption className="visually-hidden" style={{ position: 'absolute', left: -9999 }}>
          {caption}
        </caption>
        <Table.Thead>
          {table.getHeaderGroups().map((hg) => (
            <Table.Tr key={hg.id}>
              {hg.headers.map((h) => {
                const sorted = h.column.getIsSorted()
                const canSort = h.column.getCanSort()
                return (
                  <Table.Th
                    key={h.id}
                    aria-sort={sorted === 'asc' ? 'ascending' : sorted === 'desc' ? 'descending' : undefined}
                  >
                    {h.isPlaceholder ? null : canSort ? (
                      <UnstyledButton
                        onClick={h.column.getToggleSortingHandler()}
                        aria-label={`Sort by ${String(h.column.columnDef.header)}`}
                      >
                        <Group gap={4} wrap="nowrap">
                          <span>{flexRender(h.column.columnDef.header, h.getContext())}</span>
                          {sorted === 'asc' ? (
                            <IconArrowUp size={12} />
                          ) : sorted === 'desc' ? (
                            <IconArrowDown size={12} />
                          ) : (
                            <IconSelector size={12} opacity={0.5} />
                          )}
                        </Group>
                      </UnstyledButton>
                    ) : (
                      flexRender(h.column.columnDef.header, h.getContext())
                    )}
                  </Table.Th>
                )
              })}
            </Table.Tr>
          ))}
        </Table.Thead>
        <Table.Tbody>
          {table.getRowModel().rows.map((row) => (
            <Table.Tr
              key={row.id}
              onClick={
                onRowClick
                  ? () => {
                      onRowClick(row.original)
                    }
                  : undefined
              }
              style={onRowClick ? { cursor: 'pointer' } : undefined}
            >
              {row.getVisibleCells().map((cell) => (
                <Table.Td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</Table.Td>
              ))}
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </ScrollArea>
  )
}

/** Muted text cell. */
export function Dim({ children }: { children: ReactNode }) {
  return (
    <Text size="sm" c="dimmed" span>
      {children}
    </Text>
  )
}
