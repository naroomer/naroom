import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// Allow e2e tests to inject a dynamic backend port via BACKEND_URL env var.
// Production default: http://localhost:8080
const backendHttp = process.env.BACKEND_URL ?? 'http://localhost:8080';
const backendWs   = backendHttp.replace(/^http/, 'ws');

// V2 dev backend: set BACKEND_URL_V2 to point at the naroom-v2-dev server.
// When unset, V2 API calls fall through to the same backend as V1 (no-op for prod).
const backendV2Http = process.env.BACKEND_URL_V2 ?? backendHttp;

export default defineConfig({
	server: {
		proxy: {
			// V2 API must be listed BEFORE the generic /api catch-all so that
			// /api/v2/* is routed to the V2 dev server (BACKEND_URL_V2) while
			// all other /api/* continue to reach the V1 backend.
			// Production: BACKEND_URL_V2 is not set, both point at the same server.
			'/api/v2': {
				target: backendV2Http,
				rewrite: (path) => path.replace(/^\/api/, '')
			},
			'/api/dev': {
				target: backendV2Http,
				rewrite: (path) => path.replace(/^\/api/, '')
			},
			// Все API запросы идут через /api/ префикс чтобы не конфликтовать с SvelteKit роутами
			'/api': {
				target: backendHttp,
				rewrite: (path) => path.replace(/^\/api/, '')
			},
			// WebSocket чат
			'/ws': {
				target: backendWs,
				ws: true,
				rewrite: (path) => path.replace(/^\/ws/, '')
			}
		}
	},
	plugins: [
		sveltekit()
	],
	ssr: {
		noExternal: ['tweetnacl']
	},
	// tweetnacl uses 'self' (browser global) which does not exist in Node.js SSR.
	// Polyfill it so that server-side rendering of pages importing tweetnacl does not crash.
	define: {
		self: 'globalThis',
	},
});
