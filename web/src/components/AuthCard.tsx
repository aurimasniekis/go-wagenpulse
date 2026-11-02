import { Center, Paper, Stack, Text } from '@mantine/core'
import type { ReactNode } from 'react'

/** Centered card used by the sign-in and setup pages. */
export function AuthCard({ title, subtitle, children }: { title: string; subtitle?: ReactNode; children: ReactNode }) {
  return (
    <Center
      mih="100vh"
      p="md"
      style={{ background: 'linear-gradient(160deg, var(--wp-bg) 0%, var(--wp-surface2) 100%)' }}
    >
      <Stack w="100%" maw={420} gap="lg">
        <Stack gap={6} align="center">
          <svg width="44" height="44" viewBox="0 0 32 32" aria-hidden="true">
            <rect width="32" height="32" rx="6" fill="#193549" />
            <path
              d="M5 18h5l3-8 5 14 3-9 2 3h4"
              fill="none"
              stroke="#ffc600"
              strokeWidth="2.6"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
          <Text fw={700} size="xl">
            Wagen<span style={{ color: 'var(--wp-accent-text)' }}>Pulse</span>
          </Text>
        </Stack>
        <Paper withBorder p="xl" radius="md" bg="var(--wp-surface)" shadow="sm">
          <Stack gap="md">
            <div>
              <Text component="h1" fw={650} size="lg" m={0}>
                {title}
              </Text>
              {subtitle ? (
                <Text size="sm" c="dimmed" mt={4}>
                  {subtitle}
                </Text>
              ) : null}
            </div>
            {children}
          </Stack>
        </Paper>
      </Stack>
    </Center>
  )
}

/** Accepts only same-origin relative paths for post-login navigation. */
export function safeReturnTo(v: unknown): string {
  return typeof v === 'string' && v.startsWith('/') && !v.startsWith('//') && !v.startsWith('/\\') ? v : '/'
}
