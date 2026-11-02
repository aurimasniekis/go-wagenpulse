import {
  Alert,
  Badge,
  Button,
  Code,
  Group,
  List,
  Modal,
  PasswordInput,
  PinInput,
  SimpleGrid,
  Stack,
  Table,
  Text,
  TextInput,
} from '@mantine/core'
import { IconLink, IconLogout, IconShieldCheck } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { api, check, unwrap } from '@/api/client'
import { ConfirmDialog } from '@/components/Confirm'
import { DefinitionList, EmptyBlock, PageHeader, QueryState, SecretOnce, Section, Time } from '@/components/ui'
import { errorMessage, notifyError, notifySuccess } from '@/lib/notify'
import { meKey, useMe } from '@/lib/session'

interface Search {
  enroll?: number
  welcome?: number
  linked?: number
}

export const Route = createFileRoute('/account')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    enroll: s.enroll ? 1 : undefined,
    welcome: s.welcome ? 1 : undefined,
    linked: s.linked ? 1 : undefined,
  }),
  component: Account,
})

function Account() {
  const search = Route.useSearch()
  const me = useMe()
  const user = me.data?.user
  if (!me.data || !user) return null
  return (
    <>
      <PageHeader
        title="Account & security"
        description="Your sign-in methods, two-factor authentication and active sessions."
      />
      {search.welcome ? (
        <Alert color="green" variant="light" mb="lg" title="Welcome to WagenPulse">
          The root user is ready. Next: enable two-factor authentication below, add a vehicle, then create an API token
          if you manage WagenPulse with Terraform.
        </Alert>
      ) : null}
      {me.data.must_enroll_totp || search.enroll ? (
        <Alert color="yellow" variant="light" mb="lg" title="Two-factor authentication required">
          Your role requires two-factor authentication. Enroll an authenticator app to continue using WagenPulse.
        </Alert>
      ) : null}
      {search.linked ? (
        <Alert color="green" variant="light" mb="lg">
          The identity was linked. You can now sign in with it.
        </Alert>
      ) : null}
      <SimpleGrid cols={{ base: 1, lg: 2 }}>
        <Section title="Profile">
          <DefinitionList
            items={[
              ['Username', user.username],
              ['Display name', user.display_name || '—'],
              ['Email', user.email || '—'],
              [
                'Role',
                <Badge key="r" variant="light">
                  {user.role}
                </Badge>,
              ],
              ['Signed in with', me.data.method],
              ['Last sign-in', <Time key="l" value={user.last_login_at} />],
            ]}
          />
        </Section>
        <Stack>
          <TotpSection enabled={user.totp_enabled} />
          {user.has_password && user.local_login_enabled ? <PasswordSection /> : null}
        </Stack>
      </SimpleGrid>
      <Stack mt="lg">
        <IdentitiesSection />
        <SessionsSection />
      </Stack>
    </>
  )
}

function TotpSection({ enabled }: { enabled: boolean }) {
  const qc = useQueryClient()
  const [enrollment, setEnrollment] = useState<{ secret: string; uri: string } | null>(null)
  const [codes, setCodes] = useState<string[] | null>(null)
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [disabling, setDisabling] = useState(false)
  const [disableCode, setDisableCode] = useState('')
  const start = useMutation({
    mutationFn: async () => unwrap(await api.POST('/me/totp')),
    onSuccess: setEnrollment,
    onError: (e) => {
      notifyError(e)
    },
  })
  const confirm = useMutation({
    mutationFn: async (c: string) => unwrap(await api.POST('/me/totp/confirm', { body: { code: c } })),
    onSuccess: async (r) => {
      setEnrollment(null)
      setCodes(r.recovery_codes)
      await qc.invalidateQueries({ queryKey: meKey })
    },
    onError: (e) => {
      setError(errorMessage(e))
      setCode('')
    },
  })
  const disable = useMutation({
    mutationFn: async () => {
      check(await api.DELETE('/me/totp', { params: { query: { code: disableCode } } }))
    },
    onSuccess: async () => {
      setDisabling(false)
      setDisableCode('')
      notifySuccess('Two-factor authentication disabled')
      await qc.invalidateQueries({ queryKey: meKey })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  return (
    <Section title="Two-factor authentication" padded>
      {codes ? (
        <SecretOnce
          label="Recovery codes"
          value={codes.join('  ')}
          warning="Store these recovery codes somewhere safe. Each works once if you lose your authenticator. They are not shown again."
        />
      ) : enrollment ? (
        <Stack gap="sm">
          <Text size="sm">
            Add WagenPulse to your authenticator app with this setup key (or open the link on a device with an
            authenticator), then enter the 6-digit code.
          </Text>
          <Text size="sm" fw={600}>
            Setup key
          </Text>
          <Code block style={{ letterSpacing: '0.1em' }} data-testid="totp-secret">
            {enrollment.secret.replace(/(.{4})/g, '$1 ').trim()}
          </Code>
          <Text size="xs" c="dimmed" style={{ wordBreak: 'break-all' }}>
            <a href={enrollment.uri}>{enrollment.uri}</a>
          </Text>
          {error ? (
            <Alert color="red" variant="light" role="alert">
              {error}
            </Alert>
          ) : null}
          <PinInput
            length={6}
            type="number"
            oneTimeCode
            value={code}
            onChange={setCode}
            onComplete={(v) => {
              confirm.mutate(v)
            }}
            aria-label="Verification code"
            disabled={confirm.isPending}
          />
          <Button
            variant="subtle"
            onClick={() => {
              setEnrollment(null)
            }}
          >
            Cancel
          </Button>
        </Stack>
      ) : enabled ? (
        <Group justify="space-between">
          <Group gap="xs">
            <IconShieldCheck size={20} color="var(--wp-success)" />
            <Text size="sm">Enabled. Sign-in asks for a code from your authenticator app.</Text>
          </Group>
          <Button
            variant="default"
            color="red"
            onClick={() => {
              setDisabling(true)
            }}
          >
            Disable
          </Button>
        </Group>
      ) : (
        <Group justify="space-between">
          <Text size="sm">Not enabled. Protect your account with an authenticator app (TOTP).</Text>
          <Button
            onClick={() => {
              start.mutate()
            }}
            loading={start.isPending}
          >
            Enable
          </Button>
        </Group>
      )}
      <Modal
        opened={disabling}
        onClose={() => {
          setDisabling(false)
        }}
        title={<Text fw={650}>Disable two-factor authentication</Text>}
      >
        <Stack>
          <Text size="sm">Enter a current code from your authenticator (or a recovery code) to confirm.</Text>
          <TextInput
            label="Code"
            value={disableCode}
            onChange={(e) => {
              setDisableCode(e.currentTarget.value)
            }}
            autoComplete="one-time-code"
          />
          <Group justify="flex-end">
            <Button
              variant="default"
              onClick={() => {
                setDisabling(false)
              }}
            >
              Cancel
            </Button>
            <Button
              color="red"
              loading={disable.isPending}
              disabled={!disableCode}
              onClick={() => {
                disable.mutate()
              }}
            >
              Disable
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Section>
  )
}

function PasswordSection() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const change = useMutation({
    mutationFn: async () => {
      check(await api.PUT('/me/password', { body: { current_password: current, new_password: next } }))
    },
    onSuccess: () => {
      setCurrent('')
      setNext('')
      setConfirm('')
      setError('')
      notifySuccess('Password changed; your other sessions were signed out')
    },
    onError: (e) => {
      setError(errorMessage(e))
    },
  })
  const mismatch = confirm.length > 0 && confirm !== next
  return (
    <Section title="Password" padded>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          change.mutate()
        }}
      >
        <Stack gap="sm">
          {error ? (
            <Alert color="red" variant="light" role="alert">
              {error}
            </Alert>
          ) : null}
          <PasswordInput
            label="Current password"
            value={current}
            onChange={(e) => {
              setCurrent(e.currentTarget.value)
            }}
            autoComplete="current-password"
          />
          <SimpleGrid cols={{ base: 1, sm: 2 }}>
            <PasswordInput
              label="New password"
              description="At least 12 characters"
              value={next}
              onChange={(e) => {
                setNext(e.currentTarget.value)
              }}
              autoComplete="new-password"
            />
            <PasswordInput
              label="Confirm"
              value={confirm}
              onChange={(e) => {
                setConfirm(e.currentTarget.value)
              }}
              error={mismatch ? 'Passwords do not match' : undefined}
              autoComplete="new-password"
            />
          </SimpleGrid>
          <Group justify="flex-end">
            <Button
              type="submit"
              loading={change.isPending}
              disabled={!current || next.length < 12 || mismatch || !confirm}
            >
              Change password
            </Button>
          </Group>
        </Stack>
      </form>
    </Section>
  )
}

function IdentitiesSection() {
  const qc = useQueryClient()
  const ids = useQuery({ queryKey: ['me', 'identities'], queryFn: async () => unwrap(await api.GET('/me/identities')) })
  const methods = useQuery({ queryKey: ['login-methods'], queryFn: async () => unwrap(await api.GET('/auth/methods')) })
  const [unlink, setUnlink] = useState<string | null>(null)
  const del = useMutation({
    mutationFn: async (id: string) => {
      check(await api.DELETE('/me/identities/{id}', { params: { path: { id } } }))
    },
    onSuccess: async () => {
      setUnlink(null)
      notifySuccess('Identity unlinked')
      await qc.invalidateQueries({ queryKey: ['me', 'identities'] })
    },
    onError: (e) => {
      notifyError(e, 'Could not unlink')
    },
  })
  const linked = new Set(ids.data?.map((i) => i.provider_slug))
  const linkable = (methods.data?.providers ?? []).filter((p) => !linked.has(p.slug))
  return (
    <Section
      title="Single sign-on identities"
      description="Linked identities can sign in to this account. Identities are never linked automatically by email."
      actions={linkable.map((p) => (
        <Button
          key={p.slug}
          size="xs"
          variant="default"
          leftSection={<IconLink size={14} />}
          component="a"
          href={`/auth/oidc/${encodeURIComponent(p.slug)}/start?intent=link&return_to=${encodeURIComponent('/account')}`}
        >
          Link {p.display_name}
        </Button>
      ))}
    >
      <QueryState query={ids}>
        {() =>
          ids.data?.length ? (
            <Table className="wp-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Provider</Table.Th>
                  <Table.Th>Email</Table.Th>
                  <Table.Th>Linked</Table.Th>
                  <Table.Th>Last sign-in</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {ids.data.map((i) => (
                  <Table.Tr key={i.id}>
                    <Table.Td>{i.provider_name}</Table.Td>
                    <Table.Td>{i.email ?? '—'}</Table.Td>
                    <Table.Td>
                      <Time value={i.linked_at} />
                    </Table.Td>
                    <Table.Td>
                      <Time value={i.last_login_at} relative />
                    </Table.Td>
                    <Table.Td>
                      <Button
                        size="xs"
                        variant="subtle"
                        color="red"
                        onClick={() => {
                          setUnlink(i.id)
                        }}
                      >
                        Unlink
                      </Button>
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          ) : (
            <EmptyBlock
              title="No linked identities"
              description={
                linkable.length ? 'Use a Link button above to add one.' : 'No single sign-on provider is enabled.'
              }
            />
          )
        }
      </QueryState>
      <ConfirmDialog
        opened={Boolean(unlink)}
        onClose={() => {
          setUnlink(null)
        }}
        onConfirm={() => {
          if (unlink) del.mutate(unlink)
        }}
        loading={del.isPending}
        title="Unlink identity?"
        confirmLabel="Unlink"
        danger
      >
        You will no longer be able to sign in with this identity. If it is your only sign-in method, you are locked out
        until an administrator sets a password for you. The root user's last usable sign-in method cannot be removed.
      </ConfirmDialog>
    </Section>
  )
}

function SessionsSection() {
  const qc = useQueryClient()
  const sessions = useQuery({
    queryKey: ['me', 'sessions'],
    queryFn: async () => unwrap(await api.GET('/me/sessions')),
  })
  const revoke = useMutation({
    mutationFn: async (id: string) => {
      check(await api.DELETE('/me/sessions/{id}', { params: { path: { id } } }))
    },
    onSuccess: async () => {
      notifySuccess('Session signed out')
      await qc.invalidateQueries({ queryKey: ['me', 'sessions'] })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  return (
    <Section
      title="Active sessions"
      description="Browsers signed in to your account. Signing out a session takes effect immediately."
    >
      <QueryState query={sessions}>
        {() => (
          <Table className="wp-table">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Device</Table.Th>
                <Table.Th>Method</Table.Th>
                <Table.Th>IP</Table.Th>
                <Table.Th>Last active</Table.Th>
                <Table.Th>Expires</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(sessions.data ?? []).map((s) => (
                <Table.Tr key={s.id}>
                  <Table.Td>
                    <Text size="sm" lineClamp={1} maw={340}>
                      {s.user_agent || 'Unknown browser'}
                    </Text>
                    {s.current ? (
                      <Badge size="xs" variant="light" color="green">
                        this session
                      </Badge>
                    ) : null}
                  </Table.Td>
                  <Table.Td>{s.method}</Table.Td>
                  <Table.Td>
                    <Text size="sm" ff="monospace">
                      {s.ip ?? '—'}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Time value={s.last_seen_at} relative />
                  </Table.Td>
                  <Table.Td>
                    <Time value={s.expires_at} relative />
                  </Table.Td>
                  <Table.Td>
                    {!s.current ? (
                      <Button
                        size="xs"
                        variant="subtle"
                        color="red"
                        leftSection={<IconLogout size={14} />}
                        loading={revoke.isPending && revoke.variables === s.id}
                        onClick={() => {
                          revoke.mutate(s.id)
                        }}
                      >
                        Sign out
                      </Button>
                    ) : null}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </QueryState>
      <List size="xs" c="dimmed" px="lg" pb="md" mt="xs">
        <List.Item>Changing your password signs out every other session.</List.Item>
      </List>
    </Section>
  )
}
