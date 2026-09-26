import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Dev: `npm run dev` proxies the WebSocket and artifact downloads to the Go backend.
const backend = process.env.PRISM_BACKEND || '127.0.0.1:7777';

export default defineConfig({
  plugins: [svelte()],
  build: { outDir: 'dist', emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 900 },
  server: {
    port: 5173,
    proxy: {
      // the backend only accepts its own origin, so present it as one (the dev page itself is on :5173)
      '/ws': { target: `ws://${backend}`, ws: true, changeOrigin: true, headers: { origin: `http://${backend}` } },
      '/artifacts': `http://${backend}`,
    },
  },
});
