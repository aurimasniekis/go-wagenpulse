import { Center, Stack, Text, Title } from '@mantine/core'
import type { QueryClient } from '@tanstack/react-query'
import { createRootRouteWithContext, Navigate, Outlet, useRouterState } from '@tanstack/react-router'
import { isPublicPath, loginUrl } from '@/api/client'
import { AppLayout } from '@/components/AppLayout'
import { ErrorBlock, LoadingBlock } from '@/components/ui'
import { useMe } from '@/lib/session'
import { AnchorLink } from '@/components/links'

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: Root,
  notFoundComponent: NotFound,
})

function Root() {
  const loc = useRouterState({ select: (s) => s.location })
  const me = useMe()
  if (isPublicPath(loc.pathname)) {
    return <Outlet />
  }
  if (me.isPending) return <LoadingBlock label="Loading WagenPulse…" h={400} />
  if (me.isError) {
    return (
      <Center h="100vh" p="md">
        <ErrorBlock error={me.error} onRetry={() => void me.refetch()} />
      </Center>
    )
  }
  if (!me.data) {
    return <Navigate to={loginUrl(`${loc.pathname}${loc.searchStr}`)} replace />
  }
  if (me.data.must_enroll_totp && loc.pathname !== '/account') {
    return <Navigate to="/account" search={{ enroll: 1 }} replace />
  }
  return (
    <AppLayout me={me.data}>
      <Outlet />
    </AppLayout>
  )
}

function NotFound() {
  return (
    <Center py={80}>
      <Stack align="center" gap="xs">
        <Title order={2}>Page not found</Title>
        <Text c="dimmed">The page you opened does not exist or was moved.</Text>
        <AnchorLink to="/">Go to the fleet overview</AnchorLink>
      </Stack>
    </Center>
  )
}
