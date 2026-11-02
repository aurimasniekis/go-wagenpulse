/* eslint-disable no-restricted-imports -- this module is the single, development-only entry point for devtools */
import { ReactQueryDevtools } from '@tanstack/react-query-devtools'
import { TanStackRouterDevtools } from '@tanstack/react-router-devtools'
import { router } from './router'

export default function Devtools() {
  return (
    <>
      <ReactQueryDevtools buttonPosition="bottom-left" />
      <TanStackRouterDevtools router={router} position="bottom-right" />
    </>
  )
}
