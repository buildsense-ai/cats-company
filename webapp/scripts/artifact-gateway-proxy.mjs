// Development-only proxy for the public gateway catalogue. Keep this separate
// from the platform /api proxy: platform credentials must never reach a gateway.
export const artifactGatewayCatalogueRoute = '^/artifact-gateway/api/apps(?:\\?|$)';

export function artifactGatewayCatalogueProxy(target) {
  return {
    target,
    changeOrigin: true,
    rewrite: (path) => path.replace(/^\/artifact-gateway(?=\/api\/apps(?:\?|$))/, ''),
    configure(proxy) {
      proxy.on('proxyReq', (request) => {
        request.removeHeader('cookie');
        request.removeHeader('authorization');
      });
      proxy.on('proxyRes', (response) => {
        delete response.headers['set-cookie'];
      });
    },
  };
}
