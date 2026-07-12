import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// Allow e2e tests to inject a dynamic backend port via BACKEND_URL env var.
// Production default: http://localhost:8080
const backendHttp = process.env.BACKEND_URL ?? 'http://localhost:8080';
const backendWs   = backendHttp.replace(/^http/, 'ws');

export default defineConfig({
	server: {
		proxy: {
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
