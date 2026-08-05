import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    // Rendering a page through antd/pro-components in jsdom costs seconds of
    // CPU, and a two-core box runs the whole suite in a single worker, so a
    // page test that takes ~2s on its own can take well over 5s during a full
    // run. The 5s default turned that into a flaky "Test timed out in 5000ms"
    // with no assertion detail (hit on PersonalNotificationTargets and
    // Connectors); keep enough headroom that a timeout means a hung test
    // rather than a busy machine.
    testTimeout: 30_000,
    hookTimeout: 30_000,
  },
})
