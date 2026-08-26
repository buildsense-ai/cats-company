# Tencent host nginx

These host-level nginx configs terminate TLS on the Tencent CVM and proxy to
the Docker stacks bound to `127.0.0.1`.

The Tencent CVM uses Let's Encrypt certificates managed by certbot:

- `api.catsco.cc`
- `app.catsco.cc`
- `catsco.cc` (including `www.catsco.cc` for the public website)
- `preview.catsco.cc` (temporary public-site preview)

They are stored under `/etc/letsencrypt/live/...` and renew automatically via
the certbot timer. The root-domain certificate must contain both
`catsco.cc` and `www.catsco.cc`. The temporary root HTTPS server is installed
separately as `/etc/nginx/sites-available/catsco-public` and redirects these
names to `preview.catsco.cc`; the preview vhost proxies to the public website
container on `127.0.0.1:28081`.
`app.catsco.cc` remains the authenticated workspace on `127.0.0.1:28080`; its
config only owns the root HTTP-to-HTTPS redirect and is not replaced during a
public-site rollout.

The temporary preview uses its own certificate and vhost at
`/etc/nginx/sites-available/catsco-preview`. It must point to a server whose
备案/主体 relationship is valid for the intended audience; adding a subdomain
does not change the registrant of `catsco.cc`.

While this temporary preview is active, both HTTP and HTTPS requests for
`catsco.cc` and `www.catsco.cc` redirect to `preview.catsco.cc`, preserving the
request path and query string. `app.catsco.cc` remains unchanged.

Before enabling the updated config, verify DNS for both root names and issue
the certificate without changing the live app certificate:

```bash
sudo certbot certonly --nginx -d catsco.cc -d www.catsco.cc
```

For the temporary preview, first create `preview.catsco.cc` in DNS pointing to
the intended preview host, then issue its separate certificate:

```bash
sudo certbot certonly --nginx -d preview.catsco.cc
```

The deployment helper will leave the existing routing unchanged until
`/etc/letsencrypt/live/preview.catsco.cc/fullchain.pem` exists and
`127.0.0.1:28081/health` succeeds.

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
