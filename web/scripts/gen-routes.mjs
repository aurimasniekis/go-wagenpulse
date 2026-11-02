// Regenerates src/routeTree.gen.ts without a full Vite build (the Vite plugin does the same during dev/build), so
// `pnpm typecheck` sees new routes. Resolves the generator through the router plugin, which depends on it.
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(require_resolve('@tanstack/router-plugin/package.json'))
const { Generator, getConfig } = await import(require.resolve('@tanstack/router-generator'))

function require_resolve(id) {
  return createRequire(path.join(root, 'package.json')).resolve(id)
}

const config = getConfig({ target: 'react', autoCodeSplitting: true }, root)
await new Generator({ config, root }).run()
