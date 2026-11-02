import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  Chip,
  Group,
  Modal,
  MultiSelect,
  NumberInput,
  SimpleGrid,
  Stack,
  Switch,
  Text,
  Textarea,
  TextInput,
} from '@mantine/core'
import { IconPlus } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo, useState } from 'react'
import { api, check, idempotencyKey, unwrap, type Schemas, type Token } from '@/api/client'
import { ConfirmDialog } from '@/components/Confirm'
import { DataTable, Dim } from '@/components/DataTable'
import { EmptyBlock, PageHeader, QueryState, SecretOnce, StatusBadge, Time } from '@/components/ui'
import { errorMessage, fieldErrors, notifyError, notifySuccess } from '@/lib/notify'
import { useCan, useMe } from '@/lib/session'

interface Search {
  create?: number
  all?: boolean
  revoked?: boolean
}

export const Route = createFileRoute('/admin/tokens')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    create: s.create ? 1 : undefined,
    all: s.all === true || s.all === 'true' ? true : undefined,
    revoked: s.revoked === true || s.revoked === 'true' ? true : undefined,
  }),
  component: Tokens,
})

function Tokens() {
  const can = useCan()
  const me = useMe()
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/admin/tokens' })
  const qc = useQueryClient()
  const [revoking, setRevoking] = useState<Token | null>(null)
  const [created, setCreated] = useState<Schemas['CreatedToken'] | null>(null)
  const set = (p: Partial<Search>) => void navigate({ search: (s) => ({ ...s, ...p }), replace: true })
  const list = useQuery({
    queryKey: ['tokens', search.all, search.revoked],
    queryFn: async () =>
      unwrap(
        await api.GET('/tokens', {
          params: { query: { all: search.all, include_revoked: search.revoked, limit: 200 } },
        }),
      ),
  })
  const users = useQuery({
    queryKey: ['users'],
    queryFn: async () => unwrap(await api.GET('/users', { params: { query: { limit: 200 } } })),
    enabled: can('users.read') && Boolean(search.all),
  })
  const revoke = useMutation({
    mutationFn: async (t: Token) => {
      const self = me.data?.token_id === t.id
      check(
        await api.DELETE('/tokens/{id}', {
          params: { path: { id: t.id }, query: { reason: 'revoked in UI', confirm_self_revoke: self || undefined } },
        }),
      )
    },
    onSuccess: async () => {
      setRevoking(null)
      notifySuccess('Token revoked; it stops working immediately')
      await qc.invalidateQueries({ queryKey: ['tokens'] })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const owner = (id: string) =>
    users.data?.items.find((u) => u.id === id)?.username ?? (id === me.data?.user.id ? 'you' : id)
  const columns = useMemo<ColumnDef<Token>[]>(
    () => [
      {
        header: 'Token',
        accessorKey: 'name',
        cell: ({ row }) => (
          <div>
            <Text size="sm" fw={600}>
              {row.original.name}
            </Text>
            <Text size="xs" c="dimmed" ff="monospace">
              {row.original.hint}
            </Text>
          </div>
        ),
      },
      ...(search.all
        ? [{ header: 'Owner', accessorFn: (t: Token) => owner(t.owner_id) } satisfies ColumnDef<Token>]
        : []),
      { header: 'Status', accessorKey: 'status', cell: ({ getValue }) => <StatusBadge state={getValue() as string} /> },
      {
        header: 'Scopes',
        accessorFn: (t) => t.scopes.length,
        cell: ({ row }) => <Text size="sm">{row.original.scopes.length} permission(s)</Text>,
      },
      {
        header: 'Expires',
        accessorFn: (t) => t.expires_at ?? '',
        cell: ({ row }) =>
          row.original.never_expires ? (
            <Badge color="orange" variant="light">
              never
            </Badge>
          ) : (
            <Time value={row.original.expires_at} relative />
          ),
      },
      {
        header: 'Last used',
        accessorFn: (t) => t.last_used_at ?? '',
        cell: ({ row }) =>
          row.original.last_used_at ? <Time value={row.original.last_used_at} relative /> : <Dim>never</Dim>,
      },
      {
        id: 'act',
        header: '',
        enableSorting: false,
        cell: ({ row }) =>
          row.original.status === 'active' ? (
            <Button
              size="xs"
              variant="subtle"
              color="red"
              onClick={() => {
                setRevoking(row.original)
              }}
            >
              Revoke
            </Button>
          ) : null,
      },
    ],
    // eslint-disable-next-line react-hooks/exhaustive-deps -- owner names are labels only
    [search.all, users.data],
  )
  return (
    <>
      <PageHeader
        title="API tokens"
        description="Tokens authenticate automation such as Terraform. A token never has more permissions than its owner currently has."
        actions={
          <Group>
            {can('tokens.manage') ? (
              <Switch
                label="All users"
                checked={Boolean(search.all)}
                onChange={(e) => {
                  set({ all: e.currentTarget.checked || undefined })
                }}
              />
            ) : null}
            <Switch
              label="Show revoked"
              checked={Boolean(search.revoked)}
              onChange={(e) => {
                set({ revoked: e.currentTarget.checked || undefined })
              }}
            />
            <Button
              leftSection={<IconPlus size={16} />}
              onClick={() => {
                set({ create: 1 })
              }}
            >
              New token
            </Button>
          </Group>
        }
      />
      <Card p={0}>
        <QueryState query={list} loadingLabel="Loading tokens…">
          {() => (
            <DataTable
              caption="API tokens"
              testId="tokens-table"
              data={list.data?.items ?? []}
              columns={columns}
              empty={
                <EmptyBlock title="No tokens" description="Create a token to use the API or the Terraform provider." />
              }
            />
          )}
        </QueryState>
      </Card>
      <Modal
        opened={Boolean(search.create) || Boolean(created)}
        onClose={() => {
          setCreated(null)
          set({ create: undefined })
        }}
        title={<Text fw={650}>{created ? 'Token created' : 'New API token'}</Text>}
        size="lg"
        closeOnClickOutside={!created}
      >
        {created ? (
          <Stack>
            <SecretOnce
              label={created.name}
              value={created.token}
              warning="Copy the token now. It is shown only once; WagenPulse stores only a verifier."
            />
            <Group justify="flex-end">
              <Button
                onClick={() => {
                  setCreated(null)
                  set({ create: undefined })
                }}
              >
                Done
              </Button>
            </Group>
          </Stack>
        ) : (
          <CreateToken
            onCreated={async (t) => {
              setCreated(t)
              await qc.invalidateQueries({ queryKey: ['tokens'] })
            }}
          />
        )}
      </Modal>
      <ConfirmDialog
        opened={Boolean(revoking)}
        onClose={() => {
          setRevoking(null)
        }}
        onConfirm={() => {
          if (revoking) revoke.mutate(revoking)
        }}
        loading={revoke.isPending}
        title="Revoke token?"
        confirmLabel="Revoke"
        danger
        typeToConfirm={revoking?.name}
      >
        Anything using <b>{revoking?.name}</b> stops working immediately. This cannot be undone.
        {revoking && me.data?.token_id === revoking.id ? ' You are using this token for the current request.' : ''}
      </ConfirmDialog>
    </>
  )
}

function CreateToken({ onCreated }: { onCreated: (t: Schemas['CreatedToken']) => Promise<void> }) {
  const me = useMe()
  const scopes = useQuery({ queryKey: ['token-scopes'], queryFn: async () => unwrap(await api.GET('/token-scopes')) })
  const system = useQuery({
    queryKey: ['settings', 'system'],
    queryFn: async () => unwrap(await api.GET('/settings/system')),
    retry: false,
  })
  const vehicles = useQuery({
    queryKey: ['vehicles', 'list', false],
    queryFn: async () => unwrap(await api.GET('/vehicles', { params: { query: { limit: 200 } } })),
  })
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [vehicleIds, setVehicleIds] = useState<string[]>([])
  const [days, setDays] = useState<number | ''>('')
  const [never, setNever] = useState(false)
  const [ack, setAck] = useState(false)
  const [error, setError] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const mine = new Set(me.data?.permissions ?? [])
  const isRoot = me.data?.user.role === 'root' && me.data.method !== 'token'
  const maxDays = system.data?.token_max_ttl_days ?? 365
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/tokens', {
          body: {
            name,
            description: description || undefined,
            scopes: selected,
            vehicle_ids: vehicleIds,
            never_expires: never || undefined,
            acknowledge_never_expires: never ? ack : undefined,
            expires_in_days: never || days === '' ? undefined : days,
          },
          headers: idempotencyKey(),
        }),
      ),
    onSuccess: onCreated,
    onError: (e) => {
      setError(errorMessage(e))
      setFields(fieldErrors(e))
    },
  })
  const all = (scopes.data?.permissions ?? []).filter((p) => mine.has(p))
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        create.mutate()
      }}
    >
      <Stack gap="sm">
        {error ? (
          <Alert color="red" variant="light" role="alert">
            {error}
          </Alert>
        ) : null}
        <TextInput
          label="Name"
          withAsterisk
          value={name}
          onChange={(e) => {
            setName(e.currentTarget.value)
          }}
          error={fields.name}
          placeholder="terraform"
        />
        <Textarea
          label="Description"
          autosize
          minRows={1}
          value={description}
          onChange={(e) => {
            setDescription(e.currentTarget.value)
          }}
        />
        <Stack gap={4}>
          <Text size="sm" fw={500}>
            Presets
          </Text>
          <Group gap="xs">
            {Object.entries(scopes.data?.presets ?? {}).map(([k, ps]) => (
              <Button
                key={k}
                size="xs"
                variant="default"
                onClick={() => {
                  setSelected(ps.filter((p) => mine.has(p)))
                }}
              >
                {k.replace('_', ' ')}
              </Button>
            ))}
          </Group>
        </Stack>
        <Stack gap={4}>
          <Text size="sm" fw={500}>
            Permissions{' '}
            {fields.scopes ? (
              <Text span c="red" size="xs">
                {fields.scopes}
              </Text>
            ) : null}
          </Text>
          <Chip.Group multiple value={selected} onChange={setSelected}>
            <Group gap={6}>
              {all.map((p) => (
                <Chip key={p} value={p} size="xs" variant="outline">
                  {p}
                </Chip>
              ))}
            </Group>
          </Chip.Group>
          <Text size="xs" c="dimmed">
            Only permissions you hold are offered. tokens.delegate (create tokens with this token) is never included by
            presets; the Terraform <code>wagenpulse_api_token</code> resource needs it.
          </Text>
        </Stack>
        <MultiSelect
          label="Restrict to vehicles"
          description="Empty allows all vehicles"
          data={(vehicles.data?.items ?? []).map((v) => ({ value: v.id, label: v.display_name }))}
          value={vehicleIds}
          onChange={setVehicleIds}
          error={fields.vehicle_ids}
        />
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <NumberInput
            label="Expires in (days)"
            description={`Default ${String(system.data?.token_default_ttl_days ?? '—')}, at most ${String(maxDays)}`}
            min={1}
            max={maxDays}
            value={days}
            onChange={(v) => {
              setDays(typeof v === 'number' ? v : '')
            }}
            disabled={never}
            error={fields.expires_in_days}
          />
          {isRoot ? (
            <Stack gap={4} justify="flex-end">
              <Switch
                label="Never expires (root only)"
                checked={never}
                onChange={(e) => {
                  setNever(e.currentTarget.checked)
                }}
              />
            </Stack>
          ) : null}
        </SimpleGrid>
        {never ? (
          <Alert color="orange" variant="light">
            <Checkbox
              label="I understand this token stays valid until it is revoked. I will store it in a secret manager and rotate it deliberately."
              checked={ack}
              onChange={(e) => {
                setAck(e.currentTarget.checked)
              }}
            />
          </Alert>
        ) : null}
        <Group justify="flex-end">
          <Button type="submit" loading={create.isPending} disabled={!name || selected.length === 0 || (never && !ack)}>
            Create token
          </Button>
        </Group>
      </Stack>
    </form>
  )
}
