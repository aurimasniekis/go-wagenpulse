import { Alert, Badge, Button, Card, Code, Drawer, Group, Select, Text, TextInput } from '@mantine/core'
import { useDebouncedValue } from '@mantine/hooks'
import { IconShieldCheck } from '@tabler/icons-react'
import { useInfiniteQuery, useMutation } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { useState } from 'react'
import { api, unwrap, type AuditRecord } from '@/api/client'
import { DataTable, Dim } from '@/components/DataTable'
import { DefinitionList, EmptyBlock, PageHeader, QueryState, Time } from '@/components/ui'
import { notifyError } from '@/lib/notify'

interface Search {
  action?: string
  outcome?: string
  actor?: string
  seq?: number
}

export const Route = createFileRoute('/admin/audit')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    action: typeof s.action === 'string' && s.action ? s.action : undefined,
    outcome: typeof s.outcome === 'string' && s.outcome ? s.outcome : undefined,
    actor: typeof s.actor === 'string' && s.actor ? s.actor : undefined,
    seq: typeof s.seq === 'number' ? s.seq : undefined,
  }),
  component: Audit,
})

const columns: ColumnDef<AuditRecord>[] = [
  { header: '#', accessorKey: 'seq', cell: ({ getValue }) => <span className="wp-num">{getValue() as number}</span> },
  { header: 'Time', accessorKey: 'at', cell: ({ row }) => <Time value={row.original.at_time} /> },
  { header: 'Action', accessorKey: 'action', cell: ({ getValue }) => <Code>{getValue() as string}</Code> },
  {
    header: 'Outcome',
    accessorKey: 'outcome',
    cell: ({ row }) => (
      <Badge variant="light" color={row.original.outcome === 'success' ? 'green' : 'red'}>
        {row.original.outcome}
        {row.original.reason_code ? ` · ${row.original.reason_code}` : ''}
      </Badge>
    ),
  },
  {
    header: 'Actor',
    accessorFn: (a) => a.actor_id ?? a.actor_type,
    cell: ({ row }) => (
      <Text size="xs">
        {row.original.actor_type}
        {row.original.actor_id ? ` ${row.original.actor_id}` : ''}
        {row.original.auth_method ? <Dim> · {row.original.auth_method}</Dim> : null}
      </Text>
    ),
  },
  {
    header: 'Target',
    accessorFn: (a) => a.target_id ?? '',
    cell: ({ row }) =>
      row.original.target_type ? (
        <Text size="xs">
          {row.original.target_type} {row.original.target_id}
        </Text>
      ) : (
        <Dim>—</Dim>
      ),
  },
  {
    header: 'IP',
    accessorFn: (a) => a.source_ip ?? '',
    cell: ({ getValue }) => (
      <Text size="xs" ff="monospace">
        {(getValue() as string) || '—'}
      </Text>
    ),
  },
]

function Audit() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/admin/audit' })
  const set = (p: Partial<Search>) => void navigate({ search: (s) => ({ ...s, ...p }), replace: true })
  const [actionText, setActionText] = useState(search.action ?? '')
  const [action] = useDebouncedValue(actionText, 300)
  const list = useInfiniteQuery({
    queryKey: ['audit', action, search.outcome, search.actor],
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET('/audit-events', {
          params: {
            query: {
              action: action || undefined,
              outcome: search.outcome as 'success' | 'failure' | 'denied' | undefined,
              actor_id: search.actor,
              limit: 100,
              before_seq: pageParam || undefined,
            },
          },
        }),
      ),
    initialPageParam: 0,
    getNextPageParam: (last) => last.next_before_seq,
  })
  const verify = useMutation({
    mutationFn: async () => unwrap(await api.POST('/audit-events/verify')),
    onError: (e) => {
      notifyError(e)
    },
  })
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  const selected = items.find((a) => a.seq === search.seq)
  return (
    <>
      <PageHeader
        title="Audit log"
        description="Security-relevant actions in a hash-chained, append-only log. Sensitive details are encrypted at rest."
        actions={
          <Button
            variant="default"
            leftSection={<IconShieldCheck size={16} />}
            loading={verify.isPending}
            onClick={() => {
              verify.mutate()
            }}
          >
            Verify chain
          </Button>
        }
      />
      {verify.data ? (
        <Alert
          color={verify.data.ok ? 'green' : 'red'}
          variant="light"
          mb="lg"
          withCloseButton
          onClose={() => {
            verify.reset()
          }}
          title={verify.data.ok ? 'Audit chain intact' : 'Audit chain broken'}
        >
          {verify.data.ok
            ? `${String(verify.data.events)} records verified.`
            : `Problem at record ${String(verify.data.broken_at ?? '?')}: ${verify.data.problem ?? 'unknown'}. Investigate for tampering and restore from a backup if needed.`}
        </Alert>
      ) : null}
      <Card p={0}>
        <Group p="md" gap="sm" wrap="wrap">
          <TextInput
            placeholder="Action, e.g. token.created"
            aria-label="Action"
            value={actionText}
            onChange={(e) => {
              setActionText(e.currentTarget.value)
              set({ action: e.currentTarget.value || undefined })
            }}
            w={240}
          />
          <Select
            placeholder="Any outcome"
            aria-label="Outcome"
            data={['success', 'failure', 'denied']}
            value={search.outcome ?? null}
            onChange={(o) => {
              set({ outcome: o ?? undefined })
            }}
            clearable
            w={160}
          />
          <TextInput
            placeholder="Actor ID"
            aria-label="Actor ID"
            value={search.actor ?? ''}
            onChange={(e) => {
              set({ actor: e.currentTarget.value || undefined })
            }}
            w={240}
          />
        </Group>
        <QueryState query={list} loadingLabel="Loading the audit log…">
          {() => (
            <>
              <DataTable
                caption="Audit log"
                testId="audit-table"
                data={items}
                columns={columns}
                onRowClick={(a) => {
                  set({ seq: a.seq })
                }}
                empty={<EmptyBlock title="No matching audit records" />}
              />
              {list.hasNextPage ? (
                <Group justify="center" p="md">
                  <Button variant="default" loading={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                    Load older records
                  </Button>
                </Group>
              ) : null}
            </>
          )}
        </QueryState>
      </Card>
      <Drawer
        opened={Boolean(selected)}
        onClose={() => {
          set({ seq: undefined })
        }}
        position="right"
        size="lg"
        title={<Text fw={650}>Audit record {selected?.seq}</Text>}
      >
        {selected ? (
          <>
            <DefinitionList
              items={[
                ['Action', <Code key="a">{selected.action}</Code>],
                ['Outcome', `${selected.outcome}${selected.reason_code ? ` (${selected.reason_code})` : ''}`],
                ['Time', <Time key="t" value={selected.at_time} />],
                ['Actor', `${selected.actor_type} ${selected.actor_id ?? ''}`],
                ['Auth method', selected.auth_method ?? '—'],
                ['Target', selected.target_type ? `${selected.target_type} ${selected.target_id ?? ''}` : '—'],
                ['Source IP', selected.source_ip ?? '—'],
                ['Request ID', selected.request_id ? <Code key="r">{selected.request_id}</Code> : '—'],
              ]}
            />
            <Text size="sm" fw={600} mt="md" mb={4}>
              Details
            </Text>
            <Code block>{JSON.stringify(selected.details ?? {}, null, 2)}</Code>
          </>
        ) : null}
      </Drawer>
    </>
  )
}
