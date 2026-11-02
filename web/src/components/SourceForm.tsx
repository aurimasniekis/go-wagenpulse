import {
  Alert,
  Button,
  Checkbox,
  Group,
  MultiSelect,
  NumberInput,
  PasswordInput,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Text,
  Textarea,
  TextInput,
} from '@mantine/core'
import { IconAlertTriangle } from '@tabler/icons-react'
import { useForm } from '@tanstack/react-form'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api, unwrap, type Schemas, type Source } from '@/api/client'
import { errorMessage, fieldErrors } from '@/lib/notify'
import { eventPolicies } from './domain'
import { brands } from './VehicleForm'

export type SourceBody = Schemas['SourceBody']

/**
 * Create/edit form for a portal source. Secrets are write-only: an empty secret field on edit keeps the stored value.
 * The interface is the portal's undocumented web backend, so the form says so plainly.
 */
export function SourceForm({
  initial,
  onSubmit,
  onCancel,
  submitLabel,
}: {
  initial?: Source
  onSubmit: (b: SourceBody) => Promise<void>
  onCancel: () => void
  submitLabel: string
}) {
  const [error, setError] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const [secretMode, setSecretMode] = useState<'stored' | 'ref'>(initial?.secret_ref ? 'ref' : 'stored')
  const [clearSecret, setClearSecret] = useState(false)
  const vehicles = useQuery({
    queryKey: ['vehicles', 'list', false],
    queryFn: async () => unwrap(await api.GET('/vehicles', { params: { query: { limit: 200 } } })),
  })
  const form = useForm({
    defaultValues: {
      name: initial?.name ?? '',
      enabled: initial?.enabled ?? true,
      brand: (initial?.brand ?? 'volkswagen') as SourceBody['brand'],
      country: initial?.country ?? 'de',
      language: initial?.language ?? 'en',
      portal_brand_key: initial?.portal_brand_key ?? '',
      auth_mode: initial?.auth_mode ?? 'session_cookie',
      username: '',
      secret: '',
      secret_ref: initial?.secret_ref ?? '',
      collect_continuous: initial?.collect_continuous ?? true,
      collect_historical: initial?.collect_historical ?? false,
      interval_minutes: initial?.interval_minutes ?? 15,
      live_event_policy: (initial?.live_event_policy ?? 'publish') as SourceBody['live_event_policy'],
      historical_event_policy: (initial?.historical_event_policy ?? 'none') as SourceBody['historical_event_policy'],
      vehicle_ids: initial?.vehicle_ids ?? [],
    },
    onSubmit: async ({ value }) => {
      setError('')
      setFields({})
      const body: SourceBody = {
        name: value.name,
        enabled: value.enabled,
        brand: value.brand,
        country: value.country.trim().toLowerCase(),
        language: value.language.trim().toLowerCase(),
        portal_brand_key: value.portal_brand_key.trim() || undefined,
        auth_mode: value.auth_mode,
        collect_continuous: value.collect_continuous,
        collect_historical: value.collect_historical,
        interval_minutes: value.interval_minutes,
        live_event_policy: value.live_event_policy,
        historical_event_policy: value.historical_event_policy,
        vehicle_ids: value.vehicle_ids,
      }
      if (value.username) body.username = value.username
      if (secretMode === 'ref') {
        body.secret_ref = value.secret_ref.trim()
      } else if (value.secret) {
        body.secret = value.secret
      } else if (clearSecret) {
        body.secret = ''
      }
      try {
        await onSubmit(body)
      } catch (e) {
        setError(errorMessage(e))
        setFields(fieldErrors(e))
      }
    },
  })
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        void form.handleSubmit()
      }}
    >
      <Stack gap="sm">
        <Alert
          color="yellow"
          variant="light"
          icon={<IconAlertTriangle size={18} />}
          title="Unofficial portal interface"
        >
          The EU Data Act portal has no documented API. Collection uses the portal's own web backend, which may change
          or block automated access without notice. A continuous data request must already exist (create it in a
          browser). Credentials are encrypted at rest and only sent to the portal.
        </Alert>
        {error ? (
          <Alert color="red" variant="light" role="alert">
            {error}
          </Alert>
        ) : null}
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <form.Field name="name" validators={{ onSubmit: ({ value }) => (value.trim() ? undefined : 'Required') }}>
            {(f) => (
              <TextInput
                label="Name"
                withAsterisk
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={f.state.meta.errors[0] ?? fields.name}
              />
            )}
          </form.Field>
          <form.Field name="brand">
            {(f) => (
              <Select
                label="Portal brand"
                data={brands.filter((b) => b.value !== 'other')}
                value={f.state.value}
                onChange={(v) => {
                  f.handleChange((v ?? 'volkswagen') as SourceBody['brand'])
                }}
                allowDeselect={false}
                error={fields.brand}
              />
            )}
          </form.Field>
          <form.Field name="country">
            {(f) => (
              <TextInput
                label="Country"
                description="Two-letter code used by the portal URL"
                maxLength={2}
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={fields.country}
              />
            )}
          </form.Field>
          <form.Field name="language">
            {(f) => (
              <TextInput
                label="Language"
                description="Two-letter code"
                maxLength={2}
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={fields.language}
              />
            )}
          </form.Field>
        </SimpleGrid>
        <form.Field name="auth_mode">
          {(f) => (
            <Stack gap={4}>
              <Text size="sm" fw={500}>
                Authentication
              </Text>
              <SegmentedControl
                value={f.state.value}
                onChange={(v) => {
                  f.handleChange(v as SourceBody['auth_mode'])
                }}
                data={[
                  { value: 'session_cookie', label: 'Browser session cookie' },
                  { value: 'password', label: 'Scripted login (email + password)' },
                ]}
              />
              <Text size="xs" c="dimmed">
                {f.state.value === 'session_cookie'
                  ? 'Copy the Cookie request header from a signed-in portal tab (developer tools → Network). Expires when the portal session ends; you will be asked to re-authorize.'
                  : 'WagenPulse signs in like the portal web page. It stops and asks for help when a CAPTCHA, new terms or consent appear; it never bypasses them.'}
              </Text>
            </Stack>
          )}
        </form.Field>
        <form.Subscribe selector={(s) => s.values.auth_mode}>
          {(mode) =>
            mode === 'password' ? (
              <SimpleGrid cols={{ base: 1, sm: 2 }}>
                <form.Field name="username">
                  {(f) => (
                    <TextInput
                      label="Account email"
                      placeholder={initial?.username_set ? 'Stored — leave empty to keep' : undefined}
                      value={f.state.value}
                      onChange={(e) => {
                        f.handleChange(e.currentTarget.value)
                      }}
                      error={fields.username}
                      autoComplete="off"
                    />
                  )}
                </form.Field>
                <form.Field name="portal_brand_key">
                  {(f) => (
                    <TextInput
                      label="Portal brand key"
                      description="Only when the brand's login needs it"
                      value={f.state.value}
                      onChange={(e) => {
                        f.handleChange(e.currentTarget.value)
                      }}
                      error={fields.portal_brand_key}
                    />
                  )}
                </form.Field>
              </SimpleGrid>
            ) : null
          }
        </form.Subscribe>
        <Stack gap={4}>
          <Group justify="space-between">
            <Text size="sm" fw={500}>
              Secret
            </Text>
            <SegmentedControl
              size="xs"
              value={secretMode}
              onChange={(v) => {
                setSecretMode(v as 'stored' | 'ref')
              }}
              data={[
                { value: 'stored', label: 'Store encrypted' },
                { value: 'ref', label: 'External reference' },
              ]}
            />
          </Group>
          {secretMode === 'stored' ? (
            <form.Subscribe selector={(s) => s.values.auth_mode}>
              {(mode) => (
                <form.Field name="secret">
                  {(f) =>
                    mode === 'session_cookie' ? (
                      <Textarea
                        aria-label="Session cookie"
                        placeholder={initial?.secret_set ? 'Stored — leave empty to keep' : 'Cookie header value'}
                        autosize
                        minRows={2}
                        maxRows={5}
                        value={f.state.value}
                        onChange={(e) => {
                          f.handleChange(e.currentTarget.value)
                        }}
                        error={fields.secret}
                        styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 12 } }}
                      />
                    ) : (
                      <PasswordInput
                        aria-label="Password"
                        placeholder={initial?.secret_set ? 'Stored — leave empty to keep' : 'Password'}
                        value={f.state.value}
                        onChange={(e) => {
                          f.handleChange(e.currentTarget.value)
                        }}
                        error={fields.secret}
                        autoComplete="new-password"
                      />
                    )
                  }
                </form.Field>
              )}
            </form.Subscribe>
          ) : (
            <form.Field name="secret_ref">
              {(f) => (
                <TextInput
                  aria-label="Secret reference"
                  placeholder="env:WAGENPULSE_SECRET_PORTAL or file:/run/secrets/portal"
                  value={f.state.value}
                  onChange={(e) => {
                    f.handleChange(e.currentTarget.value)
                  }}
                  error={fields.secret_ref}
                />
              )}
            </form.Field>
          )}
          {initial?.secret_set && secretMode === 'stored' && !initial.secret_ref ? (
            <Checkbox
              size="xs"
              label="Clear the stored secret"
              checked={clearSecret}
              onChange={(e) => {
                setClearSecret(e.currentTarget.checked)
              }}
            />
          ) : null}
        </Stack>
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <form.Field name="collect_continuous">
            {(f) => (
              <Switch
                label="Collect continuous deliveries"
                checked={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.checked)
                }}
              />
            )}
          </form.Field>
          <form.Field name="collect_historical">
            {(f) => (
              <Switch
                label="Collect historical deliveries"
                description="Historical export format is unverified; files are stored and shown as unrecognized until a sample is confirmed."
                checked={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.checked)
                }}
              />
            )}
          </form.Field>
          <form.Field name="interval_minutes">
            {(f) => (
              <NumberInput
                label="Check every (minutes)"
                description="15–1440. The portal delivers continuous data at most every 15 minutes."
                min={15}
                max={1440}
                value={f.state.value}
                onChange={(v) => {
                  f.handleChange(typeof v === 'number' ? v : 15)
                }}
                error={fields.interval_minutes}
              />
            )}
          </form.Field>
          <form.Field name="vehicle_ids">
            {(f) => (
              <MultiSelect
                label="Vehicles"
                description="Vehicles whose deliveries this source downloads"
                data={(vehicles.data?.items ?? []).map((v) => ({ value: v.id, label: v.display_name }))}
                value={f.state.value}
                onChange={(v) => {
                  f.handleChange(v)
                }}
                error={fields.vehicle_ids}
                searchable
              />
            )}
          </form.Field>
          <form.Field name="live_event_policy">
            {(f) => (
              <Select
                label="Events from new deliveries"
                data={eventPolicies}
                value={f.state.value ?? 'publish'}
                onChange={(v) => {
                  f.handleChange((v ?? 'publish') as SourceBody['live_event_policy'])
                }}
                allowDeselect={false}
              />
            )}
          </form.Field>
          <form.Field name="historical_event_policy">
            {(f) => (
              <Select
                label="Events from historical deliveries"
                data={eventPolicies}
                value={f.state.value ?? 'none'}
                onChange={(v) => {
                  f.handleChange((v ?? 'none') as SourceBody['historical_event_policy'])
                }}
                allowDeselect={false}
              />
            )}
          </form.Field>
        </SimpleGrid>
        <form.Field name="enabled">
          {(f) => (
            <Switch
              label="Enabled"
              checked={f.state.value}
              onChange={(e) => {
                f.handleChange(e.currentTarget.checked)
              }}
            />
          )}
        </form.Field>
        <Group justify="flex-end">
          <Button variant="default" onClick={onCancel}>
            Cancel
          </Button>
          <form.Subscribe selector={(s) => s.isSubmitting}>
            {(submitting) => (
              <Button type="submit" loading={submitting}>
                {submitLabel}
              </Button>
            )}
          </form.Subscribe>
        </Group>
      </Stack>
    </form>
  )
}
