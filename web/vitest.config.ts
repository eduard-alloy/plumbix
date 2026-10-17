import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: false,
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    // CSS is stubbed except the token file, which StatePill.test.tsx reads as text.
    css: { include: [/tokens\.css/] },
    // Plumbix validates itself with six containers in parallel; a loaded host made a 5 s wait time out once.
    testTimeout: 15000,
  },
})
