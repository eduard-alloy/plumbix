import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The build lands in dist/, which the Go binary embeds. emptyOutDir is off
// so dist/.gitkeep survives; hashed asset names keep stale files harmless.
export default defineConfig({
  plugins: [react()],
  build: { outDir: 'dist', emptyOutDir: false, sourcemap: false },
})
