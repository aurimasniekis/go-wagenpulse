import { Badge, Button, Code, Grid, Group, ScrollArea, Table, Text } from '@mantine/core'
import { IconRefresh } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { api, unwrap } from '@/api/client'
import { AnchorLink } from '@/components/links'
import {
  DefinitionList,
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  PageHeader,
  QueryState,
  Section,
  StatusBadge,
  Time,
} from '@/components/ui'
import { fmtValue, maskVIN } from '@/lib/format'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'

export const Route = createFileRoute('/events/$eventId')({ component: EventDetail })

/** Masks the VIN in a displayed payload; the stored payload is unchanged. */
function redactPayload(p: unknown): unknown {
  if (!p || typeof p !== 'object') return p
  const copy = structuredClone(p) as { vehicle?: { vin?: string } }
  if (copy.vehicle?.vin) copy.vehicle.vin = maskVIN(copy.vehicle.vin)
  return copy
}

function EventDetail() {
  const { eventId } = Route.useParams()
  const can = useCan()
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['event', eventId],
    queryFn: async () => unwrap(await api.GET('/events/{id}', { params: { path: { id: eventId } } })),
  })
  const deliveries = useQuery({
    queryKey: ['deliveries', 'event', eventId],
    queryFn: async () => unwrap(await api.GET('/deliveries', { params: { query: { event: eventId, limit: 100 } } })),
    refetchInterval: (x) =>
      x.state.data?.items.some((d) => d.state !== 'succeeded' && d.state !== 'dead') ? 3000 : false,
  })
  const retry = useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST('/deliveries/{id}/retry', { params: { path: { id } } })),
    onSuccess: async () => {
      notifySuccess('Delivery queued for another attempt')
      await qc.invalidateQueries({ queryKey: ['deliveries'] })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  if (q.isPending) return <LoadingBlock h={320} />
  if (q.isError) return <ErrorBlock error={q.error} onRetry={() => void q.refetch()} />
  const e = q.data
  const p = e.payload
  return (
    <>
      <PageHeader
        title={
          <Group gap="sm">
            <span>{e.type}</span>
            {e.backfill ? (
              <Badge variant="light" color="gray">
                backfill
              </Badge>
            ) : null}
            {p?.severity ? (
              <Badge
                variant="light"
                color={p.severity === 'critical' ? 'red' : p.severity === 'warning' ? 'yellow' : 'blue'}
              >
                {p.severity}
              </Badge>
            ) : null}
          </Group>
        }
        description={
          <span>
            Observed <Time value={e.observed_at} /> · created <Time value={e.created_at} />
          </span>
        }
      />
      <Grid>
        <Grid.Col span={{ base: 12, lg: 6 }}>
          <Section title="Event">
            <DefinitionList
              items={[
                ['Event ID', <Code key="id">{e.id}</Code>],
                [
                  'Vehicle',
                  e.vehicle_id ? (
                    <AnchorLink key="v" to="/vehicles/$vehicleId" params={{ vehicleId: e.vehicle_id }} size="sm">
                      {p?.vehicle.name ?? e.vehicle_id}
                    </AnchorLink>
                  ) : (
                    '—'
                  ),
                ],
                [
                  'Rule',
                  e.rule_id ? (
                    <AnchorLink key="r" to="/rules/$ruleId" params={{ ruleId: e.rule_id }} size="sm">
                      {p?.rule?.name ?? e.rule_id} (revision {e.rule_revision})
                    </AnchorLink>
                  ) : (
                    '—'
                  ),
                ],
                ['Signal', p?.signal?.name ?? e.signal_key ?? '—'],
                ['Value', p?.value ? fmtValue(p.value) : '—'],
                ['Previous', p?.previous ? fmtValue(p.previous) : '—'],
                ...(p?.threshold
                  ? ([
                      [
                        'Threshold',
                        `${p.threshold.direction} ${String(p.threshold.value)}${p.threshold.hysteresis ? ` ±${String(p.threshold.hysteresis)}` : ''}`,
                      ],
                    ] as [string, string][])
                  : []),
                [
                  'Import',
                  e.import_id ? (
                    <AnchorLink key="i" to="/imports/$importId" params={{ importId: e.import_id }} size="sm">
                      {e.import_id}
                    </AnchorLink>
                  ) : (
                    '—'
                  ),
                ],
                ['Correlation ID', <Code key="c">{e.correlation_id}</Code>],
              ]}
            />
          </Section>
        </Grid.Col>
        <Grid.Col span={{ base: 12, lg: 6 }}>
          <Section
            title="Payload (envelope v1)"
            description={
              p?.vehicle.vin
                ? 'The VIN is masked here; adapters configured to include it receive it in full.'
                : undefined
            }
          >
            <ScrollArea h={380} px="lg" pb="md">
              <Code block data-testid="event-payload">
                {JSON.stringify(redactPayload(p), null, 2)}
              </Code>
            </ScrollArea>
          </Section>
        </Grid.Col>
      </Grid>
      <Section title="Deliveries" description="One per adapter with a matching route (plus replays).">
        <QueryState query={deliveries}>
          {() =>
            deliveries.data?.items.length ? (
              <Table className="wp-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Adapter</Table.Th>
                    <Table.Th>State</Table.Th>
                    <Table.Th>Attempts</Table.Th>
                    <Table.Th>Subject</Table.Th>
                    <Table.Th />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {deliveries.data.items.map((d) => (
                    <Table.Tr key={d.id}>
                      <Table.Td>
                        <AnchorLink to="/deliveries/$deliveryId" params={{ deliveryId: d.id }} size="sm">
                          {d.adapter_name}
                        </AnchorLink>
                        {d.replay_seq > 0 ? (
                          <Badge size="xs" ml={6} variant="light" color="grape">
                            replay {d.replay_seq}
                          </Badge>
                        ) : null}
                      </Table.Td>
                      <Table.Td>
                        <StatusBadge state={d.state} />
                      </Table.Td>
                      <Table.Td className="wp-num">
                        {d.attempts}/{d.max_attempts}
                      </Table.Td>
                      <Table.Td>
                        <Text size="xs" ff="monospace">
                          {d.subject}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        {can('deliveries.retry') && (d.state === 'dead' || d.state === 'retrying') ? (
                          <Button
                            size="xs"
                            variant="default"
                            leftSection={<IconRefresh size={14} />}
                            loading={retry.isPending && retry.variables === d.id}
                            onClick={() => {
                              retry.mutate(d.id)
                            }}
                          >
                            Retry now
                          </Button>
                        ) : null}
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            ) : (
              <EmptyBlock
                title="Not routed"
                description="No enabled route matched this event when it was created, so it was stored but not delivered."
              />
            )
          }
        </QueryState>
      </Section>
    </>
  )
}
