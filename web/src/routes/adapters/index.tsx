import { Badge, Button, Card, Modal, Text } from '@mantine/core'
import { IconPlus } from '@tabler/icons-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo } from 'react'
import { api, idempotencyKey, unwrap, type Adapter } from '@/api/client'
import { AdapterForm } from '@/components/AdapterForm'
import { DataTable } from '@/components/DataTable'
import { AnchorLink } from '@/components/links'
import { EmptyBlock, PageHeader, QueryState, StatusBadge, Time } from '@/components/ui'
import { fmtNumber } from '@/lib/format'
import { notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'

export const Route = createFileRoute('/adapters/')({
  validateSearch: (s: Record<string, unknown>): { create?: number } => ({ create: s.create ? 1 : undefined }),
  component: Adapters,
})

function Adapters() {
  const can = useCan()
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/adapters/' })
  const qc = useQueryClient()
  const list = useQuery({
    queryKey: ['adapters'],
    queryFn: async () => unwrap(await api.GET('/adapters')),
    refetchInterval: 15_000,
  })
  const close = () => void navigate({ search: {} })
  const columns = useMemo<ColumnDef<Adapter>[]>(
    () => [
      {
        header: 'Adapter',
        accessorKey: 'name',
        cell: ({ row }) => (
          <div>
            <AnchorLink to="/adapters/$adapterId" params={{ adapterId: row.original.id }} fw={600} size="sm">
              {row.original.name}
            </AnchorLink>
            <Text size="xs" c="dimmed" ff="monospace">
              {row.original.config.url}
            </Text>
          </div>
        ),
      },
      {
        header: 'Connection',
        accessorFn: (a) => (a.enabled ? (a.status?.state ?? 'connecting') : 'disabled'),
        cell: ({ getValue }) => <StatusBadge state={getValue() as string} />,
      },
      {
        header: 'Pending',
        accessorFn: (a) => a.queue?.pending ?? 0,
        cell: ({ getValue }) => <span className="wp-num">{fmtNumber(getValue() as number)}</span>,
      },
      {
        header: 'Retrying',
        accessorFn: (a) => a.queue?.retrying ?? 0,
        cell: ({ getValue }) => <span className="wp-num">{fmtNumber(getValue() as number)}</span>,
      },
      {
        header: 'Dead',
        accessorFn: (a) => a.queue?.dead ?? 0,
        cell: ({ getValue }) =>
          (getValue() as number) > 0 ? (
            <Badge color="red">{fmtNumber(getValue() as number)}</Badge>
          ) : (
            <span className="wp-num">0</span>
          ),
      },
      {
        header: 'Delivered 24 h',
        accessorFn: (a) => a.queue?.succeeded_24h ?? 0,
        cell: ({ getValue }) => <span className="wp-num">{fmtNumber(getValue() as number)}</span>,
      },
      {
        header: 'Last success',
        accessorFn: (a) => a.status?.last_ok_at ?? '',
        cell: ({ row }) => <Time value={row.original.status?.last_ok_at} relative />,
      },
    ],
    [],
  )
  return (
    <>
      <PageHeader
        title="Adapters"
        description="Pulse (NATS JetStream) destinations. Each adapter delivers independently, so one failing adapter never blocks the others."
        actions={
          can('adapters.write') ? (
            <Button leftSection={<IconPlus size={16} />} onClick={() => void navigate({ search: { create: 1 } })}>
              Add adapter
            </Button>
          ) : null
        }
      />
      <Card p={0}>
        <QueryState query={list} loadingLabel="Loading adapters…">
          {() => (
            <DataTable
              caption="Adapters"
              testId="adapters-table"
              data={list.data ?? []}
              columns={columns}
              onRowClick={(a) => void navigate({ to: '/adapters/$adapterId', params: { adapterId: a.id } })}
              empty={
                <EmptyBlock
                  title="No adapters"
                  description="Add a Pulse adapter, then create a route that sends rule events to it."
                />
              }
            />
          )}
        </QueryState>
      </Card>
      <Modal opened={Boolean(search.create)} onClose={close} title={<Text fw={650}>Add Pulse adapter</Text>} size="xl">
        <AdapterForm
          submitLabel="Add adapter"
          onCancel={close}
          onSubmit={async (body) => {
            const a = unwrap(await api.POST('/adapters', { body, headers: idempotencyKey() }))
            await qc.invalidateQueries({ queryKey: ['adapters'] })
            notifySuccess(`${a.name} added`)
            void navigate({ to: '/adapters/$adapterId', params: { adapterId: a.id }, search: {} })
          }}
        />
      </Modal>
    </>
  )
}
