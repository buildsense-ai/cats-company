FROM node:22-bookworm-slim

# Keep the public Dockerfile usable outside China; the remote deploy script
# supplies USTC. HTTP also works before this slim image has CA certificates;
# Debian's Signed-By keyring and package signature checks remain enabled.
ARG DEBIAN_MIRROR=http://deb.debian.org/debian
ARG DEBIAN_SECURITY_MIRROR=http://deb.debian.org/debian-security
RUN sed -i \
      -e "s|http://deb.debian.org/debian-security|${DEBIAN_SECURITY_MIRROR}|g" \
      -e "s|http://deb.debian.org/debian\([ /]*\)$|${DEBIAN_MIRROR}\1|g" \
      /etc/apt/sources.list.d/debian.sources \
    && printf 'Acquire::Retries "3";\nAcquire::http::Timeout "30";\nAcquire::https::Timeout "30";\nAPT::Update::Error-Mode "any";\n' > /etc/apt/apt.conf.d/80-build-network \
    && apt-get update \
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
