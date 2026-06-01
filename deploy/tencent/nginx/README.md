# Tencent host nginx

These host-level nginx configs terminate TLS on the Tencent CVM and proxy to
the Docker stacks bound to `127.0.0.1`.

The Tencent CVM uses Let's Encrypt certificates managed by certbot:

- `api.catsco.cc`
- `app.catsco.cc`

They are stored under `/etc/letsencrypt/live/...` and renew automatically via
the certbot timer. Root-domain HTTPS is not configured here; HTTP requests for
`catsco.cc` and `www.catsco.cc` can redirect to `https://app.catsco.cc`.

Install without enabling traffic:

```bash
sudo install -o root -g root -m 644 deploy/tencent/nginx/catscompany-app.conf /etc/nginx/sites-available/catscompany-app
sudo install -o root -g root -m 644 deploy/tencent/nginx/catscompany-api.conf /etc/nginx/sites-available/catscompany-api
sudo nginx -t
```

Enable on the host:

```bash
sudo ln -sfn /etc/nginx/sites-available/catscompany-app /etc/nginx/sites-enabled/catscompany-app
sudo ln -sfn /etc/nginx/sites-available/catscompany-api /etc/nginx/sites-enabled/catscompany-api
sudo nginx -t
sudo systemctl reload nginx
```
