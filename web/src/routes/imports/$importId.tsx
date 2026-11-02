import { Alert, Badge, Button, Card, Code, Group, List, Progress, SimpleGrid, Table, Text } from '@mantine/core'
import { IconAlertTriangle, IconPlayerStop, IconRefresh } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, check, unwrap } from '@/api/client'
import { ConfirmDialog } from '@/components/Confirm'
import {
  DefinitionList,
  EmptyBlock,
  ErrorBlock,
  Kpi,
  LoadingBlock,
  PageHeader,
  ScrollRegion,
  Section,
  StatusBadge,
  Time,
} from '@/components/ui'
import { fmtBytes, fmtNumber } from '@/lib/format'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'
import { AnchorLink } from '@/components/links'

export const Route = createFileRoute('/imports/$importId')({ component: ImportDetail })

const outcomeHelp: Record<string, string> = {
  duplicate: 'Identical content was imported before (matched by SHA-256).',
  unrecognized:
    'The file structure is not a known delivery format. It is kept so it can be reprocessed when support is added.',
  rejected: 'The file was refused (unsafe archive, VIN mismatch or invalid content).',
}

function ImportDetail() {
  const { importId } = Route.useParams()
  const can = useCan()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [confirmCancel, setConfirmCancel] = useState(false)
  const q = useQuery({
    queryKey: ['import', importId],
    queryFn: async () => unwrap(await api.GET('/imports/{id}', { params: { path: { id: importId } } })),
    refetchInterval: (x) => (x.state.data && ['queued', 'running'].includes(x.state.data.import.state) ? 2000 : false),
  })
  const cancel = useMutation({
    mutationFn: async () => {
      check(await api.POST('/imports/{id}/cancel', { params: { path: { id: importId } } }))
    },
    onSuccess: async () => {
      setConfirmCancel(false)
      notifySuccess('Cancellation requested; the import stops at the next checkpoint')
      await qc.invalidateQueries({ queryKey: ['import', importId] })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const retry = useMutation({
    mutationFn: async () => unwrap(await api.POST('/imports/{id}/retry', { params: { path: { id: importId } } })),
    onSuccess: async (im) => {
      await qc.invalidateQueries({ queryKey: ['imports'] })
      void navigate({ to: '/imports/$importId', params: { importId: im.id } })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  if (q.isPending) return <LoadingBlock h={320} />
  if (q.isError) return <ErrorBlock error={q.error} onRetry={() => void q.refetch()} />
  const { import: im, deliveries } = q.data
  const running = im.state === 'running' || im.state === 'queued'
  const pct = im.files_total ? Math.round((im.files_done / im.files_total) * 100) : 0
  return (
    <>
      <PageHeader
        title={
          <Group gap="sm">
            <span>Import</span>
            <StatusBadge state={im.state} />
            {im.backfill ? (
              <Badge variant="light" color="gray">
                backfill
              </Badge>
            ) : null}
          </Group>
        }
        description={
          <span>
            {im.kind} started <Time value={im.created_at} />
            {im.finished_at ? (
              <>
                {' '}
                · finished <Time value={im.finished_at} />
              </>
            ) : null}
          </span>
        }
        actions={
          can('imports.write') ? (
            running ? (
              <Button
                variant="default"
                color="red"
                leftSection={<IconPlayerStop size={16} />}
                onClick={() => {
                  setConfirmCancel(true)
                }}
              >
                Cancel
              </Button>
            ) : im.state === 'failed' || im.state === 'cancelled' || im.state === 'partial' ? (
              <Button
                variant="default"
                leftSection={<IconRefresh size={16} />}
                loading={retry.isPending}
                onClick={() => {
                  retry.mutate()
                }}
              >
                Retry
              </Button>
            ) : null
          ) : null
        }
      />
      {running ? (
        <Card mb="lg">
          <Group justify="space-between" mb={6}>
            <Text size="sm" fw={550}>
              {im.state === 'queued'
                ? 'Waiting for a worker…'
                : `Processing file ${String(Math.min(im.files_done + 1, im.files_total))} of ${String(im.files_total)}`}
            </Text>
            <Text size="sm" className="wp-num">
              {pct}%
            </Text>
          </Group>
          <Progress value={pct} animated size="md" aria-label="Import progress" />
        </Card>
      ) : null}
      {im.error_message ? (
        <Alert
          color="red"
          variant="light"
          icon={<IconAlertTriangle size={18} />}
          mb="lg"
          title={im.error_code || 'Import failed'}
        >
          {im.error_message}
        </Alert>
      ) : null}
      <SimpleGrid cols={{ base: 2, md: 4 }} mb="lg">
        <Kpi
          label="Observations accepted"
          value={fmtNumber(im.observations_accepted)}
          hint={`${fmtNumber(im.corrections)} corrections · ${fmtNumber(im.late_skipped)} late for rules`}
        />
        <Kpi
          label="Duplicates"
          value={fmtNumber(im.observations_duplicate)}
          hint={`${fmtNumber(im.deliveries_duplicate)} duplicate file(s)`}
        />
        <Kpi
          label="Problems"
          value={fmtNumber(im.observations_rejected + im.deliveries_rejected + im.deliveries_unrecognized)}
          color={
            im.observations_rejected + im.deliveries_rejected + im.deliveries_unrecognized > 0
              ? 'var(--wp-warning)'
              : undefined
          }
          hint={`${fmtNumber(im.deliveries_rejected)} rejected · ${fmtNumber(im.deliveries_unrecognized)} unrecognized · ${fmtNumber(im.observations_rejected)} invalid values`}
        />
        <Kpi
          label="Events"
          value={
            im.event_policy === 'none'
              ? 'off'
              : fmtNumber(im.event_policy === 'preview' ? im.events_previewed : im.events_created)
          }
          hint={
            im.event_policy === 'preview'
              ? 'previewed, not delivered'
              : im.event_policy === 'publish'
                ? 'published'
                : 'event policy: store only'
          }
        />
      </SimpleGrid>
      {im.unknown_signals > 0 ? (
        <Alert variant="light" color="blue" mb="lg">
          {im.unknown_signals} signal key(s) are not in any loaded dictionary. Their values are stored as received.{' '}
          <AnchorLink to="/signals" search={{ origin: 'observed' }}>
            View observed signals
          </AnchorLink>
        </Alert>
      ) : null}
      {im.warnings.length > 0 ? (
        <Alert variant="light" color="yellow" mb="lg" title="Warnings">
          <List size="sm">
            {im.warnings.map((w) => (
              <List.Item key={w}>{w}</List.Item>
            ))}
          </List>
        </Alert>
      ) : null}
      <Section
        title="Files"
        description="Each file is stored by content hash; identical files are recognized as duplicates whatever their name."
      >
        {deliveries.length === 0 ? (
          <EmptyBlock title={running ? 'No file processed yet' : 'No files'} />
        ) : (
          <ScrollRegion label="Imported files">
            <Table className="wp-table" miw={760}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>File</Table.Th>
                  <Table.Th>Outcome</Table.Th>
                  <Table.Th>Format</Table.Th>
                  <Table.Th>Accepted</Table.Th>
                  <Table.Th>Duplicates</Table.Th>
                  <Table.Th>Data time</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {deliveries.map((d) => (
                  <Table.Tr key={d.id}>
                    <Table.Td>
                      <Text size="sm" ff="monospace" lineClamp={1} maw={320}>
                        {d.original_name || d.sha256.slice(0, 16)}
                      </Text>
                      <Text size="xs" c="dimmed">
                        {fmtBytes(d.size)} · sha256 {d.sha256.slice(0, 12)}…
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <StatusBadge
                        state={d.outcome === 'duplicate' ? 'skipped' : (d.outcome ?? d.status)}
                        label={d.outcome ?? undefined}
                      />
                      {d.message || (d.outcome && outcomeHelp[d.outcome]) ? (
                        <Text size="xs" c="dimmed" mt={2} maw={320}>
                          {d.message || (d.outcome ? outcomeHelp[d.outcome] : '')}
                        </Text>
                      ) : null}
                      {d.fingerprint ? (
                        <Text size="xs" c="dimmed">
                          structure <Code>{d.fingerprint.slice(0, 16)}</Code>
                        </Text>
                      ) : null}
                    </Table.Td>
                    <Table.Td>{d.format}</Table.Td>
                    <Table.Td className="wp-num">{fmtNumber(d.accepted)}</Table.Td>
                    <Table.Td className="wp-num">{fmtNumber(d.duplicates)}</Table.Td>
                    <Table.Td>{d.time_from ? <Time value={d.time_to ?? d.time_from} /> : '—'}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </ScrollRegion>
        )}
      </Section>
      <Section title="Details">
        <DefinitionList
          items={[
            ['Import ID', <Code key="id">{im.id}</Code>],
            ['Event policy', im.event_policy],
            [
              'Vehicle',
              im.vehicle_id ? (
                <AnchorLink key="v" to="/vehicles/$vehicleId" params={{ vehicleId: im.vehicle_id }} size="sm">
                  {im.vehicle_id}
                </AnchorLink>
              ) : (
                'matched per file by VIN'
              ),
            ],
            [
              'Source',
              im.source_id ? (
                <AnchorLink key="s" to="/sources/$sourceId" params={{ sourceId: im.source_id }} size="sm">
                  {im.source_id}
                </AnchorLink>
              ) : (
                'upload'
              ),
            ],
            [
              'Data coverage',
              im.coverage_from ? (
                <span key="c">
                  <Time value={im.coverage_from} /> – <Time value={im.coverage_to} />
                </span>
              ) : (
                '—'
              ),
            ],
            ['Correlation ID', <Code key="cid">{im.correlation_id}</Code>],
          ]}
        />
      </Section>
      <ConfirmDialog
        opened={confirmCancel}
        onClose={() => {
          setConfirmCancel(false)
        }}
        onConfirm={() => {
          cancel.mutate()
        }}
        loading={cancel.isPending}
        title="Cancel import?"
        confirmLabel="Cancel import"
        danger
      >
        Files already processed stay imported. Remaining files are skipped; you can retry the import later.
      </ConfirmDialog>
    </>
  )
}
