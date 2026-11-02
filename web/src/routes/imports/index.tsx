import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  Group,
  Progress,
  Select,
  SimpleGrid,
  Stack,
  Tabs,
  Text,
} from '@mantine/core'
import { Dropzone } from '@mantine/dropzone'
import { IconFileZip, IconInfoCircle, IconRefresh, IconUpload, IconX } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo, useState } from 'react'
import { api, unwrap, uploadDeliveries, type Import, type RawDelivery } from '@/api/client'
import { ConfirmDialog } from '@/components/Confirm'
import { eventPolicies as policies } from '@/components/domain'
import { DataTable, Dim } from '@/components/DataTable'
import { EmptyBlock, PageHeader, QueryState, Section, StatusBadge, Time } from '@/components/ui'
import { fmtBytes, fmtNumber } from '@/lib/format'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'
import { AnchorLink } from '@/components/links'

interface Search {
  tab?: 'imports' | 'deliveries'
  state?: string
  kind?: string
}

export const Route = createFileRoute('/imports/')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    tab: s.tab === 'deliveries' ? 'deliveries' : undefined,
    state: typeof s.state === 'string' ? s.state : undefined,
    kind: typeof s.kind === 'string' ? s.kind : undefined,
  }),
  component: Imports,
})

const MAX_UPLOAD = 50 * 1024 * 1024

function Imports() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/imports/' })
  const can = useCan()
  const tab = search.tab ?? 'imports'
  return (
    <>
      <PageHeader
        title="Imports"
        description="Upload deliveries downloaded from the EU Data Act portal and follow every import, including collector runs."
      />
      {can('imports.write') ? <Uploader /> : null}
      <Tabs
        value={tab}
        onChange={(t) => void navigate({ search: { tab: t === 'deliveries' ? 'deliveries' : undefined } })}
      >
        <Tabs.List mb="md">
          <Tabs.Tab value="imports">Imports</Tabs.Tab>
          <Tabs.Tab value="deliveries">Raw deliveries</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="imports">
          <ImportList
            state={search.state}
            kind={search.kind}
            onFilter={(p) => void navigate({ search: (s) => ({ ...s, ...p }) })}
          />
        </Tabs.Panel>
        <Tabs.Panel value="deliveries">
          <RawDeliveries />
        </Tabs.Panel>
      </Tabs>
    </>
  )
}

function Uploader() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const vehicles = useQuery({
    queryKey: ['vehicles', 'list', false],
    queryFn: async () => unwrap(await api.GET('/vehicles', { params: { query: { limit: 200 } } })),
  })
  const system = useQuery({
    queryKey: ['settings', 'system'],
    queryFn: async () => unwrap(await api.GET('/settings/system')),
    retry: false,
  })
  const [files, setFiles] = useState<File[]>([])
  const [vehicle, setVehicle] = useState<string | null>(null)
  const [policy, setPolicy] = useState<string | null>(null)
  const effectivePolicy = policy ?? system.data?.upload_event_policy ?? 'none'
  const upload = useMutation({
    mutationFn: () => uploadDeliveries(files, vehicle ?? '', effectivePolicy),
    onSuccess: async (im) => {
      setFiles([])
      notifySuccess(`${String(files.length)} file(s) queued for import`)
      await qc.invalidateQueries({ queryKey: ['imports'] })
      void navigate({ to: '/imports/$importId', params: { importId: im.id } })
    },
    onError: (e) => {
      notifyError(e, 'Upload failed')
    },
  })
  const total = files.reduce((n, f) => n + f.size, 0)
  return (
    <Section
      title="Upload deliveries"
      description="ZIP or JSON files from the portal's data-delivery download. Duplicates are detected by content and skipped."
      p="lg"
    >
      <Stack p="lg" pt={0}>
        <Dropzone
          onDrop={(dropped) => {
            setFiles((cur) => [...cur, ...dropped].slice(0, 100))
          }}
          onReject={(r) => {
            notifyError(
              new Error(r.map((x) => `${x.file.name}: ${x.errors.map((e) => e.message).join(', ')}`).join('; ')),
              'Some files were rejected',
            )
          }}
          maxSize={MAX_UPLOAD}
          accept={{
            'application/zip': ['.zip'],
            'application/x-zip-compressed': ['.zip'],
            'application/json': ['.json'],
          }}
          multiple
          data-testid="upload-dropzone"
          inputProps={{ 'aria-label': 'Delivery files to import' }}
        >
          <Group justify="center" gap="md" mih={110} style={{ pointerEvents: 'none' }}>
            <Dropzone.Accept>
              <IconUpload size={36} />
            </Dropzone.Accept>
            <Dropzone.Reject>
              <IconX size={36} />
            </Dropzone.Reject>
            <Dropzone.Idle>
              <IconFileZip size={36} stroke={1.4} />
            </Dropzone.Idle>
            <div>
              <Text size="md" fw={550}>
                Drop delivery files here or click to choose
              </Text>
              <Text size="sm" c="dimmed">
                .zip or .json, up to 50 MiB each. Real deliveries contain your VIN and location; they stay on this
                server.
              </Text>
            </div>
          </Group>
        </Dropzone>
        {files.length > 0 ? (
          <Card withBorder p="sm" bg="var(--wp-surface2)">
            <Group justify="space-between" mb={6}>
              <Text size="sm" fw={550}>
                {files.length} file(s), {fmtBytes(total)}
              </Text>
              <Button
                variant="subtle"
                size="xs"
                onClick={() => {
                  setFiles([])
                }}
              >
                Clear
              </Button>
            </Group>
            <Stack
              gap={2}
              mah={140}
              style={{ overflow: 'auto' }}
              tabIndex={0}
              role="region"
              aria-label="Selected files"
            >
              {files.map((f, i) => (
                <Text key={`${f.name}-${String(i)}`} size="xs" ff="monospace">
                  {f.name} · {fmtBytes(f.size)}
                </Text>
              ))}
            </Stack>
          </Card>
        ) : null}
        <SimpleGrid cols={{ base: 1, sm: 3 }}>
          <Select
            label="Vehicle"
            description="Empty matches each delivery's VIN to a vehicle."
            placeholder="Match by VIN"
            data={(vehicles.data?.items ?? []).map((v) => ({ value: v.id, label: v.display_name }))}
            value={vehicle}
            onChange={setVehicle}
            clearable
          />
          <Select
            label="Events"
            description="Uploaded history is backfill: by default it does not publish events."
            data={policies}
            value={effectivePolicy}
            onChange={setPolicy}
            allowDeselect={false}
          />
          <Group align="flex-end">
            <Button
              leftSection={<IconUpload size={16} />}
              disabled={files.length === 0}
              loading={upload.isPending}
              onClick={() => {
                upload.mutate()
              }}
              fullWidth
            >
              Import {files.length ? `${String(files.length)} file(s)` : ''}
            </Button>
          </Group>
        </SimpleGrid>
        {effectivePolicy === 'publish' ? (
          <Alert variant="light" color="yellow" icon={<IconInfoCircle size={18} />}>
            Events from this import are delivered to every route that accepts backfill. Consumers see{' '}
            <code>backfill: true</code>.
          </Alert>
        ) : null}
      </Stack>
    </Section>
  )
}

function ImportProgress({ im }: { im: Import }) {
  const pct = im.files_total ? Math.round((im.files_done / im.files_total) * 100) : 0
  if (im.state !== 'running' && im.state !== 'queued') return null
  return <Progress value={pct} animated={im.state === 'running'} size="sm" aria-label="Import progress" />
}

function ImportList({
  state,
  kind,
  onFilter,
}: {
  state?: string
  kind?: string
  onFilter: (p: Partial<Search>) => void
}) {
  const navigate = useNavigate()
  const list = useQuery({
    queryKey: ['imports', 'list', state, kind],
    queryFn: async () =>
      unwrap(
        await api.GET('/imports', {
          params: {
            query: {
              state: state as Import['state'] | undefined,
              kind: kind as Import['kind'] | undefined,
              limit: 100,
            },
          },
        }),
      ),
    refetchInterval: (q) =>
      q.state.data?.items.some((i) => i.state === 'running' || i.state === 'queued') ? 2000 : 30_000,
  })
  const columns = useMemo<ColumnDef<Import>[]>(
    () => [
      {
        header: 'Started',
        accessorKey: 'created_at',
        cell: ({ row }) => (
          <AnchorLink to="/imports/$importId" params={{ importId: row.original.id }} size="sm">
            <Time value={row.original.created_at} />
          </AnchorLink>
        ),
      },
      { header: 'Kind', accessorKey: 'kind' },
      {
        header: 'State',
        accessorKey: 'state',
        cell: ({ row }) => (
          <Stack gap={4}>
            <StatusBadge state={row.original.state} />
            <ImportProgress im={row.original} />
          </Stack>
        ),
      },
      {
        header: 'Files',
        accessorFn: (i) => i.files_total,
        cell: ({ row }) => `${String(row.original.files_done)}/${String(row.original.files_total)}`,
      },
      {
        header: 'Accepted',
        accessorKey: 'observations_accepted',
        cell: ({ getValue }) => <span className="wp-num">{fmtNumber(getValue() as number)}</span>,
      },
      {
        header: 'Duplicates',
        accessorFn: (i) => i.deliveries_duplicate + i.observations_duplicate,
        cell: ({ row }) => (
          <span className="wp-num">
            {fmtNumber(row.original.observations_duplicate)} obs · {fmtNumber(row.original.deliveries_duplicate)} files
          </span>
        ),
      },
      {
        header: 'Problems',
        accessorFn: (i) => i.deliveries_rejected + i.deliveries_unrecognized + i.observations_rejected,
        cell: ({ row }) => {
          const i = row.original
          const parts = [
            i.deliveries_rejected ? `${String(i.deliveries_rejected)} rejected` : '',
            i.deliveries_unrecognized ? `${String(i.deliveries_unrecognized)} unrecognized` : '',
            i.observations_rejected ? `${String(i.observations_rejected)} invalid values` : '',
          ].filter(Boolean)
          return parts.length ? (
            <Text size="sm" c="var(--wp-warning)">
              {parts.join(' · ')}
            </Text>
          ) : (
            <Dim>none</Dim>
          )
        },
      },
      {
        header: 'Events',
        accessorKey: 'events_created',
        cell: ({ row }) =>
          row.original.event_policy === 'none' ? (
            <Dim>off</Dim>
          ) : (
            fmtNumber(row.original.events_created || row.original.events_previewed)
          ),
      },
    ],
    [],
  )
  return (
    <Card p={0}>
      <Group p="md" gap="sm">
        <Select
          placeholder="Any state"
          aria-label="State"
          data={['queued', 'running', 'succeeded', 'partial', 'failed', 'cancelled']}
          value={state ?? null}
          onChange={(s) => {
            onFilter({ state: s ?? undefined })
          }}
          clearable
          w={180}
        />
        <Select
          placeholder="Any kind"
          aria-label="Kind"
          data={['upload', 'collection', 'reprocess']}
          value={kind ?? null}
          onChange={(k) => {
            onFilter({ kind: k ?? undefined })
          }}
          clearable
          w={180}
        />
      </Group>
      <QueryState query={list} loadingLabel="Loading imports…">
        {() => (
          <DataTable
            caption="Imports"
            testId="imports-table"
            data={list.data?.items ?? []}
            columns={columns}
            onRowClick={(i) => void navigate({ to: '/imports/$importId', params: { importId: i.id } })}
            empty={
              <EmptyBlock
                title="No imports"
                description="Upload a delivery above, or configure a source to collect automatically."
              />
            }
          />
        )}
      </QueryState>
    </Card>
  )
}

function RawDeliveries() {
  const can = useCan()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [status, setStatus] = useState<string | null>(null)
  const [selected, setSelected] = useState<string[]>([])
  const [confirm, setConfirm] = useState(false)
  const [policy, setPolicy] = useState('none')
  const list = useQuery({
    queryKey: ['raw-deliveries', status],
    queryFn: async () =>
      unwrap(
        await api.GET('/raw-deliveries', {
          params: { query: { status: (status ?? undefined) as RawDelivery['status'] | undefined, limit: 200 } },
        }),
      ),
  })
  const reprocess = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/imports/reprocess', { body: { delivery_ids: selected, event_policy: policy as 'none' } }),
      ),
    onSuccess: async (im) => {
      setConfirm(false)
      setSelected([])
      await qc.invalidateQueries({ queryKey: ['imports'] })
      void navigate({ to: '/imports/$importId', params: { importId: im.id } })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const columns = useMemo<ColumnDef<RawDelivery>[]>(
    () => [
      ...(can('imports.write')
        ? [
            {
              id: 'sel',
              header: '',
              enableSorting: false,
              cell: ({ row }: { row: { original: RawDelivery } }) => (
                <Checkbox
                  aria-label="Select delivery"
                  disabled={!row.original.raw_content_retained}
                  checked={selected.includes(row.original.id)}
                  onChange={(e) => {
                    const on = e.currentTarget.checked
                    setSelected((s) => (on ? [...s, row.original.id] : s.filter((x) => x !== row.original.id)))
                  }}
                  onClick={(e) => {
                    e.stopPropagation()
                  }}
                />
              ),
            } satisfies ColumnDef<RawDelivery>,
          ]
        : []),
      { header: 'Received', accessorKey: 'received_at', cell: ({ row }) => <Time value={row.original.received_at} /> },
      {
        header: 'File',
        accessorFn: (d) => d.original_name ?? '',
        cell: ({ row }) => (
          <div>
            <Text size="sm" ff="monospace" lineClamp={1} maw={280}>
              {row.original.original_name || row.original.sha256.slice(0, 16)}
            </Text>
            <Text size="xs" c="dimmed">
              {fmtBytes(row.original.size)} · {row.original.origin}
            </Text>
          </div>
        ),
      },
      {
        header: 'Format',
        accessorKey: 'format',
        cell: ({ row }) => (
          <Badge variant="light" color={row.original.format === 'unknown' ? 'yellow' : 'gray'} tt="none">
            {row.original.format}
          </Badge>
        ),
      },
      { header: 'Status', accessorKey: 'status', cell: ({ row }) => <StatusBadge state={row.original.status} /> },
      {
        header: 'Observations',
        accessorKey: 'observation_count',
        cell: ({ getValue }) => <span className="wp-num">{fmtNumber(getValue() as number)}</span>,
      },
      {
        header: 'Data time',
        accessorFn: (d) => d.time_from ?? '',
        cell: ({ row }) =>
          row.original.time_from ? <Time value={row.original.time_to ?? row.original.time_from} /> : <Dim>—</Dim>,
      },
      {
        header: 'Raw kept until',
        accessorFn: (d) => d.retention_until ?? '',
        cell: ({ row }) =>
          row.original.raw_content_retained ? (
            <Time value={row.original.retention_until} seconds={false} />
          ) : (
            <Dim>purged</Dim>
          ),
      },
    ],
    [can, selected],
  )
  return (
    <Card p={0}>
      <Group p="md" justify="space-between">
        <Select
          placeholder="Any status"
          aria-label="Status"
          data={['stored', 'processed', 'rejected', 'unrecognized', 'purged']}
          value={status}
          onChange={setStatus}
          clearable
          w={200}
        />
        {can('imports.write') ? (
          <Button
            variant="default"
            leftSection={<IconRefresh size={16} />}
            disabled={selected.length === 0}
            onClick={() => {
              setConfirm(true)
            }}
          >
            Reprocess {selected.length ? `${String(selected.length)} selected` : ''}
          </Button>
        ) : null}
      </Group>
      <QueryState query={list} loadingLabel="Loading deliveries…">
        {() => (
          <DataTable
            caption="Raw deliveries"
            data={list.data?.items ?? []}
            columns={columns}
            empty={
              <EmptyBlock
                title="No stored deliveries"
                description="Every uploaded or collected file is stored here (content-addressed) until retention removes it."
              />
            }
          />
        )}
      </QueryState>
      <ConfirmDialog
        opened={confirm}
        onClose={() => {
          setConfirm(false)
        }}
        onConfirm={() => {
          reprocess.mutate()
        }}
        loading={reprocess.isPending}
        title="Reprocess deliveries?"
        confirmLabel="Reprocess"
      >
        <Stack gap="sm">
          <Text size="sm">
            {selected.length} stored deliveries are parsed again with the current parser and dictionary. Observations
            already stored are deduplicated; changed values are recorded as corrections.
          </Text>
          <Select
            label="Events"
            data={policies}
            value={policy}
            onChange={(p) => {
              setPolicy(p ?? 'none')
            }}
            allowDeselect={false}
          />
        </Stack>
      </ConfirmDialog>
    </Card>
  )
}
