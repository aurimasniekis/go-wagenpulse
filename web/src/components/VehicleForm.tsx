import {
  Alert,
  Button,
  Group,
  NumberInput,
  Select,
  SimpleGrid,
  Stack,
  TagsInput,
  Textarea,
  TextInput,
} from '@mantine/core'
import { useForm } from '@tanstack/react-form'
import { useState } from 'react'
import type { Schemas, Vehicle } from '@/api/client'
import { errorMessage, fieldErrors } from '@/lib/notify'

export type VehicleBody = Schemas['VehicleBody']

export const brands = [
  { value: 'volkswagen', label: 'Volkswagen' },
  { value: 'volkswagen_commercial', label: 'Volkswagen Commercial Vehicles' },
  { value: 'audi', label: 'Audi' },
  { value: 'skoda', label: 'Škoda' },
  { value: 'seat', label: 'SEAT' },
  { value: 'cupra', label: 'CUPRA' },
  { value: 'bentley', label: 'Bentley' },
  { value: 'other', label: 'Other' },
]

const vinRe = /^[A-HJ-NPR-Z0-9]{17}$/

/** Create/edit form for a vehicle. The VIN is personal data: it is masked elsewhere in the UI. */
export function VehicleForm({
  initial,
  onSubmit,
  onCancel,
  submitLabel,
}: {
  initial?: Vehicle
  onSubmit: (b: VehicleBody) => Promise<void>
  onCancel: () => void
  submitLabel: string
}) {
  const [error, setError] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const form = useForm({
    defaultValues: {
      display_name: initial?.display_name ?? '',
      vin: initial?.vin ?? '',
      slug: initial?.slug ?? '',
      brand: initial?.brand ?? 'volkswagen',
      model: initial?.model ?? '',
      model_year: initial?.model_year ?? (undefined as number | undefined),
      license_plate: initial?.license_plate ?? '',
      tags: initial?.tags ?? [],
      notes: initial?.notes ?? '',
      stale_after_minutes: initial?.stale_after_minutes ?? (undefined as number | undefined),
    },
    onSubmit: async ({ value }) => {
      setError('')
      setFields({})
      try {
        await onSubmit({
          ...value,
          vin: value.vin.trim().toUpperCase(),
          slug: value.slug.trim() || undefined,
          brand: value.brand,
          model_year: value.model_year,
          stale_after_minutes: value.stale_after_minutes,
        })
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
        {error ? (
          <Alert color="red" variant="light" role="alert">
            {error}
          </Alert>
        ) : null}
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <form.Field
            name="display_name"
            validators={{ onSubmit: ({ value }) => (value.trim() ? undefined : 'Required') }}
          >
            {(f) => (
              <TextInput
                label="Name"
                withAsterisk
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={f.state.meta.errors[0] ?? fields.display_name}
              />
            )}
          </form.Field>
          <form.Field
            name="vin"
            validators={{
              onSubmit: ({ value }) =>
                vinRe.test(value.trim().toUpperCase()) ? undefined : '17 characters, no I, O or Q',
            }}
          >
            {(f) => (
              <TextInput
                label="VIN"
                withAsterisk
                description="Used to match deliveries to this vehicle."
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={f.state.meta.errors[0] ?? fields.vin}
                autoComplete="off"
                styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)' } }}
              />
            )}
          </form.Field>
          <form.Field name="brand">
            {(f) => (
              <Select
                label="Brand"
                data={brands}
                value={f.state.value}
                onChange={(v) => {
                  f.handleChange((v ?? 'other') as typeof f.state.value)
                }}
                allowDeselect={false}
              />
            )}
          </form.Field>
          <form.Field name="model">
            {(f) => (
              <TextInput
                label="Model"
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
              />
            )}
          </form.Field>
          <form.Field name="model_year">
            {(f) => (
              <NumberInput
                label="Model year"
                min={1990}
                max={2100}
                value={f.state.value ?? ''}
                onChange={(v) => {
                  f.handleChange(typeof v === 'number' ? v : undefined)
                }}
                error={fields.model_year}
              />
            )}
          </form.Field>
          <form.Field name="license_plate">
            {(f) => (
              <TextInput
                label="License plate"
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
              />
            )}
          </form.Field>
          <form.Field name="slug">
            {(f) => (
              <TextInput
                label="Slug"
                description="Used in event subjects; generated from the name when empty."
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={fields.slug}
              />
            )}
          </form.Field>
          <form.Field name="stale_after_minutes">
            {(f) => (
              <NumberInput
                label="Stale after (minutes)"
                description="Empty uses the system default."
                min={15}
                max={525600}
                value={f.state.value ?? ''}
                onChange={(v) => {
                  f.handleChange(typeof v === 'number' ? v : undefined)
                }}
                error={fields.stale_after_minutes}
              />
            )}
          </form.Field>
        </SimpleGrid>
        <form.Field name="tags">
          {(f) => (
            <TagsInput
              label="Tags"
              value={f.state.value}
              onChange={(v) => {
                f.handleChange(v)
              }}
            />
          )}
        </form.Field>
        <form.Field name="notes">
          {(f) => (
            <Textarea
              label="Notes"
              autosize
              minRows={2}
              value={f.state.value}
              onChange={(e) => {
                f.handleChange(e.currentTarget.value)
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
