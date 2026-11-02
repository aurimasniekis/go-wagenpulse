import { Badge, Button, Card, Modal, Text } from '@mantine/core'
import { IconPlus } from '@tabler/icons-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo } from 'react'
import { api, idempotencyKey, unwrap, type Source } from '@/api/client'
import { DataTable, Dim } from '@/components/DataTable'
import { SourceForm } from '@/components/SourceForm'
import { EmptyBlock, PageHeader, QueryState, StatusBadge, Time } from '@/components/ui'
import { fmtDuration } from '@/lib/format'
import { notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'
import { AnchorLink } from '@/components/links'

export const Route = createFileRoute('/sources/')({
  validateSearch: (s: Record<string, unknown>): { create?: number } => ({ create: s.create ? 1 : undefined }),
  component: Sources,
})

function Sources() {
  const can = useCan()
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/sources/' })
  const qc = useQueryClient()
  const list = useQuery({
    queryKey: ['sources'],
    queryFn: async () => unwrap(await api.GET('/sources')),
    refetchInterval: 30_000,
  })
  const close = () => void navigate({ search: {} })
  const columns = useMemo<ColumnDef<Source>[]>(
    () => [
      {
        header: 'Source',
        accessorKey: 'name',
        cell: ({ row }) => (
          <div>
            <AnchorLink to="/sources/$sourceId" params={{ sourceId: row.original.id }} fw={600} size="sm">
              {row.original.name}
            </AnchorLink>
            <Text size="xs" c="dimmed">
              {row.original.brand} · {row.original.country.toUpperCase()} ·{' '}
              {row.original.auth_mode === 'password' ? 'scripted login' : 'session cookie'}
            </Text>
          </div>
        ),
      },
      {
        header: 'State',
        accessorFn: (s) => (s.enabled ? (s.status?.state ?? 'configured') : 'disabled'),
        cell: ({ getValue }) => <StatusBadge state={getValue() as string} />,
      },
      { header: 'Vehicles', accessorFn: (s) => s.vehicle_ids.length },
      { header: 'Interval', accessorKey: 'interval_minutes', cell: ({ getValue }) => `${String(getValue())} min` },
      {
        header: 'Last success',
        accessorFn: (s) => s.status?.last_success_at ?? '',
        cell: ({ row }) => <Time value={row.original.status?.last_success_at} relative />,
      },
      {
        header: 'Lag',
        accessorFn: (s) => s.status?.lag_seconds ?? 0,
        cell: ({ row }) =>
          row.original.status?.lag_seconds !== undefined ? fmtDuration(row.original.status.lag_seconds) : <Dim>—</Dim>,
      },
      {
        header: 'Next run',
        accessorFn: (s) => s.status?.next_run_at ?? '',
        cell: ({ row }) =>
          row.original.enabled ? <Time value={row.original.status?.next_run_at} relative /> : <Dim>disabled</Dim>,
      },
    ],
    [],
  )
  return (
    <>
      <PageHeader
        title={
          <>
            Sources{' '}
            <Badge variant="light" color="yellow" tt="none" style={{ verticalAlign: 'middle' }}>
              unofficial portal interface
            </Badge>
          </>
        }
        description="Automatic collection from the EU Data Act portal. Uploading files on the Imports page works without any source."
        actions={
          can('sources.write') ? (
            <Button leftSection={<IconPlus size={16} />} onClick={() => void navigate({ search: { create: 1 } })}>
              Add source
            </Button>
          ) : null
        }
      />
      <Card p={0}>
        <QueryState query={list} loadingLabel="Loading sources…">
          {() => (
            <DataTable
              caption="Sources"
              testId="sources-table"
              data={list.data ?? []}
              columns={columns}
              onRowClick={(s) => void navigate({ to: '/sources/$sourceId', params: { sourceId: s.id } })}
              empty={
                <EmptyBlock
                  title="No sources"
                  description="Add a source to collect deliveries automatically, or upload files on the Imports page."
                />
              }
            />
          )}
        </QueryState>
      </Card>
      <Modal opened={Boolean(search.create)} onClose={close} title={<Text fw={650}>Add portal source</Text>} size="xl">
        <SourceForm
          submitLabel="Add source"
          onCancel={close}
          onSubmit={async (body) => {
            const s = unwrap(await api.POST('/sources', { body, headers: idempotencyKey() }))
            await qc.invalidateQueries({ queryKey: ['sources'] })
            notifySuccess(`${s.name} added`)
            void navigate({ to: '/sources/$sourceId', params: { sourceId: s.id }, search: {} })
          }}
        />
      </Modal>
    </>
  )
}
