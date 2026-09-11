FROM node:22-bookworm-slim

# Installing Chromium pulls a large dependency set. The deploy hosts reach
# deb.debian.org too slowly to finish that inside the build budget, so
# deploy/remote-build-source.sh points apt at a nearby mirror through this
# argument. Leaving it empty keeps the base image's own sources, which is what
# CI and local builds use.
ARG APT_DEBIAN_MIRROR=
RUN if [ -n "${APT_DEBIAN_MIRROR}" ]; then \
      mirror="${APT_DEBIAN_MIRROR%/}"; \
      for file in /etc/apt/sources.list /etc/apt/sources.list.d/*.sources /etc/apt/sources.list.d/*.list; do \
        [ -f "$file" ] || continue; \
        sed -i \
          -e "s#https\?://deb\.debian\.org/debian-security#${mirror}/debian-security#g" \
          -e "s#https\?://security\.debian\.org/debian-security#${mirror}/debian-security#g" \
          -e "s#https\?://deb\.debian\.org/debian#${mirror}/debian#g" \
          "$file"; \
      done; \
    fi; \
    apt-get update \
    && apt-get install -y --no-install-recommends chromium fonts-noto-cjk ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev
COPY src ./src

ENV NODE_ENV=production \
    SHIMO_WORKER_HOST=0.0.0.0 \
    SHIMO_WORKER_PORT=7070 \
    CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium

RUN mkdir -p /var/lib/catsco-shimo && chown -R node:node /app /var/lib/catsco-shimo
USER node
EXPOSE 7070
CMD ["node", "src/server.mjs"]
