import '@fontsource-variable/inter'
import '@fontsource-variable/jetbrains-mono'
import '@mantine/core/styles.css'
import '@mantine/notifications/styles.css'
import '@mantine/dropzone/styles.css'
import './theme/global.css'

import { MantineProvider } from '@mantine/core'
import { Notifications } from '@mantine/notifications'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { lazy, StrictMode, Suspense } from 'react'
import { createRoot } from 'react-dom/client'
import { queryClient, router } from './router'
import { cssVariablesResolver, theme } from './theme/theme'

// Devtools are loaded only in development builds and tree-shaken from production.
const Devtools = import.meta.env.DEV ? lazy(() => import('./devtools')) : () => null

const nonce = document.querySelector<HTMLMetaElement>('meta[name="wagenpulse-csp-nonce"]')?.content

const root = document.getElementById('root')
if (root) {
  createRoot(root).render(
    <StrictMode>
      <MantineProvider
        theme={theme}
        cssVariablesResolver={cssVariablesResolver}
        defaultColorScheme="auto"
        getStyleNonce={() => nonce ?? ''}
      >
        <Notifications position="bottom-right" limit={4} />
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
          <Suspense fallback={null}>
            <Devtools />
          </Suspense>
        </QueryClientProvider>
      </MantineProvider>
    </StrictMode>,
  )
}
