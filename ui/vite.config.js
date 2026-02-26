import { defineConfig } from 'vite';
import { sveltekit } from '@sveltejs/kit/vite';
import { SvelteKitPWA } from '@vite-pwa/sveltekit';

export default defineConfig({
	plugins: [
		sveltekit(),
		SvelteKitPWA({
			disable: false,
			registerType: 'autoUpdate',
			strategies: 'generateSW',
			includeAssets: ['icon-192.png', 'icon-512.png', 'apple-touch-icon.png', 'favicon.png'],
			devOptions: {
				enabled: false,
				type: 'module'
			},
			manifest: {
				id: '/app',
				name: 'Elok',
				short_name: 'Elok',
				description: 'Local-first agent host with channels and plugins.',
				start_url: '/app',
				scope: '/app/',
				theme_color: '#161b2b',
				background_color: '#111626',
				display: 'standalone',
				icons: [
					{
						src: 'icon-192.png',
						sizes: '192x192',
						type: 'image/png'
					},
					{
						src: 'icon-512.png',
						sizes: '512x512',
						type: 'image/png'
					},
					{
						src: 'icon-512.png',
						sizes: '512x512',
						type: 'image/png',
						purpose: 'any maskable'
					}
				]
			},
			workbox: {
				globPatterns: ['**/*.{js,css,html,ico,png,svg,woff,woff2,webmanifest}'],
				maximumFileSizeToCacheInBytes: 5 * 1024 * 1024
			}
		})
	],
	server: {
		port: 5173,
		host: '0.0.0.0'
	}
});
