import { Alert, Badge, Button, Card, Code, Group, Modal, SimpleGrid, Table, Text } from '@mantine/core'
import { IconPencil, IconPlayerPlay, IconTrash } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, check, ifMatch, unwrap } from '@/api/client'
import { ConfirmDialog } from '@/components/Confirm'
import { SourceForm } from '@/components/SourceForm'
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
import { AnchorLink } from '@/components/links'

export const Route = createFileRoute('/sources/$sourceId')({ component: SourceDetail })

/** What each authorization/collection state means and what the operator can do about it. */
const stateGuidance: Record<string, { color: string; text: string }> = {
  configured: {
    color: 'blue',
    text: 'Not checked yet. The first collection run verifies the credentials and the active data request.',
  },
  authorized: { color: 'green', text: 'Signed in and an active data request was found. Collection runs on schedule.' },
  reauthorization_required: {
    color: 'red',
    text: 'The portal rejected the session or login (expired cookie, changed password, CAPTCHA, new terms or consent). Sign in to the portal in a browser, resolve any prompt, then update the secret.',
  },
  no_active_request: {
    color: 'yellow',
    text: 'No active continuous data request exists for this account. Create one in the portal in a browser; requests renew about every 12 months.',
  },
  rate_limited: {
    color: 'yellow',
    text: 'The portal asked WagenPulse to slow down. Collection resumes after the retry time.',
  },
  unavailable: { color: 'orange', text: 'The portal did not respond correctly. WagenPulse retries with backoff.' },
  unsupported: { color: 'gray', text: 'This brand or account type is not supported by the portal interface.' },
  disabled: { color: 'gray', text: 'The source is disabled; nothing is collected.' },
  error: {
    color: 'red',
    text: 'Collection failed with an unexpected error. See the reason below and the server log (by correlation ID).',
  },
}

function SourceDetail() {
  const { sourceId } = Route.useParams()
  const can = useCan()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const q = useQuery({
    queryKey: ['source', sourceId],
    queryFn: async () => unwrap(await api.GET('/sources/{id}', { params: { path: { id: sourceId } } })),
    refetchInterval: 15_000,
  })
  const imports = useQuery({
    queryKey: ['imports', 'source', sourceId],
    queryFn: async () => unwrap(await api.GET('/imports', { params: { query: { source: sourceId, limit: 20 } } })),
    enabled: can('imports.read'),
    refetchInterval: 15_000,
  })
  const vehicles = useQuery({
    queryKey: ['vehicles', 'list', false],
    queryFn: async () => unwrap(await api.GET('/vehicles', { params: { query: { limit: 200 } } })),
  })
  const collect = useMutation({
    mutationFn: async () => {
      check(await api.POST('/sources/{id}/collect', { params: { path: { id: sourceId } } }))
    },
    onSuccess: async () => {
      notifySuccess('Collection queued')
      await qc.invalidateQueries({ queryKey: ['source', sourceId] })
    },
    onError: (e) => {
      notifyError(e, 'Could not start collection')
    },
  })
  const del = useMutation({
    mutationFn: async () => {
      check(await api.DELETE('/sources/{id}', { params: { path: { id: sourceId } } }))
    },
    onSuccess: async () => {
      notifySuccess('Source deleted; its imported data is kept')
      await qc.invalidateQueries({ queryKey: ['sources'] })
      void navigate({ to: '/sources', search: {} })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  if (q.isPending) return <LoadingBlock h={320} />
  if (q.isError) return <ErrorBlock error={q.error} onRetry={() => void q.refetch()} />
  const s = q.data
  const st = s.status
  const state = s.enabled ? (st?.state ?? 'configured') : 'disabled'
  const guide = stateGuidance[state]
  const vName = (id: string) => vehicles.data?.items.find((v) => v.id === id)?.display_name ?? id
  return (
    <>
      <PageHeader
        title={
          <Group gap="sm">
            <span>{s.name}</span>
            <StatusBadge state={state} />
          </Group>
        }
        description={
          <span>
            {s.brand} portal · {s.country.toUpperCase()}/{s.language} ·{' '}
            <Badge variant="light" color="yellow" tt="none" size="sm">
              unofficial portal interface
            </Badge>
          </span>
        }
        actions={
          can('sources.write') ? (
            <Group gap="xs">
              <Button
                leftSection={<IconPlayerPlay size={16} />}
                loading={collect.isPending}
                disabled={!s.enabled}
                onClick={() => {
                  collect.mutate()
                }}
              >
                Collect now
              </Button>
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
      {guide ? (
        <Alert color={guide.color} variant="light" mb="lg" title={state.replace(/_/g, ' ')}>
          <Text size="sm">{guide.text}</Text>
          {st?.reason ? (
            <Text size="sm" mt={6}>
              Reason: <Code>{st.reason}</Code>
            </Text>
          ) : null}
          {st?.retry_after ? (
            <Text size="sm" mt={6}>
              Retry after <Time value={st.retry_after} />
            </Text>
          ) : null}
        </Alert>
      ) : null}
      <SimpleGrid cols={{ base: 2, md: 4 }} mb="lg">
        <Kpi
          label="Last success"
          value={st?.last_success_at ? <Time value={st.last_success_at} relative /> : 'never'}
        />
        <Kpi
          label="Newest delivery"
          value={st?.last_delivery_at ? <Time value={st.last_delivery_at} relative /> : '—'}
          hint={st?.lag_seconds !== undefined ? `Lag ${fmtDuration(st.lag_seconds)}` : undefined}
        />
        <Kpi
          label="Next run"
          value={s.enabled && st?.next_run_at ? <Time value={st.next_run_at} relative /> : '—'}
          hint={`Every ${String(s.interval_minutes)} min`}
        />
        <Kpi
          label="Consecutive failures"
          value={fmtNumber(st?.consecutive_failures ?? 0)}
          color={(st?.consecutive_failures ?? 0) > 0 ? 'var(--wp-warning)' : undefined}
          hint={st?.last_error_class ? `last: ${st.last_error_class}` : undefined}
        />
      </SimpleGrid>
      <SimpleGrid cols={{ base: 1, lg: 2 }}>
        <Section title="Configuration">
          <DefinitionList
            items={[
              ['Authentication', s.auth_mode === 'password' ? 'Scripted login' : 'Browser session cookie'],
              ['Account email', s.username_set ? 'stored (encrypted)' : '—'],
              [
                'Secret',
                s.secret_ref ? (
                  <Code key="r">{s.secret_ref}</Code>
                ) : s.secret_set ? (
                  `stored (encrypted)${s.secret_fingerprint ? ` · ${s.secret_fingerprint}` : ''}`
                ) : (
                  'not set'
                ),
              ],
              [
                'Collects',
                [s.collect_continuous ? 'continuous' : '', s.collect_historical ? 'historical' : '']
                  .filter(Boolean)
                  .join(' + ') || 'nothing',
              ],
              ['Events (new data)', s.live_event_policy],
              ['Events (historical)', s.historical_event_policy],
              [
                'Vehicles',
                s.vehicle_ids.length ? (
                  <Group key="v" gap={4}>
                    {s.vehicle_ids.map((id) => (
                      <AnchorLink key={id} to="/vehicles/$vehicleId" params={{ vehicleId: id }} size="sm">
                        {vName(id)}
                      </AnchorLink>
                    ))}
                  </Group>
                ) : (
                  'none'
                ),
              ],
              ['Last attempt', <Time key="a" value={st?.last_attempt_at} />],
              ['Updated', <Time key="u" value={s.updated_at} />],
            ]}
          />
        </Section>
        {can('imports.read') ? (
          <Section title="Recent collection runs">
            <QueryState query={imports}>
              {() =>
                imports.data?.items.length ? (
                  <Table className="wp-table">
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th>Run</Table.Th>
                        <Table.Th>State</Table.Th>
                        <Table.Th>Files</Table.Th>
                        <Table.Th>Accepted</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {imports.data.items.map((im) => (
                        <Table.Tr key={im.id}>
                          <Table.Td>
                            <AnchorLink to="/imports/$importId" params={{ importId: im.id }} size="sm">
                              <Time value={im.created_at} />
                            </AnchorLink>
                          </Table.Td>
                          <Table.Td>
                            <StatusBadge state={im.state} />
                          </Table.Td>
                          <Table.Td className="wp-num">{im.files_total}</Table.Td>
                          <Table.Td className="wp-num">{fmtNumber(im.observations_accepted)}</Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                ) : (
                  <EmptyBlock title="No runs yet" description="Runs that found new files appear here." />
                )
              }
            </QueryState>
          </Section>
        ) : null}
      </SimpleGrid>
      <Modal
        opened={editing}
        onClose={() => {
          setEditing(false)
        }}
        title={<Text fw={650}>Edit source</Text>}
        size="xl"
      >
        <SourceForm
          initial={s}
          submitLabel="Save changes"
          onCancel={() => {
            setEditing(false)
          }}
          onSubmit={async (body) => {
            unwrap(
              await api.PUT('/sources/{id}', { params: { path: { id: s.id } }, body, headers: ifMatch(s.revision) }),
            )
            await qc.invalidateQueries({ queryKey: ['source', sourceId] })
            await qc.invalidateQueries({ queryKey: ['sources'] })
            setEditing(false)
            notifySuccess('Source saved')
          }}
        />
      </Modal>
      <ConfirmDialog
        opened={deleting}
        onClose={() => {
          setDeleting(false)
        }}
        onConfirm={() => {
          del.mutate()
        }}
        loading={del.isPending}
        title="Delete source?"
        confirmLabel="Delete source"
        danger
        typeToConfirm={s.name}
      >
        Collection stops and the stored credentials are destroyed. Deliveries and telemetry already imported by this
        source are kept.
      </ConfirmDialog>
      <Card mt="lg" p="sm" bg="transparent" withBorder={false}>
        <Text size="xs" c="dimmed">
          WagenPulse identifies itself to the portal as WagenPulse and limits its request rate. It does not create or
          renew data requests and never bypasses CAPTCHA, consent or terms prompts.
        </Text>
      </Card>
    </>
  )
}
