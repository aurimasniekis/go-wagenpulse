import { Alert, Badge, Button, Code, Group, SimpleGrid, Table, Text } from '@mantine/core'
import { IconRefresh } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import { api, unwrap } from '@/api/client'
import { AnchorLink } from '@/components/links'
import {
  DefinitionList,
  EmptyBlock,
  ErrorBlock,
  Kpi,
  LoadingBlock,
  PageHeader,
  Section,
  StatusBadge,
  Time,
} from '@/components/ui'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'

export const Route = createFileRoute('/deliveries/$deliveryId')({ component: DeliveryDetail })

const errorHelp: Record<string, string> = {
  publish_timeout:
    'No acknowledgement within the publish timeout. With Pulse this usually means the credentials lack a publish grant for the subject.',
  permission_denied:
    'The server refused the publish. Check that the adapter credentials grant publish on this subject.',
  credentials_invalid: 'The credentials were rejected. Update the adapter credentials.',
  nats_unavailable: 'The NATS server is unreachable. Delivery resumes automatically when it is back.',
  no_stream: 'No JetStream stream captures the subject. Check the subject template and the Pulse stream configuration.',
  invalid_subject: 'The rendered subject is not a valid Pulse subject. Fix the subject template.',
  payload_too_large: 'The event exceeds the server payload limit.',
  destination_blocked:
    'The adapter address is blocked by the outbound network policy (outbound.allow_cidrs / deny_cidrs).',
  publish_rejected: 'JetStream rejected the message.',
}

function DeliveryDetail() {
  const { deliveryId } = Route.useParams()
  const can = useCan()
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['delivery', deliveryId],
    queryFn: async () => unwrap(await api.GET('/deliveries/{id}', { params: { path: { id: deliveryId } } })),
    refetchInterval: (x) => (x.state.data && !['succeeded', 'dead'].includes(x.state.data.state) ? 3000 : false),
  })
  const retry = useMutation({
    mutationFn: async () => unwrap(await api.POST('/deliveries/{id}/retry', { params: { path: { id: deliveryId } } })),
    onSuccess: async () => {
      notifySuccess('Delivery queued for another attempt')
      await qc.invalidateQueries({ queryKey: ['delivery', deliveryId] })
      await qc.invalidateQueries({ queryKey: ['deliveries'] })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  if (q.isPending) return <LoadingBlock h={320} />
  if (q.isError) return <ErrorBlock error={q.error} onRetry={() => void q.refetch()} />
  const d = q.data
  const history = d.attempt_history ?? []
  return (
    <>
      <PageHeader
        title={
          <Group gap="sm">
            <span>Delivery to {d.adapter_name}</span>
            <StatusBadge state={d.state} />
            {d.replay_seq > 0 ? (
              <Badge variant="light" color="grape">
                replay {d.replay_seq}
              </Badge>
            ) : null}
          </Group>
        }
        description={
          <span>
            {d.event_type} ·{' '}
            <AnchorLink to="/events/$eventId" params={{ eventId: d.event_id }} size="sm">
              view event
            </AnchorLink>
          </span>
        }
        actions={
          can('deliveries.retry') && (d.state === 'dead' || d.state === 'retrying') ? (
            <Button
              leftSection={<IconRefresh size={16} />}
              loading={retry.isPending}
              onClick={() => {
                retry.mutate()
              }}
            >
              Retry now
            </Button>
          ) : null
        }
      />
      {d.state === 'dead' ? (
        <Alert color="red" variant="light" mb="lg" title="Delivery gave up">
          {d.dead_reason === 'adapter_deleted'
            ? 'The adapter was deleted while this delivery was pending.'
            : (d.last_error ?? d.dead_reason ?? 'All attempts failed.')}
          {d.last_error_class && errorHelp[d.last_error_class] ? (
            <Text size="sm" mt={4}>
              {errorHelp[d.last_error_class]}
            </Text>
          ) : null}
        </Alert>
      ) : d.state === 'retrying' && d.last_error_class ? (
        <Alert color="yellow" variant="light" mb="lg" title="Retrying">
          {errorHelp[d.last_error_class] ?? d.last_error}
        </Alert>
      ) : null}
      <SimpleGrid cols={{ base: 2, md: 4 }} mb="lg">
        <Kpi label="Attempts" value={`${String(d.attempts)} / ${String(d.max_attempts)}`} />
        <Kpi
          label="Next attempt"
          value={
            d.next_attempt_at && d.state !== 'succeeded' && d.state !== 'dead' ? (
              <Time value={d.next_attempt_at} relative />
            ) : (
              '—'
            )
          }
        />
        <Kpi
          label="Acknowledged"
          value={d.succeeded_at ? <Time value={d.succeeded_at} relative /> : '—'}
          hint={d.ack_duplicate ? 'JetStream reported a duplicate (already stored)' : undefined}
        />
        <Kpi label="Stream sequence" value={d.ack_seq ? String(d.ack_seq) : '—'} hint={d.ack_stream} />
      </SimpleGrid>
      <SimpleGrid cols={{ base: 1, lg: 2 }}>
        <Section title="Message">
          <DefinitionList
            items={[
              ['Subject', <Code key="s">{d.subject ?? '—'}</Code>],
              ['Nats-Msg-Id', <Code key="m">{d.msg_id}</Code>],
              ['Delivery ID', <Code key="id">{d.id}</Code>],
              ['Created', <Time key="c" value={d.created_at} />],
              ['Updated', <Time key="u" value={d.updated_at} />],
              ...(d.replay_of ? ([['Replay of', <Code key="r">{d.replay_of}</Code>]] as [string, ReactNode][]) : []),
            ]}
          />
        </Section>
        <Section title="Attempts">
          {history.length === 0 ? (
            <EmptyBlock title="No attempts yet" />
          ) : (
            <Table className="wp-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>#</Table.Th>
                  <Table.Th>Started</Table.Th>
                  <Table.Th>Outcome</Table.Th>
                  <Table.Th>Duration</Table.Th>
                  <Table.Th>Error</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {history.map((a) => (
                  <Table.Tr key={a.attempt}>
                    <Table.Td>{a.attempt}</Table.Td>
                    <Table.Td>
                      <Time value={a.started_at} />
                    </Table.Td>
                    <Table.Td>
                      <StatusBadge state={a.outcome === 'retry' ? 'retrying' : a.outcome} />
                    </Table.Td>
                    <Table.Td className="wp-num">{a.duration_ms} ms</Table.Td>
                    <Table.Td>
                      {a.error_class ? (
                        <Text size="xs">
                          <Code>{a.error_class}</Code> {a.error_message}
                        </Text>
                      ) : (
                        '—'
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          )}
        </Section>
      </SimpleGrid>
    </>
  )
}
