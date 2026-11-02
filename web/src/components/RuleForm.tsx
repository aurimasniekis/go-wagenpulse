import {
  Alert,
  Button,
  Group,
  MultiSelect,
  NumberInput,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  TagsInput,
  Text,
  Textarea,
  TextInput,
} from '@mantine/core'
import { useForm } from '@tanstack/react-form'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api, unwrap, type Definition, type Rule, type Schemas } from '@/api/client'
import { errorMessage, fieldErrors } from '@/lib/notify'
import { SignalPicker } from './SignalPicker'

export type RuleBody = Schemas['RuleBody']

export const ruleKinds: { value: Definition['kind']; label: string; help: string }[] = [
  {
    value: 'threshold',
    label: 'Threshold',
    help: 'Fires when a numeric value goes above or below a limit; re-arms after it returns past the hysteresis band.',
  },
  {
    value: 'signal_change',
    label: 'Change',
    help: 'Fires whenever a value changes (numbers: by at least the deadband).',
  },
  {
    value: 'transition',
    label: 'Transition',
    help: 'Fires when a text or boolean value moves from one state to another (e.g. charging state).',
  },
  {
    value: 'stale',
    label: 'Stale data',
    help: 'Fires once when no new data arrived for a period; checked on a schedule, not only on import.',
  },
]

export const kindEvents: Record<string, string[]> = {
  threshold: ['threshold.crossed', 'threshold.cleared'],
  signal_change: ['signal.changed'],
  transition: ['state.transition'],
  stale: ['data.stale', 'data.resumed'],
}

export function emptyDefinition(): Definition {
  return {
    kind: 'threshold',
    signal_keys: [],
    vehicle_ids: [],
    severity: 'info',
    direction: 'below',
    threshold: 0,
    hysteresis: 0,
    debounce_count: 1,
    cooldown_seconds: 0,
  }
}

/** Builds a definition carrying only the fields that apply to its kind (the server also normalizes). */
export function cleanDefinition(d: Definition): Definition {
  const base: Definition = {
    kind: d.kind,
    signal_keys: d.signal_keys,
    vehicle_ids: d.vehicle_ids,
    severity: d.severity,
    debounce_count: Math.max(1, d.debounce_count ?? 1),
    cooldown_seconds: d.cooldown_seconds ?? 0,
  }
  switch (d.kind) {
    case 'signal_change':
      return { ...base, deadband: d.deadband ?? 0 }
    case 'threshold':
      return {
        ...base,
        direction: d.direction || 'above',
        threshold: d.threshold ?? 0,
        hysteresis: d.hysteresis ?? 0,
        notify_clear: Boolean(d.notify_clear),
      }
    case 'transition':
      return { ...base, from: d.from ?? [], to: d.to ?? [] }
    case 'stale':
      return {
        ...base,
        stale_after_seconds: d.stale_after_seconds ?? 3600,
        notify_clear: Boolean(d.notify_clear),
        debounce_count: 1,
      }
  }
}

/** Rule editor. Saving creates a new immutable revision and resets the rule's evaluation state. */
export function RuleForm({
  initial,
  onSubmit,
  onCancel,
  submitLabel,
  onDefinitionChange,
}: {
  initial?: Rule
  onSubmit: (b: RuleBody) => Promise<void>
  onCancel: () => void
  submitLabel: string
  onDefinitionChange?: (d: Definition) => void
}) {
  const [error, setError] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const vehicles = useQuery({
    queryKey: ['vehicles', 'list', false],
    queryFn: async () => unwrap(await api.GET('/vehicles', { params: { query: { limit: 200 } } })),
  })
  const form = useForm({
    defaultValues: {
      name: initial?.name ?? '',
      description: initial?.description ?? '',
      enabled: initial?.enabled ?? true,
      definition: initial ? { ...emptyDefinition(), ...initial.definition } : emptyDefinition(),
    },
    listeners: {
      onChange: ({ formApi }) => onDefinitionChange?.(cleanDefinition(formApi.state.values.definition)),
    },
    onSubmit: async ({ value }) => {
      setError('')
      setFields({})
      try {
        await onSubmit({
          name: value.name,
          description: value.description,
          enabled: value.enabled,
          definition: cleanDefinition(value.definition),
        })
      } catch (e) {
        setError(errorMessage(e))
        setFields(fieldErrors(e))
      }
    },
  })
  const fe = (name: string) => fields[`definition.${name}`] ?? fields[name]
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
          <form.Field name="definition.severity">
            {(f) => (
              <Select
                label="Severity"
                data={[
                  { value: 'info', label: 'Info' },
                  { value: 'warning', label: 'Warning' },
                  { value: 'critical', label: 'Critical' },
                ]}
                value={f.state.value}
                onChange={(v) => {
                  f.handleChange((v ?? 'info') as Definition['severity'])
                }}
                allowDeselect={false}
              />
            )}
          </form.Field>
        </SimpleGrid>
        <form.Field name="description">
          {(f) => (
            <Textarea
              label="Description"
              autosize
              minRows={1}
              value={f.state.value}
              onChange={(e) => {
                f.handleChange(e.currentTarget.value)
              }}
            />
          )}
        </form.Field>
        <form.Field name="definition.kind">
          {(f) => (
            <Stack gap={4}>
              <Text size="sm" fw={500}>
                Rule type
              </Text>
              <SegmentedControl
                value={f.state.value}
                onChange={(v) => {
                  f.handleChange(v as Definition['kind'])
                }}
                data={ruleKinds.map((k) => ({ value: k.value, label: k.label }))}
              />
              <Text size="xs" c="dimmed">
                {ruleKinds.find((k) => k.value === f.state.value)?.help} Emits: {kindEvents[f.state.value]?.join(', ')}.
              </Text>
            </Stack>
          )}
        </form.Field>
        <form.Subscribe selector={(s) => s.values.definition.kind}>
          {(kind) => (
            <>
              <form.Field name="definition.signal_keys">
                {(f) => (
                  <SignalPicker
                    label="Signals"
                    description={kind === 'stale' ? 'Empty watches any signal of the vehicle.' : 'At least one signal.'}
                    value={f.state.value}
                    onChange={(v) => {
                      f.handleChange(v)
                    }}
                    error={fe('signal_keys')}
                  />
                )}
              </form.Field>
              <form.Field name="definition.vehicle_ids">
                {(f) => (
                  <MultiSelect
                    label="Vehicles"
                    description="Empty applies the rule to all vehicles, including ones added later."
                    data={(vehicles.data?.items ?? []).map((v) => ({ value: v.id, label: v.display_name }))}
                    value={f.state.value}
                    onChange={(v) => {
                      f.handleChange(v)
                    }}
                    error={fe('vehicle_ids')}
                    searchable
                  />
                )}
              </form.Field>
              {kind === 'threshold' ? (
                <SimpleGrid cols={{ base: 1, sm: 3 }}>
                  <form.Field name="definition.direction">
                    {(f) => (
                      <Select
                        label="Fires when value goes"
                        data={[
                          { value: 'above', label: 'Above the threshold' },
                          { value: 'below', label: 'Below the threshold' },
                        ]}
                        value={f.state.value || 'above'}
                        onChange={(v) => {
                          f.handleChange((v ?? 'above') as Definition['direction'])
                        }}
                        allowDeselect={false}
                        error={fe('direction')}
                      />
                    )}
                  </form.Field>
                  <form.Field name="definition.threshold">
                    {(f) => (
                      <NumberInput
                        label="Threshold"
                        value={f.state.value ?? 0}
                        onChange={(v) => {
                          f.handleChange(typeof v === 'number' ? v : 0)
                        }}
                        error={fe('threshold')}
                        decimalScale={6}
                      />
                    )}
                  </form.Field>
                  <form.Field name="definition.hysteresis">
                    {(f) => (
                      <NumberInput
                        label="Hysteresis"
                        description="Distance back past the threshold before it re-arms"
                        min={0}
                        value={f.state.value ?? 0}
                        onChange={(v) => {
                          f.handleChange(typeof v === 'number' ? v : 0)
                        }}
                        error={fe('hysteresis')}
                        decimalScale={6}
                      />
                    )}
                  </form.Field>
                </SimpleGrid>
              ) : null}
              {kind === 'signal_change' ? (
                <form.Field name="definition.deadband">
                  {(f) => (
                    <NumberInput
                      label="Deadband"
                      description="Numeric changes smaller than this are ignored (0 = any change)"
                      min={0}
                      value={f.state.value ?? 0}
                      onChange={(v) => {
                        f.handleChange(typeof v === 'number' ? v : 0)
                      }}
                      error={fe('deadband')}
                      decimalScale={6}
                      maw={320}
                    />
                  )}
                </form.Field>
              ) : null}
              {kind === 'transition' ? (
                <SimpleGrid cols={{ base: 1, sm: 2 }}>
                  <form.Field name="definition.from">
                    {(f) => (
                      <TagsInput
                        label="From values"
                        description="Empty = any previous value"
                        value={f.state.value ?? []}
                        onChange={(v) => {
                          f.handleChange(v)
                        }}
                        error={fe('from')}
                      />
                    )}
                  </form.Field>
                  <form.Field name="definition.to">
                    {(f) => (
                      <TagsInput
                        label="To values"
                        description="Empty = any new value"
                        value={f.state.value ?? []}
                        onChange={(v) => {
                          f.handleChange(v)
                        }}
                        error={fe('to')}
                      />
                    )}
                  </form.Field>
                </SimpleGrid>
              ) : null}
              {kind === 'stale' ? (
                <form.Field name="definition.stale_after_seconds">
                  {(f) => (
                    <NumberInput
                      label="Stale after (minutes)"
                      description="5 minutes to 90 days. Portal data arrives at most every 15 minutes, so allow for delivery delay."
                      min={5}
                      max={129600}
                      value={Math.round((f.state.value ?? 3600) / 60)}
                      onChange={(v) => {
                        f.handleChange((typeof v === 'number' ? v : 60) * 60)
                      }}
                      error={fe('stale_after_seconds')}
                      maw={320}
                    />
                  )}
                </form.Field>
              ) : null}
              {kind === 'threshold' || kind === 'stale' ? (
                <form.Field name="definition.notify_clear">
                  {(f) => (
                    <Switch
                      label={
                        kind === 'stale'
                          ? 'Also emit data.resumed when data returns'
                          : 'Also emit threshold.cleared when the value recovers'
                      }
                      checked={Boolean(f.state.value)}
                      onChange={(e) => {
                        f.handleChange(e.currentTarget.checked)
                      }}
                    />
                  )}
                </form.Field>
              ) : null}
              <SimpleGrid cols={{ base: 1, sm: 2 }}>
                {kind !== 'stale' ? (
                  <form.Field name="definition.debounce_count">
                    {(f) => (
                      <NumberInput
                        label="Debounce"
                        description="Consecutive matching observations required (1 = fire immediately)"
                        min={1}
                        max={100}
                        value={f.state.value ?? 1}
                        onChange={(v) => {
                          f.handleChange(typeof v === 'number' ? v : 1)
                        }}
                        error={fe('debounce_count')}
                      />
                    )}
                  </form.Field>
                ) : null}
                <form.Field name="definition.cooldown_seconds">
                  {(f) => (
                    <NumberInput
                      label="Cooldown (minutes)"
                      description="Suppresses repeats per vehicle and signal, by observation time"
                      min={0}
                      max={43200}
                      value={Math.round((f.state.value ?? 0) / 60)}
                      onChange={(v) => {
                        f.handleChange((typeof v === 'number' ? v : 0) * 60)
                      }}
                      error={fe('cooldown_seconds')}
                    />
                  )}
                </form.Field>
              </SimpleGrid>
            </>
          )}
        </form.Subscribe>
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
        {initial ? (
          <Text size="xs" c="dimmed">
            Saving creates revision {initial.revision + 1}. The rule's evaluation state (armed thresholds, debounce
            counters, cooldowns) is reset.
          </Text>
        ) : null}
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
