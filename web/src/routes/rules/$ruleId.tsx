import { Badge, Button, Code, Grid, Group, SimpleGrid, Table, Tabs, Text } from '@mantine/core'
import { IconPencil, IconTrash } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, check, ifMatch, unwrap, type Definition } from '@/api/client'
import { ConfirmDialog } from '@/components/Confirm'
import { DeliveryCounts, describeRule } from '@/components/domain'
import { AnchorLink } from '@/components/links'
import { kindEvents, ruleKinds, RuleForm } from '@/components/RuleForm'
import { RulePreview } from '@/components/RulePreview'
import {
  DefinitionList,
  EmptyBlock,
  ErrorBlock,
  Kpi,
  LoadingBlock,
  PageHeader,
  QueryState,
  Section,
  StatusBadge,
  Time,
} from '@/components/ui'
import { fmtDuration, fmtNumber } from '@/lib/format'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'
import { signalLabel } from '@/lib/signals'

export const Route = createFileRoute('/rules/$ruleId')({ component: RuleDetail })

function RuleDetail() {
  const { ruleId } = Route.useParams()
  const can = useCan()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [draft, setDraft] = useState<Definition | null>(null)
  const q = useQuery({
    queryKey: ['rule', ruleId],
    queryFn: async () => unwrap(await api.GET('/rules/{id}', { params: { path: { id: ruleId } } })),
  })
  const revisions = useQuery({
    queryKey: ['rule', ruleId, 'revisions'],
    queryFn: async () =>
      unwrap(await api.GET('/rules/{id}/revisions', { params: { path: { id: ruleId } } })) as {
        revision: number
        created_by: string
        created_at: string
        definition: Definition
      }[],
  })
  const events = useQuery({
    queryKey: ['events', 'rule', ruleId],
    queryFn: async () => unwrap(await api.GET('/events', { params: { query: { rule: ruleId, limit: 25 } } })),
    enabled: can('events.read'),
  })
  const vehicles = useQuery({
    queryKey: ['vehicles', 'list', false],
    queryFn: async () => unwrap(await api.GET('/vehicles', { params: { query: { limit: 200 } } })),
  })
  const del = useMutation({
    mutationFn: async () => {
      check(await api.DELETE('/rules/{id}', { params: { path: { id: ruleId } } }))
    },
    onSuccess: async () => {
      notifySuccess('Rule deleted; its events and revisions are kept')
      await qc.invalidateQueries({ queryKey: ['rules'] })
      void navigate({ to: '/rules' })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  if (q.isPending) return <LoadingBlock h={320} />
  if (q.isError) return <ErrorBlock error={q.error} onRetry={() => void q.refetch()} />
  const r = q.data
  const d = r.definition
  const vName = (id: string) => vehicles.data?.items.find((v) => v.id === id)?.display_name ?? id

  if (editing) {
    return (
      <>
        <PageHeader
          title={`Edit ${r.name}`}
          description="Saving creates a new revision. Earlier events keep the revision that produced them."
        />
        <Grid>
          <Grid.Col span={{ base: 12, lg: 7 }}>
            <Section title="Definition" padded>
              <RuleForm
                initial={r}
                submitLabel="Save as new revision"
                onDefinitionChange={setDraft}
                onCancel={() => {
                  setEditing(false)
                }}
                onSubmit={async (body) => {
                  unwrap(
                    await api.PUT('/rules/{id}', {
                      params: { path: { id: r.id } },
                      body,
                      headers: ifMatch(r.etag_revision),
                    }),
                  )
                  await Promise.all([
                    qc.invalidateQueries({ queryKey: ['rule', ruleId] }),
                    qc.invalidateQueries({ queryKey: ['rules'] }),
                  ])
                  setEditing(false)
                  notifySuccess('Rule saved')
                }}
              />
            </Section>
          </Grid.Col>
          <Grid.Col span={{ base: 12, lg: 5 }}>
            <Section title="Preview" padded>
              <RulePreview definition={draft ?? d} />
            </Section>
          </Grid.Col>
        </Grid>
      </>
    )
  }

  return (
    <>
      <PageHeader
        title={
          <Group gap="sm">
            <span>{r.name}</span>
            <StatusBadge state={r.enabled ? 'enabled' : 'disabled'} />
            <Badge variant="light" color="gray">
              revision {r.revision}
            </Badge>
          </Group>
        }
        description={r.description || describeRule(d)}
        actions={
          can('rules.write') ? (
            <Group gap="xs">
              <Button
                variant="default"
                leftSection={<IconPencil size={16} />}
                onClick={() => {
                  setEditing(true)
                }}
              >
                Edit
              </Button>
              <Button
                variant="default"
                color="red"
                leftSection={<IconTrash size={16} />}
                onClick={() => {
                  setDeleting(true)
                }}
              >
                Delete
              </Button>
            </Group>
          ) : null
        }
      />
      <SimpleGrid cols={{ base: 2, md: 4 }} mb="lg">
        <Kpi label="Events (24 h)" value={fmtNumber(r.stats?.events_24h ?? 0)} />
        <Kpi label="Events (all)" value={fmtNumber(r.stats?.events ?? 0)} />
        <Kpi
          label="Last event"
          value={r.stats?.last_event_at ? <Time value={r.stats.last_event_at} relative /> : 'never'}
        />
        <Kpi
          label="Tracked series"
          value={fmtNumber(r.stats?.tracked_states ?? 0)}
          hint="vehicle × signal states held for evaluation"
        />
      </SimpleGrid>
      <Tabs defaultValue="definition" keepMounted={false}>
        <Tabs.List mb="md">
          <Tabs.Tab value="definition">Definition</Tabs.Tab>
          <Tabs.Tab value="preview">Preview</Tabs.Tab>
          {can('events.read') ? <Tabs.Tab value="events">Recent events</Tabs.Tab> : null}
          <Tabs.Tab value="revisions">Revisions</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="definition">
          <Section title="Definition">
            <DefinitionList
              items={[
                ['Type', ruleKinds.find((k) => k.value === d.kind)?.label ?? d.kind],
                [
                  'Emits',
                  kindEvents[d.kind]
                    ?.filter((t) => (d.notify_clear ?? false) || !['threshold.cleared', 'data.resumed'].includes(t))
                    .join(', '),
                ],
                ['Condition', describeRule(d).split(' · ')[0]],
                ['Severity', d.severity],
                [
                  'Signals',
                  d.signal_keys.length ? <SignalNames key="s" keys={d.signal_keys} /> : 'any signal of the vehicle',
                ],
                ['Vehicles', d.vehicle_ids.length ? d.vehicle_ids.map(vName).join(', ') : 'all vehicles'],
                ['Debounce', `${String(d.debounce_count ?? 1)} observation(s)`],
                ['Cooldown', d.cooldown_seconds ? fmtDuration(d.cooldown_seconds) : 'none'],
                ['Created', <Time key="c" value={r.created_at} />],
                ['Updated', <Time key="u" value={r.updated_at} />],
              ]}
            />
          </Section>
        </Tabs.Panel>
        <Tabs.Panel value="preview">
          <Section title="Preview against history" padded>
            <RulePreview definition={d} />
          </Section>
        </Tabs.Panel>
        <Tabs.Panel value="events">
          <Section title="Recent events">
            <QueryState query={events}>
              {() =>
                events.data?.items.length ? (
                  <Table className="wp-table">
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Event</Table.Th>
                        <Table.Th>Vehicle</Table.Th>
                        <Table.Th>Observed</Table.Th>
                        <Table.Th>Deliveries</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {events.data.items.map((e) => (
                        <Table.Tr key={e.id}>
                          <Table.Td>
                            <AnchorLink to="/events/$eventId" params={{ eventId: e.id }} size="sm">
                              {e.type}
                            </AnchorLink>
                            {e.rule_revision !== r.revision ? (
                              <Badge size="xs" ml={6} variant="light" color="gray">
                                rev {e.rule_revision}
                              </Badge>
                            ) : null}
                          </Table.Td>
                          <Table.Td>{e.vehicle_id ? vName(e.vehicle_id) : '—'}</Table.Td>
                          <Table.Td>
                            <Time value={e.observed_at} />
                          </Table.Td>
                          <Table.Td>
                            <DeliveryCounts d={e.deliveries} />
                          </Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                ) : (
                  <EmptyBlock title="No events yet" />
                )
              }
            </QueryState>
          </Section>
        </Tabs.Panel>
        <Tabs.Panel value="revisions">
          <Section
            title="Revisions"
            description="Revisions are immutable. Each event records the revision that produced it."
          >
            <QueryState query={revisions}>
              {() => (
                <Table className="wp-table">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>Revision</Table.Th>
                      <Table.Th>Created</Table.Th>
                      <Table.Th>By</Table.Th>
                      <Table.Th>Definition</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {(revisions.data ?? []).map((rev) => (
                      <Table.Tr key={rev.revision}>
                        <Table.Td>{rev.revision}</Table.Td>
                        <Table.Td>
                          <Time value={rev.created_at} />
                        </Table.Td>
                        <Table.Td>
                          <Code>{rev.created_by}</Code>
                        </Table.Td>
                        <Table.Td>
                          <Text size="sm">{describeRule(rev.definition)}</Text>
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              )}
            </QueryState>
          </Section>
        </Tabs.Panel>
      </Tabs>
      <ConfirmDialog
        opened={deleting}
        onClose={() => {
          setDeleting(false)
        }}
        onConfirm={() => {
          del.mutate()
        }}
        loading={del.isPending}
        title="Delete rule?"
        confirmLabel="Delete rule"
        danger
      >
        The rule stops creating events. Events it already created, their deliveries and its revisions are kept. Routes
        that select this rule by ID stop matching new events.
      </ConfirmDialog>
    </>
  )
}

function SignalNames({ keys }: { keys: string[] }) {
  const signals = useQuery({
    queryKey: ['signals', 'seen'],
    queryFn: async () => unwrap(await api.GET('/signals', { params: { query: { only_seen: true, limit: 200 } } })),
  })
  const name = (k: string) => {
    const s = signals.data?.items.find((x) => x.key === k)
    return s ? signalLabel(s) : undefined
  }
  return (
    <Group gap={4}>
      {keys.map((k) => (
        <AnchorLink key={k} to="/signals" search={{ key: k }} size="sm">
          {name(k) ?? k}
        </AnchorLink>
      ))}
    </Group>
  )
}
