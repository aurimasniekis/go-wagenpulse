import {
  Alert,
  Badge,
  Button,
  Card,
  Group,
  Modal,
  MultiSelect,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
} from '@mantine/core'
import { IconPlus, IconTrash } from '@tabler/icons-react'
import { useForm } from '@tanstack/react-form'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, check, idempotencyKey, ifMatch, unwrap, type Route as RouteT, type Schemas } from '@/api/client'
import { ConfirmDialog } from '@/components/Confirm'
import { eventTypes } from '@/components/domain'
import { AnchorLink } from '@/components/links'
import { EmptyBlock, PageHeader, QueryState, ScrollRegion, StatusBadge } from '@/components/ui'
import { errorMessage, fieldErrors, notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'

interface Search {
  create?: number
  adapter?: string
  edit?: string
}

export const Route = createFileRoute('/routing')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    create: s.create ? 1 : undefined,
    adapter: typeof s.adapter === 'string' ? s.adapter : undefined,
    edit: typeof s.edit === 'string' ? s.edit : undefined,
  }),
  component: Routing,
})

function Routing() {
  const can = useCan()
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/routing' })
  const qc = useQueryClient()
  const [deleting, setDeleting] = useState<RouteT | null>(null)
  const routes = useQuery({ queryKey: ['routes'], queryFn: async () => unwrap(await api.GET('/routes')) })
  const adapters = useQuery({
    queryKey: ['adapters'],
    queryFn: async () => unwrap(await api.GET('/adapters')),
    enabled: can('adapters.read'),
  })
  const rules = useQuery({
    queryKey: ['rules'],
    queryFn: async () => unwrap(await api.GET('/rules')),
    enabled: can('rules.read'),
  })
  const vehicles = useQuery({
    queryKey: ['vehicles', 'list', false],
    queryFn: async () => unwrap(await api.GET('/vehicles', { params: { query: { limit: 200 } } })),
  })
  const del = useMutation({
    mutationFn: async (id: string) => {
      check(await api.DELETE('/routes/{id}', { params: { path: { id } } }))
    },
    onSuccess: async () => {
      setDeleting(null)
      notifySuccess('Route deleted')
      await qc.invalidateQueries({ queryKey: ['routes'] })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const close = () => void navigate({ search: {} })
  const editing = routes.data?.find((r) => r.id === search.edit)
  const aName = (id: string) => adapters.data?.find((a) => a.id === id)?.name ?? id
  const rName = (id: string) => rules.data?.find((r) => r.id === id)?.name ?? id
  const vName = (id: string) => vehicles.data?.items.find((v) => v.id === id)?.display_name ?? id
  const any = (xs: string[], f: (x: string) => string, all: string) =>
    xs.length ? (
      xs.map(f).join(', ')
    ) : (
      <Text span c="dimmed" size="sm">
        {all}
      </Text>
    )
  return (
    <>
      <PageHeader
        title="Routing"
        description="A route sends matching events to one adapter. An event is delivered once to each adapter with at least one matching route."
        actions={
          can('routes.write') ? (
            <Button leftSection={<IconPlus size={16} />} onClick={() => void navigate({ search: { create: 1 } })}>
              Add route
            </Button>
          ) : null
        }
      />
      <Card p={0}>
        <QueryState query={routes} loadingLabel="Loading routes…">
          {() =>
            (routes.data ?? []).length === 0 ? (
              <EmptyBlock
                title="No routes"
                description="Without a route, events are stored but not delivered anywhere."
              />
            ) : (
              <ScrollRegion label="Routes">
                <Table className="wp-table" miw={880} data-testid="routes-table">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>Route</Table.Th>
                      <Table.Th>Adapter</Table.Th>
                      <Table.Th>Event types</Table.Th>
                      <Table.Th>Rules</Table.Th>
                      <Table.Th>Vehicles</Table.Th>
                      <Table.Th>Backfill</Table.Th>
                      <Table.Th />
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {(routes.data ?? []).map((r) => (
                      <Table.Tr key={r.id}>
                        <Table.Td>
                          <Group gap={6}>
                            <Text size="sm" fw={600}>
                              {r.name}
                            </Text>
                            {!r.enabled ? <StatusBadge state="disabled" /> : null}
                          </Group>
                        </Table.Td>
                        <Table.Td>
                          <AnchorLink to="/adapters/$adapterId" params={{ adapterId: r.adapter_id }} size="sm">
                            {aName(r.adapter_id)}
                          </AnchorLink>
                        </Table.Td>
                        <Table.Td>{any(r.event_types, (x) => x, 'all types')}</Table.Td>
                        <Table.Td>{any(r.rule_ids, rName, 'all rules')}</Table.Td>
                        <Table.Td>{any(r.vehicle_ids, vName, 'all vehicles')}</Table.Td>
                        <Table.Td>
                          {r.include_backfill ? (
                            <Badge variant="light">included</Badge>
                          ) : (
                            <Text span c="dimmed" size="sm">
                              excluded
                            </Text>
                          )}
                        </Table.Td>
                        <Table.Td>
                          {can('routes.write') ? (
                            <Group gap={4} justify="flex-end" wrap="nowrap">
                              <Button
                                size="xs"
                                variant="default"
                                onClick={() => void navigate({ search: { edit: r.id } })}
                              >
                                Edit
                              </Button>
                              <Button
                                size="xs"
                                variant="subtle"
                                color="red"
                                onClick={() => {
                                  setDeleting(r)
                                }}
                                aria-label={`Delete route ${r.name}`}
                              >
                                <IconTrash size={14} />
                              </Button>
                            </Group>
                          ) : null}
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </ScrollRegion>
            )
          }
        </QueryState>
      </Card>
      <Modal
        opened={Boolean(search.create) || Boolean(editing)}
        onClose={close}
        title={<Text fw={650}>{editing ? 'Edit route' : 'Add route'}</Text>}
        size="lg"
      >
        <RouteForm
          key={editing?.id ?? 'new'}
          initial={editing}
          adapterId={search.adapter}
          adapters={(adapters.data ?? []).map((a) => ({ value: a.id, label: a.name }))}
          rules={(rules.data ?? []).filter((r) => !r.deleted).map((r) => ({ value: r.id, label: r.name }))}
          vehicles={(vehicles.data?.items ?? []).map((v) => ({ value: v.id, label: v.display_name }))}
          onCancel={close}
          onSubmit={async (body) => {
            if (editing)
              unwrap(
                await api.PUT('/routes/{id}', {
                  params: { path: { id: editing.id } },
                  body,
                  headers: ifMatch(editing.revision),
                }),
              )
            else unwrap(await api.POST('/routes', { body, headers: idempotencyKey() }))
            await qc.invalidateQueries({ queryKey: ['routes'] })
            notifySuccess(editing ? 'Route saved' : 'Route added')
            close()
          }}
        />
      </Modal>
      <ConfirmDialog
        opened={Boolean(deleting)}
        onClose={() => {
          setDeleting(null)
        }}
        onConfirm={() => {
          if (deleting) del.mutate(deleting.id)
        }}
        loading={del.isPending}
        title="Delete route?"
        confirmLabel="Delete route"
        danger
      >
        New events stop matching <b>{deleting?.name}</b>. Deliveries already created are not affected.
      </ConfirmDialog>
    </>
  )
}

interface Opt {
  value: string
  label: string
}

function RouteForm({
  initial,
  adapterId,
  adapters,
  rules,
  vehicles,
  onSubmit,
  onCancel,
}: {
  initial?: RouteT
  adapterId?: string
  adapters: Opt[]
  rules: Opt[]
  vehicles: Opt[]
  onSubmit: (b: Schemas['RouteBody']) => Promise<void>
  onCancel: () => void
}) {
  const [error, setError] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const form = useForm({
    defaultValues: {
      name: initial?.name ?? '',
      adapter_id: initial?.adapter_id ?? adapterId ?? adapters[0]?.value ?? '',
      event_types: initial?.event_types ?? [],
      rule_ids: initial?.rule_ids ?? [],
      vehicle_ids: initial?.vehicle_ids ?? [],
      include_backfill: initial?.include_backfill ?? false,
      enabled: initial?.enabled ?? true,
    },
    onSubmit: async ({ value }) => {
      setError('')
      setFields({})
      try {
        await onSubmit(value)
      } catch (e) {
        setError(errorMessage(e))
        setFields(fieldErrors(e))
      }
    },
  })
  if (adapters.length === 0) {
    return (
      <Alert variant="light" color="yellow">
        Add an adapter first; routes send events to an adapter.
      </Alert>
    )
  }
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
          <form.Field name="adapter_id">
            {(f) => (
              <Select
                label="Adapter"
                withAsterisk
                data={adapters}
                value={f.state.value}
                onChange={(v) => {
                  f.handleChange(v ?? '')
                }}
                allowDeselect={false}
                error={fields.adapter_id}
              />
            )}
          </form.Field>
        </SimpleGrid>
        <form.Field name="event_types">
          {(f) => (
            <MultiSelect
              label="Event types"
              description="Empty matches every type"
              data={eventTypes}
              value={f.state.value}
              onChange={(v) => {
                f.handleChange(v)
              }}
              error={fields.event_types}
            />
          )}
        </form.Field>
        <form.Field name="rule_ids">
          {(f) => (
            <MultiSelect
              label="Rules"
              description="Empty matches every rule"
              data={rules}
              value={f.state.value}
              onChange={(v) => {
                f.handleChange(v)
              }}
              searchable
              error={fields.rule_ids}
            />
          )}
        </form.Field>
        <form.Field name="vehicle_ids">
          {(f) => (
            <MultiSelect
              label="Vehicles"
              description="Empty matches every vehicle"
              data={vehicles}
              value={f.state.value}
              onChange={(v) => {
                f.handleChange(v)
              }}
              searchable
              error={fields.vehicle_ids}
            />
          )}
        </form.Field>
        <form.Field name="include_backfill">
          {(f) => (
            <Switch
              label="Deliver backfill events"
              description="Events from imported history (marked backfill) are delivered only when enabled."
              checked={f.state.value}
              onChange={(e) => {
                f.handleChange(e.currentTarget.checked)
              }}
            />
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
                {initial ? 'Save route' : 'Add route'}
              </Button>
            )}
          </form.Subscribe>
        </Group>
      </Stack>
    </form>
  )
}
