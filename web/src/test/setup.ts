import '@testing-library/jest-dom/vitest'

// jsdom lacks these browser APIs Mantine relies on.
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false,
  }),
})
class RO {
  observe() {
    /* jsdom has no layout; nothing to observe */
  }
  unobserve() {
    /* no-op */
  }
  disconnect() {
    /* no-op */
  }
}
globalThis.ResizeObserver = RO
