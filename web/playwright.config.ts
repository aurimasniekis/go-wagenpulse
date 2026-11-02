import { defineConfig, devices } from '@playwright/test'
import { baseURL, stateFile } from './e2e/env'

// Runs against a throwaway instance started by scripts/e2e.sh (`make e2e`). Every page is checked in light and dark.
export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup.ts',
  timeout: 30_000,
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL,
    storageState: stateFile,
    trace: 'retain-on-failure',
    timezoneId: 'Europe/Vilnius',
  },
  projects: [
    { name: 'light', use: { ...devices['Desktop Chrome'], colorScheme: 'light' } },
    { name: 'dark', use: { ...devices['Desktop Chrome'], colorScheme: 'dark' } },
  ],
})
