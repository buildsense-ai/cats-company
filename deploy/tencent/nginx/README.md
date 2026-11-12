# Tencent host nginx

These host-level nginx configs terminate TLS on the Tencent CVM and proxy to
the Docker stacks bound to `127.0.0.1`.

The Tencent CVM uses Let's Encrypt certificates managed by certbot:

- `api.catsco.cc`
- `app.catsco.cc`
- `catsco.cn` (including `www.catsco.cn` for the public website)
- `catsco.cc` (including `www.catsco.cc` for the public website)
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

Before enabling the updated config, verify DNS for `catsco.cn` and
`www.catsco.cn` and extend the `catsco.cn` certificate without touching the
live app certificate. Keep every existing SAN (`app.catsco.cn`,
`api.catsco.cn`, `relay.catsco.cn`) and add the two root names. Prefer DNS-01
through the Volcengine hook already installed on the host
(`/usr/local/sbin/volcdns-certbot-hook`, with `VOLC_DNS_ZONE=catsco.cn`); the
vhosts also expose `/.well-known/acme-challenge/` under `/var/www/letsencrypt`
(the certbot webroot) so HTTP-01 keeps working as a fallback.

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

For a new host, the commands below install the complete vhost templates. On the
existing production host, do not overwrite either app vhost: those files include
the independently managed uploads snippet and contain host-specific settings.

Install without enabling traffic:

```bash
sudo install -o root -g root -m 644 deploy/tencent/nginx/catscompany-app.conf /etc/nginx/sites-available/catscompany-app
sudo install -o root -g root -m 644 deploy/tencent/nginx/catscompany-app-cn.conf /etc/nginx/sites-available/catscompany-app-cn
sudo install -o root -g root -m 644 deploy/tencent/nginx/catsco-public.conf /etc/nginx/sites-available/catsco-public
sudo install -o root -g root -m 644 deploy/tencent/nginx/catscompany-api.conf /etc/nginx/sites-available/catscompany-api
sudo install -o root -g root -m 644 deploy/tencent/nginx/catsco-safe-log.conf /etc/nginx/conf.d/catsco-safe-log.conf
sudo install -o root -g root -m 644 deploy/tencent/nginx/uploads-zos.conf /etc/nginx/snippets/uploads-zos.conf
sudo install -o root -g root -m 644 deploy/tencent/nginx/uploads-zos-map.conf /etc/nginx/conf.d/uploads-zos-map.conf
sudo nginx -t
```

Both app vhosts keep the ZOS read route temporarily for legacy clients that
send no `Authorization` header. Requests with an `Authorization` header are
internally routed to `@uploads_authenticated`, which delegates current
persisted-message authorization to the application. That named location has
no ZOS fallback: application denials and errors are returned to the caller.

The existing `?preview` and `?download` query routes also continue to use the
application fallback, including for legacy clients.

The anonymous ZOS route remains a bearer-link compatibility path, not a
privacy boundary. Keep this split only during client migration; remove the
ZOS route and its maps after legacy clients are retired. The map file installed
above defines `$uploads_disp` and `$uploads_csp`, used by the ZOS route.

After a production release has bundled the helper and snippet, the operator
must record the legacy-route retirement date and explicitly run:

```bash
sudo -n /srv/catscompany-prod/compose/ensure-uploads-compat-nginx.sh
```

The helper leaves `/etc/nginx/sites-available/catscompany-app` unchanged after
confirming its existing snippet include. It backs up the `.cn` app vhost and
replaces only its unique inline `/uploads/` location with the include; an
ambiguous or duplicate route fails closed. It also backs up and updates the
snippet, then validates with `nginx -t` and reloads Nginx. Validation or reload
failure restores both the prior `.cn` vhost and snippet. The production workflow
packages these files but does not execute the helper, so merge/deploy alone does
not change attachment routing. Docker Compose deployments continue to route
uploads directly to the application.

Retire the anonymous ZOS route after the supported XiaoBa client floor has
moved to authenticated downloads and logs show no required legacy traffic for
the agreed observation window. Remove the ZOS route and its maps by the
recorded retirement date; do not extend the bridge without an explicit review.

Enable on the host:

```bash
sudo ln -sfn /etc/nginx/sites-available/catscompany-app /etc/nginx/sites-enabled/catscompany-app
sudo ln -sfn /etc/nginx/sites-available/catscompany-app-cn /etc/nginx/sites-enabled/catscompany-app-cn
sudo ln -sfn /etc/nginx/sites-available/catsco-public /etc/nginx/sites-enabled/catsco-public
sudo ln -sfn /etc/nginx/sites-available/catscompany-api /etc/nginx/sites-enabled/catscompany-api
sudo nginx -t
sudo systemctl reload nginx
```
