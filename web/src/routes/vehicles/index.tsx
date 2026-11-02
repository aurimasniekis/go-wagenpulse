import { Button, Card, Group, Modal, Switch, Text } from '@mantine/core'
import { IconPlus } from '@tabler/icons-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo, useState } from 'react'
import { api, idempotencyKey, unwrap, type VehicleSummary } from '@/api/client'
import { DataTable, Dim } from '@/components/DataTable'
import { EmptyBlock, PageHeader, QueryState, StatusBadge, Time } from '@/components/ui'
import { brands, VehicleForm } from '@/components/VehicleForm'
import { fmtNumber, maskVIN } from '@/lib/format'
import { notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'
import { AnchorLink } from '@/components/links'

export const Route = createFileRoute('/vehicles/')({
  validateSearch: (s: Record<string, unknown>): { create?: number } => ({ create: s.create ? 1 : undefined }),
  component: Vehicles,
})

const brandLabel = (b: string) => brands.find((x) => x.value === b)?.label ?? b

function Vehicles() {
  const can = useCan()
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/vehicles/' })
  const qc = useQueryClient()
  const [archived, setArchived] = useState(false)
  const list = useQuery({
    queryKey: ['vehicles', 'list', archived],
    queryFn: async () =>
      unwrap(await api.GET('/vehicles', { params: { query: { include_archived: archived, limit: 200 } } })),
  })
  const close = () => void navigate({ search: {} })
  const columns = useMemo<ColumnDef<VehicleSummary>[]>(
    () => [
      {
        header: 'Vehicle',
        accessorKey: 'display_name',
        cell: ({ row }) => (
          <div>
            <AnchorLink to="/vehicles/$vehicleId" params={{ vehicleId: row.original.id }} fw={600} size="sm">
              {row.original.display_name}
            </AnchorLink>
            <Text size="xs" c="dimmed" ff="monospace">
              {maskVIN(row.original.vin)}
            </Text>
          </div>
        ),
      },
      { header: 'Brand', accessorKey: 'brand', cell: ({ getValue }) => brandLabel(getValue() as string) },
      {
        header: 'Model',
        accessorFn: (v) => [v.model, v.model_year].filter(Boolean).join(' '),
        cell: ({ getValue }) => (getValue() as string) || <Dim>—</Dim>,
      },
      {
        header: 'State',
        id: 'state',
        accessorFn: (v) =>
          v.archived
            ? 'archived'
            : !v.freshness.last_observed_at
              ? 'no data'
              : v.freshness.stale_signals === v.freshness.signals
                ? 'stale'
                : 'fresh',
        cell: ({ getValue }) => {
          const s = getValue() as string
          return (
            <StatusBadge state={s === 'no data' ? 'disabled' : s} label={s === 'no data' ? 'No data' : undefined} />
          )
        },
      },
      {
        header: 'Signals',
        accessorFn: (v) => v.freshness.signals,
        cell: ({ getValue }) => <span className="wp-num">{fmtNumber(getValue() as number)}</span>,
      },
      {
        header: 'Last data',
        accessorFn: (v) => v.freshness.last_observed_at ?? '',
        cell: ({ row }) => <Time value={row.original.freshness.last_observed_at} relative />,
      },
    ],
    [],
  )
  return (
    <>
      <PageHeader
        title="Vehicles"
        description="Vehicles whose EU Data Act deliveries WagenPulse stores. Archiving hides a vehicle but keeps its telemetry."
        actions={
          <Group>
            <Switch
              label="Show archived"
              checked={archived}
              onChange={(e) => {
                setArchived(e.currentTarget.checked)
              }}
            />
            {can('vehicles.write') ? (
              <Button leftSection={<IconPlus size={16} />} onClick={() => void navigate({ search: { create: 1 } })}>
                Add vehicle
              </Button>
            ) : null}
          </Group>
        }
      />
      <Card p={0}>
        <QueryState query={list} loadingLabel="Loading vehicles…">
          {() => (
            <DataTable
              caption="Vehicles"
              testId="vehicles-table"
              data={list.data?.items ?? []}
              columns={columns}
              onRowClick={(v) => void navigate({ to: '/vehicles/$vehicleId', params: { vehicleId: v.id } })}
              empty={
                <EmptyBlock title="No vehicles" description="Add a vehicle with its VIN to start importing data." />
              }
            />
          )}
        </QueryState>
      </Card>
      <Modal opened={Boolean(search.create)} onClose={close} title={<Text fw={650}>Add vehicle</Text>} size="lg">
        <VehicleForm
          submitLabel="Add vehicle"
          onCancel={close}
          onSubmit={async (body) => {
            const v = unwrap(await api.POST('/vehicles', { body, headers: idempotencyKey() }))
            await qc.invalidateQueries({ queryKey: ['vehicles'] })
            notifySuccess(`${v.display_name} added`)
            void navigate({ to: '/vehicles/$vehicleId', params: { vehicleId: v.id }, search: {} })
          }}
        />
      </Modal>
    </>
  )
}
