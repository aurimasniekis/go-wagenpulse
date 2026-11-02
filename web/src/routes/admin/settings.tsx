import { Alert, Button, Group, NumberInput, Select, SimpleGrid, Stack, Switch, Table, Text } from '@mantine/core'
import { IconPlayerPlay } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { api, etagOf, unwrap, type Retention, type Schemas, type SystemSettings } from '@/api/client'
import { ConfirmDialog } from '@/components/Confirm'
import { eventPolicies } from '@/components/domain'
import { PageHeader, QueryState, Section } from '@/components/ui'
import { errorMessage, fieldErrors, notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'

export const Route = createFileRoute('/admin/settings')({ component: Settings })

function Settings() {
  const system = useQuery({
    queryKey: ['settings', 'system', 'etag'],
    queryFn: async () => {
      const res = await api.GET('/settings/system')
      return { value: unwrap(res), etag: etagOf(res) }
    },
  })
  const retention = useQuery({
    queryKey: ['settings', 'retention', 'etag'],
    queryFn: async () => {
      const res = await api.GET('/settings/retention')
      return { value: unwrap(res), etag: etagOf(res) }
    },
  })
  return (
    <>
      <PageHeader
        title="Settings"
        description="Runtime settings stored in the database. Bootstrap settings (listeners, data directory, keys) live in the configuration file."
      />
      <Stack>
        <QueryState query={system}>
          {() =>
            system.data ? (
              <SystemForm key={system.data.etag} initial={system.data.value} etag={system.data.etag} />
            ) : null
          }
        </QueryState>
        <QueryState query={retention}>
          {() =>
            retention.data ? (
              <RetentionForm key={retention.data.etag} initial={retention.data.value} etag={retention.data.etag} />
            ) : null
          }
        </QueryState>
      </Stack>
    </>
  )
}

function useSave<T>(path: '/settings/system' | '/settings/retention', etag: string) {
  const qc = useQueryClient()
  const [error, setError] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const m = useMutation({
    mutationFn: async (body: T) => {
      setError('')
      setFields({})
      // eslint-disable-next-line @typescript-eslint/no-explicit-any -- both settings endpoints share the PUT shape
      return unwrap(await api.PUT(path, { body: body as any, headers: { 'If-Match': etag } }))
    },
    onSuccess: async () => {
      notifySuccess('Settings saved')
      await qc.invalidateQueries({ queryKey: ['settings'] })
    },
    onError: (e) => {
      setError(errorMessage(e))
      setFields(fieldErrors(e))
    },
  })
  return { ...m, error, fields }
}

function SystemForm({ initial, etag }: { initial: SystemSettings; etag: string }) {
  const can = useCan()
  const [v, setV] = useState(initial)
  const save = useSave<SystemSettings>('/settings/system', etag)
  const ro = !can('settings.write')
  const num = (k: keyof SystemSettings) => (x: string | number) => {
    setV({ ...v, [k]: typeof x === 'number' ? x : 0 })
  }
  return (
    <Section title="System" padded>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate(v)
        }}
      >
        <Stack gap="md">
          {save.error ? (
            <Alert color="red" variant="light" role="alert">
              {save.error}
            </Alert>
          ) : null}
          <Text size="sm" fw={600}>
            Sign-in
          </Text>
          <SimpleGrid cols={{ base: 1, sm: 2 }}>
            <Switch
              label="Password sign-in"
              description="Disabling it is refused if the root user would have no way to sign in."
              checked={v.local_login_enabled}
              onChange={(e) => {
                setV({ ...v, local_login_enabled: e.currentTarget.checked })
              }}
              disabled={ro}
              error={save.fields.local_login_enabled}
            />
            <Switch
              label="Single sign-on"
              checked={v.oidc_login_enabled}
              onChange={(e) => {
                setV({ ...v, oidc_login_enabled: e.currentTarget.checked })
              }}
              disabled={ro}
            />
            <Switch
              label="Require two-factor for admins"
              description="Root and admins who sign in with a password must enroll an authenticator before using WagenPulse."
              checked={v.require_totp_for_admins}
              onChange={(e) => {
                setV({ ...v, require_totp_for_admins: e.currentTarget.checked })
              }}
              disabled={ro}
            />
          </SimpleGrid>
          <SimpleGrid cols={{ base: 1, sm: 2, md: 4 }}>
            <NumberInput
              label="Session idle timeout (min)"
              min={5}
              value={v.session_idle_minutes}
              onChange={num('session_idle_minutes')}
              disabled={ro}
              error={save.fields.session_idle_minutes}
            />
            <NumberInput
              label="Session lifetime (h)"
              min={1}
              value={v.session_absolute_hours}
              onChange={num('session_absolute_hours')}
              disabled={ro}
              error={save.fields.session_absolute_hours}
            />
            <NumberInput
              label="Token default lifetime (days)"
              min={1}
              max={366}
              value={v.token_default_ttl_days}
              onChange={num('token_default_ttl_days')}
              disabled={ro}
              error={save.fields.token_default_ttl_days}
            />
            <NumberInput
              label="Token max lifetime (days)"
              min={1}
              max={366}
              value={v.token_max_ttl_days}
              onChange={num('token_max_ttl_days')}
              disabled={ro}
              error={save.fields.token_max_ttl_days}
            />
          </SimpleGrid>
          <Text size="sm" fw={600}>
            Data and events
          </Text>
          <SimpleGrid cols={{ base: 1, sm: 3 }}>
            <Select
              label="Events from uploads (default)"
              data={eventPolicies}
              value={v.upload_event_policy}
              onChange={(x) => {
                setV({ ...v, upload_event_policy: x ?? 'none' })
              }}
              disabled={ro}
              allowDeselect={false}
            />
            <Select
              label="Events from historical data"
              data={eventPolicies}
              value={v.historical_event_policy}
              onChange={(x) => {
                setV({ ...v, historical_event_policy: x ?? 'none' })
              }}
              disabled={ro}
              allowDeselect={false}
            />
            <NumberInput
              label="Data is stale after (min)"
              description="Default for vehicles without their own value"
              min={1}
              max={43200}
              value={v.stale_after_minutes}
              onChange={num('stale_after_minutes')}
              disabled={ro}
              error={save.fields.stale_after_minutes}
            />
          </SimpleGrid>
          {!ro ? (
            <Group justify="flex-end">
              <Button type="submit" loading={save.isPending}>
                Save system settings
              </Button>
            </Group>
          ) : null}
        </Stack>
      </form>
    </Section>
  )
}

function RetentionForm({ initial, etag }: { initial: Retention; etag: string }) {
  const can = useCan()
  const [v, setV] = useState(initial)
  const [confirm, setConfirm] = useState(false)
  const [result, setResult] = useState<Schemas['RetentionResult'] | null>(null)
  const save = useSave<Retention>('/settings/retention', etag)
  const run = useMutation({
    mutationFn: async () => unwrap(await api.POST('/settings/retention/run', { body: { confirm: true } })),
    onSuccess: (r) => {
      setConfirm(false)
      setResult(r)
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const ro = !can('settings.write')
  const num = (k: keyof Retention) => (x: string | number) => {
    setV({ ...v, [k]: typeof x === 'number' ? x : 0 })
  }
  return (
    <Section
      title="Retention"
      description="How long data is kept. 0 keeps data forever. Retention runs daily; reducing a period deletes older data on the next run."
      padded
      actions={
        !ro ? (
          <Button
            size="xs"
            variant="default"
            leftSection={<IconPlayerPlay size={14} />}
            onClick={() => {
              setConfirm(true)
            }}
          >
            Run now
          </Button>
        ) : null
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate(v)
        }}
      >
        <Stack gap="md">
          {save.error ? (
            <Alert color="red" variant="light" role="alert">
              {save.error}
            </Alert>
          ) : null}
          <SimpleGrid cols={{ base: 1, sm: 2, md: 5 }}>
            <NumberInput
              label="Observations (days)"
              description="0 or 7+"
              min={0}
              value={v.observations_days}
              onChange={num('observations_days')}
              disabled={ro}
              error={save.fields.observations_days}
            />
            <NumberInput
              label="Raw delivery files (days)"
              description="Needed to reprocess"
              min={0}
              value={v.raw_deliveries_days}
              onChange={num('raw_deliveries_days')}
              disabled={ro}
              error={save.fields.raw_deliveries_days}
            />
            <NumberInput
              label="Events & deliveries (days)"
              description="0 or 7+"
              min={0}
              value={v.events_days}
              onChange={num('events_days')}
              disabled={ro}
              error={save.fields.events_days}
            />
            <NumberInput
              label="Audit log (days)"
              description="0 or 90+"
              min={0}
              value={v.audit_days}
              onChange={num('audit_days')}
              disabled={ro}
              error={save.fields.audit_days}
            />
            <NumberInput
              label="Finished jobs (days)"
              min={1}
              value={v.finished_jobs_days}
              onChange={num('finished_jobs_days')}
              disabled={ro}
              error={save.fields.finished_jobs_days}
            />
          </SimpleGrid>
          {!ro ? (
            <Group justify="flex-end">
              <Button type="submit" loading={save.isPending}>
                Save retention
              </Button>
            </Group>
          ) : null}
          {result ? (
            <Alert
              color="green"
              variant="light"
              title="Retention run finished"
              withCloseButton
              onClose={() => {
                setResult(null)
              }}
            >
              <Table>
                <Table.Tbody>
                  {Object.entries(result).map(([k, n]) => (
                    <Table.Tr key={k}>
                      <Table.Td>{k.replace(/_/g, ' ')}</Table.Td>
                      <Table.Td className="wp-num">{n} deleted</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Alert>
          ) : null}
        </Stack>
      </form>
      <ConfirmDialog
        opened={confirm}
        onClose={() => {
          setConfirm(false)
        }}
        onConfirm={() => {
          run.mutate()
        }}
        loading={run.isPending}
        title="Run retention now?"
        confirmLabel="Delete expired data"
        danger
      >
        Deletes everything older than the saved retention periods now. Deleted data cannot be recovered except from a
        backup.
      </ConfirmDialog>
    </Section>
  )
}
