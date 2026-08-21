import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { VitePWA } from 'vite-plugin-pwa';

const backendTarget = process.env.VITE_BACKEND_TARGET || 'http://localhost:6061';
const localXiaobaTarget = 'http://127.0.0.1:3800';

const proxy = {
  '/local-xiaoba': {
    target: localXiaobaTarget,
    rewrite: (path) => path.replace(/^\/local-xiaoba/, ''),
  },
  '/api/stt/realtime': {
    target: backendTarget,
    ws: true,
  },
  '/api': backendTarget,
  '/local': backendTarget,
  '/uploads': backendTarget,
  '/v0': {
    target: backendTarget,
    ws: true,
  },
};

export default defineConfig({
  plugins: [
    react(),
    VitePWA({
      strategies: 'injectManifest',
      srcDir: 'src',
      filename: 'sw.js',
      registerType: 'prompt',
      injectRegister: null,
      includeAssets: [
        'pwa-192x192.png',
        'pwa-512x512.png',
        'pwa-maskable-512x512.png',
        'pwa-notification-badge-96x96.png',
      ],
      manifest: {
        name: 'CatsCo',
        short_name: 'CatsCo',
        lang: 'zh-CN',
        start_url: '/',
        scope: '/',
        display: 'standalone',
        theme_color: '#f8f8f8',
        background_color: '#111827',
        icons: [
          {
            src: '/pwa-192x192.png',
            sizes: '192x192',
            type: 'image/png',
            purpose: 'any',
          },
          {
            src: '/pwa-512x512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'any',
          },
          {
            src: '/pwa-maskable-512x512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'maskable',
          },
        ],
      },
      injectManifest: {
        // Navigation HTML always comes from the network so a cached document
        // cannot reference an outdated application bundle. The offline page
        // remains available when navigation cannot reach the service.
        globPatterns: [
          'assets/index-*.{js,css}',
          'assets/workbox-window.*.js',
          'offline.html',
        ],
        maximumFileSizeToCacheInBytes: 4 * 1024 * 1024,
      },
      devOptions: {
        enabled: false,
      },
    }),
  ],
  build: {
    outDir: 'build',
  },
  server: {
    proxy,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/test-setup.js',
  },
});
