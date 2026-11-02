import {
  Alert,
  Badge,
  Button,
  Card,
  Drawer,
  Group,
  Modal,
  PasswordInput,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
} from '@mantine/core'
import { IconPlus } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo, useState } from 'react'
import { api, check, idempotencyKey, ifMatch, unwrap, type Schemas, type User } from '@/api/client'
import { ConfirmDialog } from '@/components/Confirm'
import { DataTable, Dim } from '@/components/DataTable'
import { DefinitionList, EmptyBlock, PageHeader, QueryState, Section, StatusBadge, Time } from '@/components/ui'
import { errorMessage, fieldErrors, notifyError, notifySuccess } from '@/lib/notify'
import { useCan, useMe } from '@/lib/session'

interface Search {
  create?: number
  user?: string
}

export const Route = createFileRoute('/admin/users')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    create: s.create ? 1 : undefined,
    user: typeof s.user === 'string' ? s.user : undefined,
  }),
  component: Users,
})

const roles = [
  { value: 'admin', label: 'Admin — everything except root-only actions' },
  { value: 'operator', label: 'Operator — vehicles, imports, sources, rules, delivery' },
  { value: 'viewer', label: 'Viewer — read-only' },
]

function Users() {
  const can = useCan()
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/admin/users' })
  const qc = useQueryClient()
  const list = useQuery({
    queryKey: ['users'],
    queryFn: async () => unwrap(await api.GET('/users', { params: { query: { limit: 200 } } })),
  })
  const columns = useMemo<ColumnDef<User>[]>(
    () => [
      {
        header: 'User',
        accessorKey: 'username',
        cell: ({ row }) => (
          <div>
            <Text size="sm" fw={600}>
              {row.original.display_name || row.original.username}
            </Text>
            <Text size="xs" c="dimmed">
              {row.original.username}
              {row.original.email ? ` · ${row.original.email}` : ''}
            </Text>
          </div>
        ),
      },
      {
        header: 'Role',
        accessorKey: 'role',
        cell: ({ getValue }) => (
          <Badge variant="light" color={getValue() === 'root' ? 'yellow' : 'blue'}>
            {getValue() as string}
          </Badge>
        ),
      },
      { header: 'Status', accessorKey: 'status', cell: ({ getValue }) => <StatusBadge state={getValue() as string} /> },
      {
        header: '2FA',
        accessorKey: 'totp_enabled',
        cell: ({ getValue }) =>
          getValue() ? (
            <Badge variant="light" color="green">
              on
            </Badge>
          ) : (
            <Dim>off</Dim>
          ),
      },
      {
        header: 'Password login',
        accessorFn: (u) => u.has_password && u.local_login_enabled,
        cell: ({ getValue }) => (getValue() ? 'yes' : <Dim>no</Dim>),
      },
      {
        header: 'Last sign-in',
        accessorFn: (u) => u.last_login_at ?? '',
        cell: ({ row }) => <Time value={row.original.last_login_at} relative />,
      },
    ],
    [],
  )
  const selected = list.data?.items.find((u) => u.id === search.user)
  return (
    <>
      <PageHeader
        title="Users"
        description="People who can sign in. The root user is created at setup and cannot be demoted, suspended or deleted."
        actions={
          can('users.write') ? (
            <Button leftSection={<IconPlus size={16} />} onClick={() => void navigate({ search: { create: 1 } })}>
              Add user
            </Button>
          ) : null
        }
      />
      <Card p={0}>
        <QueryState query={list} loadingLabel="Loading users…">
          {() => (
            <DataTable
              caption="Users"
              testId="users-table"
              data={list.data?.items ?? []}
              columns={columns}
              onRowClick={(u) => void navigate({ search: { user: u.id } })}
              empty={<EmptyBlock title="No users" />}
            />
          )}
        </QueryState>
      </Card>
      <Modal
        opened={Boolean(search.create)}
        onClose={() => void navigate({ search: {} })}
        title={<Text fw={650}>Add user</Text>}
        size="lg"
      >
        <CreateUser
          onDone={async () => {
            await qc.invalidateQueries({ queryKey: ['users'] })
            void navigate({ search: {} })
          }}
        />
      </Modal>
      <Drawer
        opened={Boolean(selected)}
        onClose={() => void navigate({ search: {} })}
        position="right"
        size="lg"
        title={<Text fw={650}>{selected?.display_name || selected?.username}</Text>}
      >
        {selected ? (
          <UserDetail
            key={selected.id + String(selected.revision)}
            user={selected}
            onClose={() => void navigate({ search: {} })}
          />
        ) : null}
      </Drawer>
    </>
  )
}

function CreateUser({ onDone }: { onDone: () => Promise<void> }) {
  const [v, setV] = useState<Schemas['UserCreate']>({
    username: '',
    display_name: '',
    email: '',
    role: 'viewer',
    password: '',
    local_login_enabled: true,
    status: 'active',
  })
  const [error, setError] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const create = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/users', {
          body: { ...v, password: v.password || undefined, email: v.email || undefined },
          headers: idempotencyKey(),
        }),
      ),
    onSuccess: async (u) => {
      notifySuccess(`${u.username} added`)
      await onDone()
    },
    onError: (e) => {
      setError(errorMessage(e))
      setFields(fieldErrors(e))
    },
  })
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
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <TextInput
            label="Username"
            withAsterisk
            value={v.username}
            onChange={(e) => {
              setV({ ...v, username: e.currentTarget.value })
            }}
            error={fields.username}
            autoComplete="off"
          />
          <TextInput
            label="Display name"
            value={v.display_name ?? ''}
            onChange={(e) => {
              setV({ ...v, display_name: e.currentTarget.value })
            }}
            error={fields.display_name}
          />
          <TextInput
            label="Email"
            description="Informational; never used to link SSO identities"
            value={v.email ?? ''}
            onChange={(e) => {
              setV({ ...v, email: e.currentTarget.value })
            }}
            error={fields.email}
          />
          <Select
            label="Role"
            data={roles}
            value={v.role ?? 'viewer'}
            onChange={(r) => {
              setV({ ...v, role: (r ?? 'viewer') as Schemas['UserCreate']['role'] })
            }}
            allowDeselect={false}
            error={fields.role}
          />
        </SimpleGrid>
        <PasswordInput
          label="Initial password"
          description="Optional: leave empty for a user who signs in only with SSO. At least 12 characters."
          value={v.password ?? ''}
          onChange={(e) => {
            setV({ ...v, password: e.currentTarget.value })
          }}
          error={fields.password}
          autoComplete="new-password"
        />
        <Group justify="flex-end">
          <Button type="submit" loading={create.isPending} disabled={!v.username}>
            Add user
          </Button>
        </Group>
      </Stack>
    </form>
  )
}

function UserDetail({ user, onClose }: { user: User; onClose: () => void }) {
  const can = useCan()
  const me = useMe()
  const qc = useQueryClient()
  const isRoot = user.role === 'root'
  const isSelf = me.data?.user.id === user.id
  const [role, setRole] = useState(user.role)
  const [password, setPassword] = useState('')
  const [dialog, setDialog] = useState<'' | 'delete' | 'totp' | 'suspend'>('')
  const identities = useQuery({
    queryKey: ['users', user.id, 'identities'],
    queryFn: async () => unwrap(await api.GET('/users/{user}/identities', { params: { path: { user: user.id } } })),
  })
  const invalidate = () => qc.invalidateQueries({ queryKey: ['users'] })
  const update = useMutation({
    mutationFn: async (body: Schemas['UserUpdate']) =>
      unwrap(
        await api.PATCH('/users/{user}', {
          params: { path: { user: user.id } },
          body,
          headers: ifMatch(user.revision),
        }),
      ),
    onSuccess: async () => {
      setPassword('')
      setDialog('')
      notifySuccess('User updated')
      await invalidate()
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const resetTotp = useMutation({
    mutationFn: async () => {
      check(await api.DELETE('/users/{user}/totp', { params: { path: { user: user.id } } }))
    },
    onSuccess: async () => {
      setDialog('')
      notifySuccess('Two-factor authentication reset; the user must enroll again if required')
      await invalidate()
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const del = useMutation({
    mutationFn: async () => {
      check(await api.DELETE('/users/{user}', { params: { path: { user: user.id } } }))
    },
    onSuccess: async () => {
      notifySuccess('User deleted; their sessions and API tokens were revoked')
      await invalidate()
      onClose()
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const unlink = useMutation({
    mutationFn: async (id: string) => {
      check(await api.DELETE('/users/{user}/identities/{id}', { params: { path: { user: user.id, id } } }))
    },
    onSuccess: async () => {
      notifySuccess('Identity unlinked')
      await qc.invalidateQueries({ queryKey: ['users', user.id, 'identities'] })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const editable = can('users.write') && !isRoot
  return (
    <Stack>
      <Section title="Account">
        <DefinitionList
          items={[
            ['Username', user.username],
            ['Role', user.role],
            ['Status', <StatusBadge key="s" state={user.status} />],
            ['Two-factor', user.totp_enabled ? 'enabled' : 'not enabled'],
            [
              'Password login',
              user.has_password ? (user.local_login_enabled ? 'enabled' : 'disabled for this user') : 'no password set',
            ],
            ['Created', <Time key="c" value={user.created_at} />],
            ['Last sign-in', <Time key="l" value={user.last_login_at} />],
          ]}
        />
      </Section>
      {isRoot ? (
        <Alert variant="light" color="gray">
          The root user can only be changed by root itself (Account & security) or on the host with{' '}
          <code>wagenpulse admin reset-password</code>.
        </Alert>
      ) : null}
      {editable ? (
        <Section title="Manage" padded>
          <Stack gap="sm">
            <Group align="flex-end">
              <Select
                label="Role"
                data={roles}
                value={role}
                onChange={(r) => {
                  setRole(r ?? user.role)
                }}
                allowDeselect={false}
                style={{ flex: 1 }}
                disabled={isSelf}
              />
              <Button
                variant="default"
                disabled={role === user.role}
                loading={update.isPending}
                onClick={() => {
                  update.mutate({ role: role as Schemas['UserUpdate']['role'] })
                }}
              >
                Change role
              </Button>
            </Group>
            <Text size="xs" c="dimmed">
              Lowering the role, suspending, disabling password login or setting a password signs the user out
              everywhere. API tokens keep their scopes but never exceed the user's current role.
            </Text>
            <Group align="flex-end">
              <PasswordInput
                label="Set a new password"
                description="At least 12 characters"
                value={password}
                onChange={(e) => {
                  setPassword(e.currentTarget.value)
                }}
                style={{ flex: 1 }}
                autoComplete="new-password"
              />
              <Button
                variant="default"
                disabled={password.length < 12}
                loading={update.isPending}
                onClick={() => {
                  update.mutate({ password })
                }}
              >
                Set password
              </Button>
            </Group>
            <Switch
              label="Allow password login"
              checked={user.local_login_enabled}
              onChange={(e) => {
                update.mutate({ local_login_enabled: e.currentTarget.checked })
              }}
              disabled={update.isPending}
            />
            <Group gap="xs" mt="xs">
              {user.status === 'active' ? (
                <Button
                  variant="default"
                  color="orange"
                  onClick={() => {
                    setDialog('suspend')
                  }}
                  disabled={isSelf}
                >
                  Suspend
                </Button>
              ) : (
                <Button
                  variant="default"
                  onClick={() => {
                    update.mutate({ status: 'active' })
                  }}
                >
                  Reactivate
                </Button>
              )}
              {user.totp_enabled ? (
                <Button
                  variant="default"
                  onClick={() => {
                    setDialog('totp')
                  }}
                >
                  Reset two-factor
                </Button>
              ) : null}
              <Button
                variant="default"
                color="red"
                onClick={() => {
                  setDialog('delete')
                }}
                disabled={isSelf}
              >
                Delete
              </Button>
            </Group>
          </Stack>
        </Section>
      ) : null}
      <Section title="SSO identities">
        <QueryState query={identities}>
          {() =>
            identities.data?.length ? (
              <Table className="wp-table">
                <Table.Tbody>
                  {identities.data.map((i) => (
                    <Table.Tr key={i.id}>
                      <Table.Td>{i.provider_name}</Table.Td>
                      <Table.Td>{i.email ?? '—'}</Table.Td>
                      <Table.Td>
                        {can('users.write') ? (
                          <Button
                            size="xs"
                            variant="subtle"
                            color="red"
                            onClick={() => {
                              unlink.mutate(i.id)
                            }}
                          >
                            Unlink
                          </Button>
                        ) : null}
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            ) : (
              <EmptyBlock title="No linked identities" />
            )
          }
        </QueryState>
      </Section>
      <ConfirmDialog
        opened={dialog === 'suspend'}
        onClose={() => {
          setDialog('')
        }}
        onConfirm={() => {
          update.mutate({ status: 'suspended' })
        }}
        loading={update.isPending}
        title="Suspend user?"
        confirmLabel="Suspend"
        danger
      >
        The user is signed out and their API tokens stop working until they are reactivated.
      </ConfirmDialog>
      <ConfirmDialog
        opened={dialog === 'totp'}
        onClose={() => {
          setDialog('')
        }}
        onConfirm={() => {
          resetTotp.mutate()
        }}
        loading={resetTotp.isPending}
        title="Reset two-factor authentication?"
        confirmLabel="Reset"
      >
        Removes the user's authenticator and recovery codes. Use this when they lost their device.
      </ConfirmDialog>
      <ConfirmDialog
        opened={dialog === 'delete'}
        onClose={() => {
          setDialog('')
        }}
        onConfirm={() => {
          del.mutate()
        }}
        loading={del.isPending}
        title="Delete user?"
        confirmLabel="Delete user"
        danger
        typeToConfirm={user.username}
      >
        The user, their sessions, linked identities and API tokens are removed. Audit records keep their user ID.
      </ConfirmDialog>
    </Stack>
  )
}
