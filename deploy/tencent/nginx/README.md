# Tencent host nginx

These host-level nginx configs terminate TLS on the Tencent CVM and proxy to
the Docker stacks bound to `127.0.0.1`.

The Tencent CVM uses Let's Encrypt certificates managed by certbot:

- `api.catsco.cc`
- `app.catsco.cc`
- `catsco.cn` (including `www.catsco.cn` for the public website)
- `catsco.cc` (including `www.catsco.cc` for the public website)
- `preview.catsco.cc` (internal preview entry)
- `wecom.catsco.cn` (WeCom callback and authenticated Agent API proxy)

They are stored under `/etc/letsencrypt/live/...` and renew automatically via
the certbot timer. The `catsco.cn` certificate must contain both `catsco.cn`
and `www.catsco.cn`; the `catsco.cc` certificate must contain both `catsco.cc`
and `www.catsco.cc`. The public HTTPS server is installed separately as
`/etc/nginx/sites-available/catsco-public`: `www.catsco.cn` (primary) and
`www.catsco.cc` (alternate) proxy to the public website container on
`127.0.0.1:28081`, while the root domains `catsco.cn` and `catsco.cc` redirect
to their `www` host, preserving the request path and query string.
`app.catsco.cc` remains the authenticated workspace on `127.0.0.1:28080`; its
config only owns the root `.cc` HTTP-to-HTTPS redirect and is not replaced
during a public-site rollout.

`preview.catsco.cc` keeps its own certificate and vhost at
`/etc/nginx/sites-available/catsco-preview` as an internal preview entry.

Before enabling the updated config, verify DNS for `catsco.cn` and
`www.catsco.cn` and extend the `catsco.cn` certificate without touching the
live app certificate. Keep every existing SAN (`app.catsco.cn`,
`api.catsco.cn`, `relay.catsco.cn`) and add the two root names. Prefer DNS-01
through the artifact-gateway hook (`deploy/prod/ops/dns01-certbot-hook.mjs`,
zone selected via `CATSCO_ARTIFACT_DNS_ZONE`); the port-80 vhost also exposes
`/.well-known/acme-challenge/` under `/var/www/html` for HTTP-01.

For the internal preview, first create `preview.catsco.cc` in DNS pointing to
the intended preview host, then issue its separate certificate:

```bash
sudo certbot certonly --nginx -d preview.catsco.cc
```

The WeCom middleware uses a separate HTTPS vhost on the CatsCompany gateway.
Create `wecom.catsco.cn` in the Volcengine DNS zone, issue its independent
certificate, then let the production helper install the repository config:

```bash
node deploy/prod/ops/ensure-wecom-dns.mjs
sudo certbot certonly --nginx -d wecom.catsco.cn
sudo deploy/prod/ensure-wecom-agent-nginx.sh /srv/catscompany-prod
```

The vhost exposes only `/health`, `/wecom/callback`, and `/api/agent/*`. The
callback authenticates with the WeCom signature protocol; Agent endpoints
still require their API key. Traffic reaches the Foshan middleware over the
WireGuard address `10.254.0.2:12345`.

The deployment helper leaves the existing routing unchanged until the
`catsco.cn` certificate exists and covers `www.catsco.cn`, the `catsco.cc`
certificate exists, and `127.0.0.1:28081/health` succeeds.

Then verify `127.0.0.1:28081/health`, install the independent public config,
run `sudo nginx -t`, and reload nginx. If the certificate or website container
is not ready, keep the previous redirect config in place. The production
helper performs these checks and fails closed.

Install without enabling traffic:

```bash
sudo install -o root -g root -m 644 deploy/tencent/nginx/catscompany-app.conf /etc/nginx/sites-available/catscompany-app
sudo install -o root -g root -m 644 deploy/tencent/nginx/catsco-public.conf /etc/nginx/sites-available/catsco-public
sudo install -o root -g root -m 644 deploy/tencent/nginx/catsco-preview.conf /etc/nginx/sites-available/catsco-preview
sudo install -o root -g root -m 644 deploy/tencent/nginx/catscompany-api.conf /etc/nginx/sites-available/catscompany-api
sudo install -o root -g root -m 644 deploy/tencent/nginx/catsco-safe-log.conf /etc/nginx/conf.d/catsco-safe-log.conf
sudo nginx -t
```

Enable on the host:

```bash
sudo ln -sfn /etc/nginx/sites-available/catscompany-app /etc/nginx/sites-enabled/catscompany-app
sudo ln -sfn /etc/nginx/sites-available/catsco-public /etc/nginx/sites-enabled/catsco-public
sudo ln -sfn /etc/nginx/sites-available/catsco-preview /etc/nginx/sites-enabled/catsco-preview
sudo ln -sfn /etc/nginx/sites-available/catscompany-api /etc/nginx/sites-enabled/catscompany-api
sudo nginx -t
sudo systemctl reload nginx
```
