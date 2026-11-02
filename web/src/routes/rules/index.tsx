import { Badge, Card, Text } from '@mantine/core'
import { IconPlus } from '@tabler/icons-react'
import { useQuery } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo } from 'react'
import { api, unwrap, type Rule } from '@/api/client'
import { DataTable, Dim } from '@/components/DataTable'
import { describeRule } from '@/components/domain'
import { AnchorLink, ButtonLink } from '@/components/links'
import { ruleKinds } from '@/components/RuleForm'
import { EmptyBlock, PageHeader, QueryState, StatusBadge, Time } from '@/components/ui'
import { fmtNumber } from '@/lib/format'
import { useCan } from '@/lib/session'

export const Route = createFileRoute('/rules/')({ component: Rules })

const severityColor: Record<string, string> = { info: 'blue', warning: 'yellow', critical: 'red' }

function Rules() {
  const can = useCan()
  const navigate = useNavigate()
  const list = useQuery({ queryKey: ['rules'], queryFn: async () => unwrap(await api.GET('/rules')) })
  const columns = useMemo<ColumnDef<Rule>[]>(
    () => [
      {
        header: 'Rule',
        accessorKey: 'name',
        cell: ({ row }) => (
          <div>
            <AnchorLink to="/rules/$ruleId" params={{ ruleId: row.original.id }} fw={600} size="sm">
              {row.original.name}
            </AnchorLink>
            <Text size="xs" c="dimmed">
              {describeRule(row.original.definition)}
            </Text>
          </div>
        ),
      },
      {
        header: 'Type',
        accessorFn: (r) => ruleKinds.find((k) => k.value === r.definition.kind)?.label ?? r.definition.kind,
      },
      {
        header: 'Severity',
        accessorFn: (r) => r.definition.severity,
        cell: ({ getValue }) => (
          <Badge variant="light" color={severityColor[getValue() as string] ?? 'gray'}>
            {getValue() as string}
          </Badge>
        ),
      },
      {
        header: 'State',
        accessorFn: (r) => (r.enabled ? 'enabled' : 'disabled'),
        cell: ({ getValue }) => <StatusBadge state={getValue() as string} />,
      },
      { header: 'Revision', accessorKey: 'revision' },
      {
        header: 'Events 24 h',
        accessorFn: (r) => r.stats?.events_24h ?? 0,
        cell: ({ getValue }) => <span className="wp-num">{fmtNumber(getValue() as number)}</span>,
      },
      {
        header: 'Last event',
        accessorFn: (r) => r.stats?.last_event_at ?? '',
        cell: ({ row }) =>
          row.original.stats?.last_event_at ? (
            <Time value={row.original.stats.last_event_at} relative />
          ) : (
            <Dim>never</Dim>
          ),
      },
    ],
    [],
  )
  return (
    <>
      <PageHeader
        title="Rules"
        description="Rules turn observations into events. Events are routed to adapters on the Routing page."
        actions={
          can('rules.write') ? (
            <ButtonLink to="/rules/new" leftSection={<IconPlus size={16} />}>
              New rule
            </ButtonLink>
          ) : null
        }
      />
      <Card p={0}>
        <QueryState query={list} loadingLabel="Loading rules…">
          {() => (
            <DataTable
              caption="Rules"
              testId="rules-table"
              data={(list.data ?? []).filter((r) => !r.deleted)}
              columns={columns}
              onRowClick={(r) => void navigate({ to: '/rules/$ruleId', params: { ruleId: r.id } })}
              empty={
                <EmptyBlock
                  title="No rules"
                  description="Create a rule, for example “battery below 20 %”, then route its events to an adapter."
                />
              }
            />
          )}
        </QueryState>
      </Card>
    </>
  )
}
