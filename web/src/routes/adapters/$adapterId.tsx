import { Alert, Button, Code, Group, Modal, SimpleGrid, Stack, Switch, Text } from '@mantine/core'
import { IconPencil, IconPlugConnected, IconTrash } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, check, ifMatch, unwrap } from '@/api/client'
import { AdapterForm } from '@/components/AdapterForm'
import { ConfirmDialog } from '@/components/Confirm'
import { AnchorLink, ButtonLink } from '@/components/links'
import { DefinitionList, ErrorBlock, Kpi, LoadingBlock, PageHeader, Section, StatusBadge, Time } from '@/components/ui'
import { fmtNumber } from '@/lib/format'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'

export const Route = createFileRoute('/adapters/$adapterId')({ component: AdapterDetail })

function AdapterDetail() {
  const { adapterId } = Route.useParams()
  const can = useCan()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [abandon, setAbandon] = useState(false)
  const q = useQuery({
    queryKey: ['adapter', adapterId],
    queryFn: async () => unwrap(await api.GET('/adapters/{id}', { params: { path: { id: adapterId } } })),
    refetchInterval: 10_000,
  })
  const routes = useQuery({
    queryKey: ['routes', adapterId],
    queryFn: async () => unwrap(await api.GET('/routes', { params: { query: { adapter: adapterId } } })),
    enabled: can('routes.read'),
  })
  const test = useMutation({
    mutationFn: async () => unwrap(await api.POST('/adapters/{id}/test', { params: { path: { id: adapterId } } })),
    onError: (e) => {
      notifyError(e)
    },
  })
  const del = useMutation({
    mutationFn: async () => {
      check(
        await api.DELETE('/adapters/{id}', {
          params: { path: { id: adapterId }, query: { abandon_pending: abandon } },
        }),
      )
    },
    onSuccess: async () => {
      notifySuccess('Adapter deleted')
      await qc.invalidateQueries({ queryKey: ['adapters'] })
      void navigate({ to: '/adapters', search: {} })
    },
    onError: (e) => {
      notifyError(e, 'Could not delete the adapter')
    },
  })
  if (q.isPending) return <LoadingBlock h={320} />
  if (q.isError) return <ErrorBlock error={q.error} onRetry={() => void q.refetch()} />
  const a = q.data
  const st = a.status
  const qu = a.queue
  const t = test.data
  const inFlight = (qu?.pending ?? 0) + (qu?.retrying ?? 0)
  return (
    <>
      <PageHeader
        title={
          <Group gap="sm">
            <span>{a.name}</span>
            <StatusBadge state={a.enabled ? (st?.state ?? 'connecting') : 'disabled'} />
          </Group>
        }
        description={
          <Text ff="monospace" size="sm" span>
            {a.config.url}
          </Text>
        }
        actions={
          can('adapters.write') ? (
            <Group gap="xs">
              <Button
                leftSection={<IconPlugConnected size={16} />}
                loading={test.isPending}
                onClick={() => {
                  test.mutate()
                }}
                disabled={!a.enabled}
              >
                Test connection
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
      {t ? (
        <Alert
          color={t.ok ? 'green' : 'red'}
          variant="light"
          mb="lg"
          title={t.ok ? 'Test event stored by JetStream' : 'Test failed'}
          withCloseButton
          onClose={() => {
            test.reset()
          }}
        >
          <Stack gap={4}>
            <Text size="sm">{t.meaning}</Text>
            <Text size="sm">
              Subject <Code>{t.subject}</Code> · {t.duration_ms} ms
              {t.ok && t.stream ? ` · stream ${t.stream} seq ${String(t.sequence ?? '')}` : ''}
            </Text>
            {t.error ? (
              <Text size="sm">
                {t.error_class ? <Code>{t.error_class}</Code> : null} {t.error}
              </Text>
            ) : null}
            {t.hint ? (
              <Text size="sm" fw={500}>
                {t.hint}
              </Text>
            ) : null}
          </Stack>
        </Alert>
      ) : null}
      {st?.reason && st.state !== 'connected' ? (
        <Alert color="orange" variant="light" mb="lg" title="Connection problem">
          {st.reason}
          {st.last_error_class ? (
            <Text size="sm" mt={4}>
              Class <Code>{st.last_error_class}</Code> at <Time value={st.last_error_at} />
            </Text>
          ) : null}
        </Alert>
      ) : null}
      <SimpleGrid cols={{ base: 2, md: 4 }} mb="lg">
        <Kpi label="Pending" value={fmtNumber(qu?.pending ?? 0)} />
        <Kpi
          label="Retrying"
          value={fmtNumber(qu?.retrying ?? 0)}
          color={(qu?.retrying ?? 0) > 0 ? 'var(--wp-warning)' : undefined}
        />
        <Kpi
          label="Dead"
          value={fmtNumber(qu?.dead ?? 0)}
          color={(qu?.dead ?? 0) > 0 ? 'var(--wp-danger)' : undefined}
          hint={
            (qu?.dead ?? 0) > 0 ? (
              <AnchorLink to="/events" search={{ tab: 'deliveries', adapter: a.id, state: 'dead' }} size="xs">
                Review dead deliveries
              </AnchorLink>
            ) : undefined
          }
        />
        <Kpi
          label="Delivered (24 h)"
          value={fmtNumber(qu?.succeeded_24h ?? 0)}
          hint={
            st?.last_ok_at ? (
              <>
                last <Time value={st.last_ok_at} relative />
              </>
            ) : undefined
          }
        />
      </SimpleGrid>
      <SimpleGrid cols={{ base: 1, lg: 2 }}>
        <Section title="Configuration">
          <DefinitionList
            items={[
              ['Subject template', <Code key="t">{a.config.subject_template}</Code>],
              ['Subject domain', <Code key="d">{a.config.subject_domain}</Code>],
              ['CloudEvents source', a.config.source || 'wagenpulse/<installation>'],
              [
                'Credentials',
                a.creds_ref ? (
                  <Code key="r">{a.creds_ref}</Code>
                ) : a.creds_set ? (
                  `stored (encrypted)${a.secret_fingerprint ? ` · ${a.secret_fingerprint}` : ''}`
                ) : (
                  'none'
                ),
              ],
              ['Private CA', a.config.ca_pem ? 'configured' : 'system trust store'],
              ['VIN in payload', a.config.include_vin ? 'included' : 'excluded'],
              ['Transport', a.config.allow_insecure_transport ? 'unencrypted allowed' : 'TLS required'],
              ['Publish timeout', `${String(a.config.publish_timeout_ms)} ms`],
              ['Max attempts', String(a.config.max_attempts)],
              [
                'Last test',
                st?.last_test_at ? (
                  <span key="lt">
                    <Time value={st.last_test_at} /> · {st.last_test_result}
                  </span>
                ) : (
                  'never'
                ),
              ],
            ]}
          />
        </Section>
        {can('routes.read') ? (
          <Section
            title="Routes to this adapter"
            actions={
              can('routes.write') ? (
                <ButtonLink to="/routing" search={{ create: 1, adapter: a.id }} size="xs" variant="default">
                  Add route
                </ButtonLink>
              ) : null
            }
          >
            <Stack gap={0} px="lg" pb="md">
              {(routes.data ?? []).length === 0 ? (
                <Text size="sm" c="dimmed">
                  No route sends events here yet. Events are delivered only to adapters with a matching route.
                </Text>
              ) : (
                (routes.data ?? []).map((r) => (
                  <Group
                    key={r.id}
                    justify="space-between"
                    py={6}
                    style={{ borderBottom: '1px solid var(--wp-border)' }}
                  >
                    <AnchorLink to="/routing" search={{ edit: r.id }} size="sm">
                      {r.name}
                    </AnchorLink>
                    <StatusBadge state={r.enabled ? 'enabled' : 'disabled'} />
                  </Group>
                ))
              )}
            </Stack>
          </Section>
        ) : null}
      </SimpleGrid>
      <Text size="xs" c="dimmed" mt="lg">
        “Delivered” means JetStream acknowledged and stored the event. Pulse does not report whether a consumer
        processed it.
      </Text>
      <Modal
        opened={editing}
        onClose={() => {
          setEditing(false)
        }}
        title={<Text fw={650}>Edit adapter</Text>}
        size="xl"
      >
        <AdapterForm
          initial={a}
          submitLabel="Save changes"
          onCancel={() => {
            setEditing(false)
          }}
          onSubmit={async (body) => {
            unwrap(
              await api.PUT('/adapters/{id}', { params: { path: { id: a.id } }, body, headers: ifMatch(a.revision) }),
            )
            await qc.invalidateQueries({ queryKey: ['adapter', adapterId] })
            await qc.invalidateQueries({ queryKey: ['adapters'] })
            setEditing(false)
            notifySuccess('Adapter saved; it reconnects with the new settings')
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
        title="Delete adapter?"
        confirmLabel="Delete adapter"
        danger
        typeToConfirm={a.name}
      >
        <Stack gap="sm">
          <Text size="sm">Routes to this adapter are deleted with it. Delivery history is kept.</Text>
          {inFlight > 0 ? (
            <>
              <Alert color="yellow" variant="light">
                {inFlight} deliveries are still pending or retrying. Deletion is refused unless you abandon them;
                abandoned deliveries are marked dead.
              </Alert>
              <Switch
                label="Abandon pending deliveries"
                checked={abandon}
                onChange={(e) => {
                  setAbandon(e.currentTarget.checked)
                }}
              />
            </>
          ) : null}
        </Stack>
      </ConfirmDialog>
    </>
  )
}
