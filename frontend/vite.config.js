import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	server: {
		proxy: {
			// Все API запросы идут через /api/ префикс чтобы не конфликтовать с SvelteKit роутами
			'/api': {
				target: 'http://localhost:8080',
				rewrite: (path) => path.replace(/^\/api/, '')
			},
			// WebSocket чат
			'/ws': {
				target: 'ws://localhost:8080',
				ws: true,
				rewrite: (path) => path.replace(/^\/ws/, '')
			}
		}
	},
	plugins: [
		sveltekit()
	]
});
