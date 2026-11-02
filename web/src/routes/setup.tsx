import { Alert, Button, Code, List, PasswordInput, Stack, Text, TextInput } from '@mantine/core'
import { IconAlertCircle } from '@tabler/icons-react'
import { useForm } from '@tanstack/react-form'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, setCsrfToken, unwrap } from '@/api/client'
import { AuthCard } from '@/components/AuthCard'
import { LoadingBlock } from '@/components/ui'
import { errorMessage, fieldErrors } from '@/lib/notify'
import { meKey } from '@/lib/session'

export const Route = createFileRoute('/setup')({ component: Setup })

function Setup() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [error, setError] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const status = useQuery({ queryKey: ['setup-status'], queryFn: async () => unwrap(await api.GET('/setup/status')) })
  const form = useForm({
    defaultValues: { token: '', username: 'root', display_name: '', password: '', confirm: '' },
    onSubmit: async ({ value }) => {
      setError('')
      setFields({})
      try {
        const me = unwrap(
          await api.POST('/setup', {
            body: {
              token: value.token.trim(),
              username: value.username,
              display_name: value.display_name || undefined,
              password: value.password,
            },
          }),
        )
        setCsrfToken(me.csrf_token)
        await qc.invalidateQueries({ queryKey: meKey })
        void navigate({ to: '/account', search: { welcome: 1 } })
      } catch (e) {
        setError(errorMessage(e))
        setFields(fieldErrors(e))
      }
    },
  })
  if (status.isPending) return <LoadingBlock h={400} />
  if (status.data?.completed) {
    return (
      <AuthCard title="Setup is complete" subtitle="The root user exists.">
        <Button component="a" href="/login">
          Go to sign in
        </Button>
      </AuthCard>
    )
  }
  return (
    <AuthCard
      title="Create the root user"
      subtitle="The root user administers WagenPulse and cannot be removed or demoted."
    >
      <Text size="sm">On the WagenPulse host, issue a one-time setup token (valid for 30 minutes):</Text>
      <Code block>wagenpulse admin setup-token</Code>
      {error ? (
        <Alert color="red" variant="light" icon={<IconAlertCircle size={18} />} role="alert">
          {error}
        </Alert>
      ) : null}
      <form
        onSubmit={(e) => {
          e.preventDefault()
          void form.handleSubmit()
        }}
      >
        <Stack gap="sm">
          <form.Field
            name="token"
            validators={{
              onSubmit: ({ value }) =>
                value.trim().startsWith('wgp_setup_') ? undefined : 'Paste the token printed by the command',
            }}
          >
            {(f) => (
              <TextInput
                label="Setup token"
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={f.state.meta.errors[0] ?? fields.token}
                autoComplete="off"
              />
            )}
          </form.Field>
          <form.Field
            name="username"
            validators={{
              onSubmit: ({ value }) =>
                /^[a-z0-9._@-]{1,64}$/i.test(value) ? undefined : '1–64 letters, digits, . _ - @',
            }}
          >
            {(f) => (
              <TextInput
                label="Username"
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={f.state.meta.errors[0] ?? fields.username}
                autoComplete="username"
              />
            )}
          </form.Field>
          <form.Field name="display_name">
            {(f) => (
              <TextInput
                label="Display name"
                placeholder="Optional"
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
              />
            )}
          </form.Field>
          <form.Field
            name="password"
            validators={{ onSubmit: ({ value }) => (value.length >= 12 ? undefined : 'At least 12 characters') }}
          >
            {(f) => (
              <PasswordInput
                label="Password"
                description="At least 12 characters; a passphrase is ideal."
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={f.state.meta.errors[0] ?? fields.password}
                autoComplete="new-password"
              />
            )}
          </form.Field>
          <form.Field
            name="confirm"
            validators={{
              onSubmitAsync: ({ value, fieldApi }) =>
                value === fieldApi.form.getFieldValue('password') ? undefined : 'Passwords do not match',
            }}
          >
            {(f) => (
              <PasswordInput
                label="Confirm password"
                value={f.state.value}
                onChange={(e) => {
                  f.handleChange(e.currentTarget.value)
                }}
                error={f.state.meta.errors[0]}
                autoComplete="new-password"
              />
            )}
          </form.Field>
          <form.Subscribe selector={(s) => s.isSubmitting}>
            {(submitting) => (
              <Button type="submit" loading={submitting} mt="xs">
                Create root user
              </Button>
            )}
          </form.Subscribe>
        </Stack>
      </form>
      <List size="xs" c="dimmed" spacing={2}>
        <List.Item>Next: enable two-factor authentication and create an API token for Terraform.</List.Item>
      </List>
    </AuthCard>
  )
}
