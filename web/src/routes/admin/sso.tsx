import {
  Alert,
  Button,
  Card,
  Checkbox,
  Code,
  CopyButton,
  Group,
  Modal,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Table,
  TagsInput,
  Text,
  TextInput,
} from '@mantine/core'
import { IconCopy, IconPlus } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, check, idempotencyKey, ifMatch, unwrap, type Provider, type Schemas } from '@/api/client'
import { ConfirmDialog } from '@/components/Confirm'
import { EmptyBlock, PageHeader, QueryState, StatusBadge } from '@/components/ui'
import { errorMessage, fieldErrors, notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'

interface Search {
  create?: number
  edit?: string
}

export const Route = createFileRoute('/admin/sso')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    create: s.create ? 1 : undefined,
    edit: typeof s.edit === 'string' ? s.edit : undefined,
  }),
  component: SSO,
})

function SSO() {
  const can = useCan()
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/admin/sso' })
  const qc = useQueryClient()
  const [deleting, setDeleting] = useState<Provider | null>(null)
  const list = useQuery({ queryKey: ['oidc-providers'], queryFn: async () => unwrap(await api.GET('/oidc-providers')) })
  const del = useMutation({
    mutationFn: async (slug: string) => {
      check(await api.DELETE('/oidc-providers/{provider}', { params: { path: { provider: slug } } }))
    },
    onSuccess: async () => {
      setDeleting(null)
      notifySuccess('Provider deleted; its linked identities were removed')
      await qc.invalidateQueries({ queryKey: ['oidc-providers'] })
    },
    onError: (e) => {
      notifyError(e, 'Could not delete the provider')
    },
  })
  const editing = list.data?.find((p) => p.slug === search.edit || p.id === search.edit)
  const close = () => void navigate({ search: {} })
  return (
    <>
      <PageHeader
        title="Single sign-on"
        description="OpenID Connect identity providers. Users sign in with a linked identity; identities are never linked by matching email."
        actions={
          can('oidc.write') ? (
            <Button leftSection={<IconPlus size={16} />} onClick={() => void navigate({ search: { create: 1 } })}>
              Add provider
            </Button>
          ) : null
        }
      />
      <Card p={0}>
        <QueryState query={list} loadingLabel="Loading providers…">
          {() =>
            list.data?.length ? (
              <Table className="wp-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Provider</Table.Th>
                    <Table.Th>Issuer</Table.Th>
                    <Table.Th>Provisioning</Table.Th>
                    <Table.Th>State</Table.Th>
                    <Table.Th />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {list.data.map((p) => (
                    <Table.Tr key={p.id}>
                      <Table.Td>
                        <Text size="sm" fw={600}>
                          {p.display_name}
                        </Text>
                        <Text size="xs" c="dimmed">
                          {p.slug}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <Text size="xs" ff="monospace">
                          {p.issuer}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        {p.provisioning === 'jit'
                          ? `create on first sign-in as ${p.default_role}`
                          : 'pre-provisioned users only'}
                      </Table.Td>
                      <Table.Td>
                        <StatusBadge state={p.enabled ? 'enabled' : 'disabled'} />
                      </Table.Td>
                      <Table.Td>
                        {can('oidc.write') ? (
                          <Group gap={4} justify="flex-end" wrap="nowrap">
                            <Button
                              size="xs"
                              variant="default"
                              onClick={() => void navigate({ search: { edit: p.slug } })}
                            >
                              Edit
                            </Button>
                            <Button
                              size="xs"
                              variant="subtle"
                              color="red"
                              onClick={() => {
                                setDeleting(p)
                              }}
                            >
                              Delete
                            </Button>
                          </Group>
                        ) : null}
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            ) : (
              <EmptyBlock
                title="No providers"
                description="Add an OpenID Connect provider (for example Authentik, Keycloak, Google or Entra ID)."
              />
            )
          }
        </QueryState>
      </Card>
      <Modal
        opened={Boolean(search.create) || Boolean(editing)}
        onClose={close}
        title={<Text fw={650}>{editing ? `Edit ${editing.display_name}` : 'Add OIDC provider'}</Text>}
        size="xl"
      >
        <ProviderForm
          key={editing?.id ?? 'new'}
          initial={editing}
          onCancel={close}
          onSubmit={async (body) => {
            if (editing)
              unwrap(
                await api.PUT('/oidc-providers/{provider}', {
                  params: { path: { provider: editing.slug } },
                  body,
                  headers: ifMatch(editing.revision),
                }),
              )
            else unwrap(await api.POST('/oidc-providers', { body, headers: idempotencyKey() }))
            await qc.invalidateQueries({ queryKey: ['oidc-providers'] })
            notifySuccess(editing ? 'Provider saved' : 'Provider added')
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
          if (deleting) del.mutate(deleting.slug)
        }}
        loading={del.isPending}
        title="Delete provider?"
        confirmLabel="Delete provider"
        danger
        typeToConfirm={deleting?.slug}
      >
        Users can no longer sign in with {deleting?.display_name}. Their linked identities are removed. WagenPulse
        refuses if this would leave the root user without a sign-in method.
      </ConfirmDialog>
    </>
  )
}

function ProviderForm({
  initial,
  onSubmit,
  onCancel,
}: {
  initial?: Provider
  onSubmit: (b: Schemas['ProviderBody']) => Promise<void>
  onCancel: () => void
}) {
  const [v, setV] = useState({
    slug: initial?.slug ?? '',
    display_name: initial?.display_name ?? '',
    issuer: initial?.issuer ?? '',
    client_id: initial?.client_id ?? '',
    scopes: initial?.scopes ?? ['openid', 'profile', 'email'],
    enabled: initial?.enabled ?? true,
    provisioning: initial?.provisioning ?? 'preprovisioned',
    default_role: initial?.default_role ?? 'viewer',
    allowed_domains: initial?.allowed_domains ?? [],
    require_verified_email: initial?.require_verified_email ?? true,
  })
  const [secretMode, setSecretMode] = useState<'stored' | 'ref'>(initial?.client_secret_ref ? 'ref' : 'stored')
  const [secret, setSecret] = useState('')
  const [secretRef, setSecretRef] = useState(initial?.client_secret_ref ?? '')
  const [clearSecret, setClearSecret] = useState(false)
  const [error, setError] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const redirect = initial?.redirect_uri ?? `${window.location.origin}/auth/oidc/callback`
  const submit = async () => {
    setBusy(true)
    setError('')
    setFields({})
    const body: Schemas['ProviderBody'] = {
      ...v,
      slug: initial ? undefined : v.slug,
      provisioning: v.provisioning,
      default_role: v.default_role,
    }
    if (secretMode === 'ref') body.client_secret_ref = secretRef.trim()
    else if (secret) body.client_secret = secret
    else if (clearSecret) body.client_secret = ''
    try {
      await onSubmit(body)
    } catch (e) {
      setError(errorMessage(e))
      setFields(fieldErrors(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
    >
      <Stack gap="sm">
        {error ? (
          <Alert color="red" variant="light" role="alert">
            {error}
          </Alert>
        ) : null}
        <Alert variant="light" color="blue">
          <Group gap="xs" wrap="nowrap">
            <Text size="sm">
              Register this redirect URI with the provider: <Code>{redirect}</Code>
            </Text>
            <CopyButton value={redirect}>
              {({ copy, copied }) => (
                <Button size="compact-xs" variant="subtle" onClick={copy} leftSection={<IconCopy size={12} />}>
                  {copied ? 'Copied' : 'Copy'}
                </Button>
              )}
            </CopyButton>
          </Group>
        </Alert>
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <TextInput
            label="Display name"
            withAsterisk
            value={v.display_name}
            onChange={(e) => {
              setV({ ...v, display_name: e.currentTarget.value })
            }}
            error={fields.display_name}
          />
          <TextInput
            label="Slug"
            withAsterisk={!initial}
            description={initial ? 'Immutable' : 'Used in URLs, e.g. authentik'}
            value={v.slug}
            disabled={Boolean(initial)}
            onChange={(e) => {
              setV({ ...v, slug: e.currentTarget.value })
            }}
            error={fields.slug}
          />
          <TextInput
            label="Issuer URL"
            withAsterisk
            value={v.issuer}
            onChange={(e) => {
              setV({ ...v, issuer: e.currentTarget.value })
            }}
            error={fields.issuer}
            placeholder="https://id.example.com/application/o/wagenpulse/"
          />
          <TextInput
            label="Client ID"
            withAsterisk
            value={v.client_id}
            onChange={(e) => {
              setV({ ...v, client_id: e.currentTarget.value })
            }}
            error={fields.client_id}
          />
        </SimpleGrid>
        <Stack gap={4}>
          <Group justify="space-between">
            <Text size="sm" fw={500}>
              Client secret
            </Text>
            <SegmentedControl
              size="xs"
              value={secretMode}
              onChange={(x) => {
                setSecretMode(x as 'stored' | 'ref')
              }}
              data={[
                { value: 'stored', label: 'Store encrypted' },
                { value: 'ref', label: 'External reference' },
              ]}
            />
          </Group>
          {secretMode === 'stored' ? (
            <TextInput
              aria-label="Client secret"
              type="password"
              placeholder={
                initial?.client_secret_set ? 'Stored — leave empty to keep' : 'Client secret (empty for public clients)'
              }
              value={secret}
              onChange={(e) => {
                setSecret(e.currentTarget.value)
              }}
              error={fields.client_secret}
              autoComplete="off"
            />
          ) : (
            <TextInput
              aria-label="Client secret reference"
              placeholder="env:WAGENPULSE_SECRET_OIDC or file:/run/secrets/oidc"
              value={secretRef}
              onChange={(e) => {
                setSecretRef(e.currentTarget.value)
              }}
              error={fields.client_secret_ref}
            />
          )}
          {initial?.client_secret_set && secretMode === 'stored' && !initial.client_secret_ref ? (
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
        <TagsInput
          label="Scopes"
          value={v.scopes}
          onChange={(s) => {
            setV({ ...v, scopes: s })
          }}
          error={fields.scopes}
        />
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <Select
            label="Provisioning"
            data={[
              { value: 'preprovisioned', label: 'Only users an admin linked' },
              { value: 'jit', label: 'Create users on first sign-in' },
            ]}
            value={v.provisioning}
            onChange={(x) => {
              setV({ ...v, provisioning: (x ?? 'preprovisioned') as typeof v.provisioning })
            }}
            allowDeselect={false}
          />
          <Select
            label="Role for new users"
            data={[
              { value: 'viewer', label: 'Viewer' },
              { value: 'operator', label: 'Operator' },
            ]}
            value={v.default_role}
            onChange={(x) => {
              setV({ ...v, default_role: (x ?? 'viewer') as typeof v.default_role })
            }}
            disabled={v.provisioning !== 'jit'}
            allowDeselect={false}
            description="Admins are never created automatically"
          />
        </SimpleGrid>
        {v.provisioning === 'jit' ? (
          <TagsInput
            label="Allowed email domains"
            description="Required for automatic creation: only verified emails in these domains get an account."
            value={v.allowed_domains}
            onChange={(d) => {
              setV({ ...v, allowed_domains: d })
            }}
            error={fields.allowed_domains}
          />
        ) : null}
        <Switch
          label="Require a verified email claim"
          checked={v.require_verified_email}
          onChange={(e) => {
            setV({ ...v, require_verified_email: e.currentTarget.checked })
          }}
        />
        <Switch
          label="Enabled"
          checked={v.enabled}
          onChange={(e) => {
            setV({ ...v, enabled: e.currentTarget.checked })
          }}
        />
        <Group justify="flex-end">
          <Button variant="default" onClick={onCancel}>
            Cancel
          </Button>
          <Button type="submit" loading={busy}>
            {initial ? 'Save provider' : 'Add provider'}
          </Button>
        </Group>
      </Stack>
    </form>
  )
}
