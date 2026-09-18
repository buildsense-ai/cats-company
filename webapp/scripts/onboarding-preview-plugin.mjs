// A separate development document keeps preview state out of real authentication.
export function onboardingPreviewPlugin() {
  return {
    name: 'local-onboarding-preview',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use(async (request, response, next) => {
        if (request.url?.split('?')[0] !== '/__dev/onboarding') return next();
        const peer = request.socket.remoteAddress;
        if (!['127.0.0.1', '::1', '::ffff:127.0.0.1'].includes(peer)) {
          response.statusCode = 403;
          response.end('Local preview only');
          return;
        }
        try {
          const html = await server.transformIndexHtml('/__dev/onboarding', `<!doctype html>
<html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>CatsCo · 本地引导预览</title></head>
<body><div id="root"></div><script type="module" src="/src/dev/onboarding-preview.jsx"></script></body></html>`);
          response.setHeader('Content-Type', 'text/html; charset=utf-8');
          response.setHeader('Cache-Control', 'no-store');
          response.end(html);
        } catch (error) { next(error); }
      });
    },
  };
}
