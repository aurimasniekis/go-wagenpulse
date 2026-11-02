import {
  Alert,
  Button,
  Checkbox,
  Group,
  NumberInput,
  SegmentedControl,
  SimpleGrid,
  Stack,
  Switch,
  Text,
  Textarea,
  TextInput,
} from '@mantine/core'
import { IconAlertTriangle } from '@tabler/icons-react'
import { useForm } from '@tanstack/react-form'
import { useState } from 'react'
import type { Adapter, Schemas } from '@/api/client'
import { errorMessage, fieldErrors } from '@/lib/notify'

export type AdapterBody = Schemas['AdapterBody']

export const DEFAULT_SUBJECT_TEMPLATE = 'evt.{domain}.vehicle.{event}.{vehicle}'

/** Create/edit form for a Pulse (NATS JetStream) adapter. Credentials are write-only. */
export function AdapterForm({
  initial,
  onSubmit,
  onCancel,
  submitLabel,
}: {
  initial?: Adapter
  onSubmit: (b: AdapterBody) => Promise<void>
  onCancel: () => void
  submitLabel: string
}) {
  const [error, setError] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const [credsMode, setCredsMode] = useState<'stored' | 'ref'>(initial?.creds_ref ? 'ref' : 'stored')
  const [clearCreds, setClearCreds] = useState(false)
  const c = initial?.config
  const form = useForm({
    defaultValues: {
      name: initial?.name ?? '',
      enabled: initial?.enabled ?? true,
      url: c?.url ?? 'tls://',
      subject_domain: c?.subject_domain ?? 'wagenpulse',
      subject_template: c?.subject_template ?? DEFAULT_SUBJECT_TEMPLATE,
      source: c?.source ?? '',
      include_vin: c?.include_vin ?? false,
      publish_timeout_ms: c?.publish_timeout_ms ?? 10000,
      max_attempts: c?.max_attempts ?? 12,
      allow_insecure_transport: c?.allow_insecure_transport ?? false,
      ca_pem: c?.ca_pem ?? '',
      creds: '',
      creds_ref: initial?.creds_ref ?? '',
    },
    onSubmit: async ({ value }) => {
      setError('')
      setFields({})
      const body: AdapterBody = {
        name: value.name,
        enabled: value.enabled,
        config: {
          url: value.url.trim(),
          subject_domain: value.subject_domain.trim(),
          subject_template: value.subject_template.trim(),
          source: value.source.trim() || undefined,
          include_vin: value.include_vin,
          publish_timeout_ms: value.publish_timeout_ms,
          max_attempts: value.max_attempts,
          allow_insecure_transport: value.allow_insecure_transport,
          ca_pem: value.ca_pem.trim() || undefined,
        },
      }
      if (credsMode === 'ref') body.creds_ref = value.creds_ref.trim()
      else if (value.creds) body.creds = value.creds
      else if (clearCreds) body.creds = ''
      try {
        await onSubmit(body)
      } catch (e) {
        setError(errorMessage(e))
        setFields(fieldErrors(e))
      }
    },
  })
  const fe = (n: string) => fields[`config.${n}`] ?? fields[n]
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        void form.handleSubmit()
      }}
    >
      <Stack gap="sm">
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
          <form.Field name="url">
            {(f) => (
              <TextInput
                label="NATS URL"
                withAsterisk
                description="tls://host:4222 (or nats:// with insecure transport allowed)"
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={fe('url')}
              />
            )}
          </form.Field>
          <form.Field name="subject_domain">
            {(f) => (
              <TextInput
                label="Subject domain"
                description="Replaces {domain}; your Pulse grant must allow evt.<domain>.>"
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={fe('subject_domain')}
              />
            )}
          </form.Field>
          <form.Field name="subject_template">
            {(f) => (
              <TextInput
                label="Subject template"
                description="Tokens: {domain} {event} {vehicle} {rule} {severity}. Validated against Pulse subject rules."
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={fe('subject_template')}
                styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
              />
            )}
          </form.Field>
          <form.Field name="publish_timeout_ms">
            {(f) => (
              <NumberInput
                label="Publish timeout (ms)"
                description="A missing publish grant shows up as a timeout"
                min={1000}
                max={60000}
                value={f.state.value}
                onChange={(v) => {
                  f.handleChange(typeof v === 'number' ? v : 10000)
                }}
                error={fe('publish_timeout_ms')}
              />
            )}
          </form.Field>
          <form.Field name="max_attempts">
            {(f) => (
              <NumberInput
                label="Max attempts"
                description="Then the delivery is marked dead (manual retry stays possible)"
                min={1}
                max={100}
                value={f.state.value}
                onChange={(v) => {
                  f.handleChange(typeof v === 'number' ? v : 12)
                }}
                error={fe('max_attempts')}
              />
            )}
          </form.Field>
        </SimpleGrid>
        <Stack gap={4}>
          <Group justify="space-between">
            <Text size="sm" fw={500}>
              Credentials (.creds file)
            </Text>
            <SegmentedControl
              size="xs"
              value={credsMode}
              onChange={(v) => {
                setCredsMode(v as 'stored' | 'ref')
              }}
              data={[
                { value: 'stored', label: 'Store encrypted' },
                { value: 'ref', label: 'External reference' },
              ]}
            />
          </Group>
          {credsMode === 'stored' ? (
            <form.Field name="creds">
              {(f) => (
                <Textarea
                  aria-label="Credentials file contents"
                  placeholder={initial?.creds_set ? 'Stored — leave empty to keep' : '-----BEGIN NATS USER JWT-----\n…'}
                  autosize
                  minRows={3}
                  maxRows={8}
                  value={f.state.value}
                  onChange={(e) => {
                    f.handleChange(e.currentTarget.value)
                  }}
                  error={fe('creds')}
                  styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 12 } }}
                />
              )}
            </form.Field>
          ) : (
            <form.Field name="creds_ref">
              {(f) => (
                <TextInput
                  aria-label="Credentials reference"
                  placeholder="file:/run/secrets/pulse.creds or env:WAGENPULSE_SECRET_PULSE"
                  value={f.state.value}
                  onChange={(e) => {
                    f.handleChange(e.currentTarget.value)
                  }}
                  error={fe('creds_ref')}
                />
              )}
            </form.Field>
          )}
          {initial?.creds_set && credsMode === 'stored' && !initial.creds_ref ? (
            <Checkbox
              size="xs"
              label="Clear the stored credentials"
              checked={clearCreds}
              onChange={(e) => {
                setClearCreds(e.currentTarget.checked)
              }}
            />
          ) : null}
        </Stack>
        <form.Field name="ca_pem">
          {(f) => (
            <Textarea
              label="CA certificate (PEM)"
              description="Only for a private CA; public CAs are trusted by default."
              autosize
              minRows={2}
              maxRows={6}
              value={f.state.value}
              onChange={(e) => {
                f.handleChange(e.currentTarget.value)
              }}
              error={fe('ca_pem')}
              styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 12 } }}
            />
          )}
        </form.Field>
        <form.Field name="include_vin">
          {(f) => (
            <Switch
              label="Include the VIN in event payloads"
              description="Off by default: events identify vehicles by ID and slug. The VIN is personal data."
              checked={f.state.value}
              onChange={(e) => {
                f.handleChange(e.currentTarget.checked)
              }}
            />
          )}
        </form.Field>
        <form.Field name="allow_insecure_transport">
          {(f) => (
            <>
              <Switch
                label="Allow unencrypted nats:// transport"
                checked={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.checked)
                }}
              />
              {f.state.value ? (
                <Alert color="yellow" variant="light" icon={<IconAlertTriangle size={18} />}>
                  Credentials and event payloads cross the network in plain text. Use only on a trusted local network.
                </Alert>
              ) : null}
            </>
          )}
        </form.Field>
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
