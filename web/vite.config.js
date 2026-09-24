import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Dev: `npm run dev` proxies the WebSocket and artifact downloads to the Go backend.
export default defineConfig({
  plugins: [svelte()],
  build: { outDir: 'dist', emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 900 },
  server: {
    port: 5173,
    proxy: {
      '/ws': { target: 'ws://127.0.0.1:7777', ws: true },
      '/artifacts': 'http://127.0.0.1:7777',
    },
  },
});
