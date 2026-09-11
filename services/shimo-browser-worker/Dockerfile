FROM node:22-bookworm-slim

RUN apt-get update \
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
