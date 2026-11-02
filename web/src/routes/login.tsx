import { Alert, Button, Divider, PasswordInput, PinInput, Stack, Text, TextInput } from '@mantine/core'
import { IconAlertCircle, IconClockExclamation, IconLogin } from '@tabler/icons-react'
import { useForm } from '@tanstack/react-form'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, setCsrfToken, unwrap } from '@/api/client'
import { AuthCard, safeReturnTo } from '@/components/AuthCard'
import { LoadingBlock } from '@/components/ui'
import { errorMessage } from '@/lib/notify'
import { meKey } from '@/lib/session'

interface Search {
  return_to?: string
  reason?: string
  error?: string
}

export const Route = createFileRoute('/login')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    return_to: typeof s.return_to === 'string' ? s.return_to : undefined,
    reason: typeof s.reason === 'string' ? s.reason : undefined,
    error: typeof s.error === 'string' ? s.error : undefined,
  }),
  component: Login,
})

const ssoErrors: Record<string, string> = {
  sso_not_linked:
    'No WagenPulse account is linked to that identity. Sign in with your password, then link it under Account & security.',
  sso_denied:
    'The identity provider account is not allowed to sign in here (unverified email or a domain outside the allowed list).',
  sso_already_linked: 'That identity is already linked to another WagenPulse account.',
  sso_expired: 'The sign-in attempt expired or was started in another browser. Try again.',
  account_suspended: 'Your WagenPulse account is suspended. Ask an administrator.',
  sso_failed: 'Single sign-on failed. Try again or use your password.',
  link_requires_session: 'Sign in first to link an identity.',
  forbidden: 'Single sign-on is disabled.',
  unavailable: 'The identity provider is unavailable. Try again later.',
  not_found: 'That sign-in option is not available.',
}

function Login() {
  const search = Route.useSearch()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [challenge, setChallenge] = useState('')
  const [error, setError] = useState('')
  const methods = useQuery({ queryKey: ['login-methods'], queryFn: async () => unwrap(await api.GET('/auth/methods')) })
  const setup = useQuery({ queryKey: ['setup-status'], queryFn: async () => unwrap(await api.GET('/setup/status')) })
  const returnTo = safeReturnTo(search.return_to)

  const finish = async (csrf: string) => {
    setCsrfToken(csrf)
    await qc.invalidateQueries({ queryKey: meKey })
    void navigate({ to: returnTo })
  }

  const form = useForm({
    defaultValues: { username: '', password: '' },
    onSubmit: async ({ value }) => {
      setError('')
      try {
        const res = unwrap(await api.POST('/auth/login', { body: value }))
        if (res.challenge) {
          setChallenge(res.challenge)
          return
        }
        if (res.me) await finish(res.me.csrf_token ?? '')
      } catch (e) {
        setError(errorMessage(e))
      }
    },
  })

  const [code, setCode] = useState('')
  const [verifying, setVerifying] = useState(false)
  const verify = async (value: string) => {
    setVerifying(true)
    setError('')
    try {
      const res = unwrap(await api.POST('/auth/login/totp', { body: { challenge, code: value } }))
      if (res.me) await finish(res.me.csrf_token ?? '')
    } catch (e) {
      setError(errorMessage(e))
      setCode('')
    } finally {
      setVerifying(false)
    }
  }

  if (methods.isPending || setup.isPending) return <LoadingBlock h={400} />
  if (setup.data && !setup.data.completed) {
    return (
      <AuthCard title="WagenPulse is not set up yet" subtitle="Create the root user first.">
        <Button component="a" href="/setup">
          Continue to setup
        </Button>
      </AuthCard>
    )
  }
  const m = methods.data
  if (challenge) {
    return (
      <AuthCard
        title="Two-factor verification"
        subtitle="Enter the 6-digit code from your authenticator app, or a recovery code."
      >
        {error ? (
          <Alert color="red" variant="light" icon={<IconAlertCircle size={18} />} role="alert">
            {error}
          </Alert>
        ) : null}
        <Stack align="center">
          <PinInput
            length={6}
            type="number"
            oneTimeCode

            value={code}
            onChange={setCode}
            onComplete={(v) => void verify(v)}
            aria-label="Verification code"
            disabled={verifying}
          />
        </Stack>
        <TextInput
          label="Or a recovery code"
          placeholder="xxxxx-xxxxx"
          onKeyDown={(e) => {
            if (e.key === 'Enter') void verify(e.currentTarget.value)
          }}
        />
        <Button
          variant="subtle"
          onClick={() => {
            setChallenge('')
          }}
        >
          Back
        </Button>
      </AuthCard>
    )
  }
  return (
    <AuthCard title="Sign in" subtitle="Vehicle data, collection health and event delivery in one place.">
      {search.reason === 'expired' ? (
        <Alert color="yellow" variant="light" icon={<IconClockExclamation size={18} />}>
          Your session ended. Sign in again to continue.
        </Alert>
      ) : null}
      {search.error ? (
        <Alert color="red" variant="light" icon={<IconAlertCircle size={18} />} role="alert">
          {ssoErrors[search.error] ?? ssoErrors.sso_failed}
        </Alert>
      ) : null}
      {error ? (
        <Alert color="red" variant="light" icon={<IconAlertCircle size={18} />} role="alert">
          {error}
        </Alert>
      ) : null}
      {m?.password ? (
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void form.handleSubmit()
          }}
        >
          <Stack gap="sm">
            <form.Field
              name="username"
              validators={{ onSubmit: ({ value }) => (value.trim() ? undefined : 'Enter your username') }}
            >
              {(f) => (
                <TextInput
                  label="Username"
                  autoComplete="username"

                  value={f.state.value}
                  onChange={(e) => {
                    f.handleChange(e.currentTarget.value)
                  }}
                  onBlur={f.handleBlur}
                  error={f.state.meta.errors[0]}
                />
              )}
            </form.Field>
            <form.Field
              name="password"
              validators={{ onSubmit: ({ value }) => (value ? undefined : 'Enter your password') }}
            >
              {(f) => (
                <PasswordInput
                  label="Password"
                  autoComplete="current-password"
                  value={f.state.value}
                  onChange={(e) => {
                    f.handleChange(e.currentTarget.value)
                  }}
                  onBlur={f.handleBlur}
                  error={f.state.meta.errors[0]}
                />
              )}
            </form.Field>
            <form.Subscribe selector={(s) => s.isSubmitting}>
              {(submitting) => (
                <Button type="submit" loading={submitting} leftSection={<IconLogin size={16} />} mt="xs">
                  Sign in
                </Button>
              )}
            </form.Subscribe>
          </Stack>
        </form>
      ) : null}
      {m && m.providers.length > 0 ? (
        <>
          {m.password ? <Divider label="or" labelPosition="center" /> : null}
          <Stack gap="xs">
            {m.providers.map((p) => (
              <Button
                key={p.slug}
                variant="default"
                component="a"
                href={`/auth/oidc/${encodeURIComponent(p.slug)}/start?return_to=${encodeURIComponent(returnTo)}`}
              >
                Continue with {p.display_name}
              </Button>
            ))}
          </Stack>
        </>
      ) : null}
      {m && !m.password && m.providers.length === 0 ? (
        <Text size="sm" c="dimmed">
          No sign-in method is enabled. An operator can restore access on the host with{' '}
          <code>wagenpulse admin reset-password</code>.
        </Text>
      ) : null}
    </AuthCard>
  )
}
