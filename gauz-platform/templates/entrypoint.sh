#!/bin/sh
set -e

echo "[entrypoint] Starting XiaoBa tenant container..."

if [ ! -d "/app/.git" ] && [ -n "${GIT_REPO_URL}" ]; then
  echo "[entrypoint] No .git found, cloning ${GIT_REPO_URL} (branch: ${GIT_BRANCH:-main})..."
  git clone --branch "${GIT_BRANCH:-main}" --single-branch "${GIT_REPO_URL}" /app/src
  mv /app/src/.git /app/.git
  mv /app/src/* /app/ 2>/dev/null || true
  mv /app/src/.* /app/ 2>/dev/null || true
  rm -rf /app/src
  echo "[entrypoint] Clone complete."
fi

if [ "${AUTO_PULL}" = "true" ] && [ -d "/app/.git" ]; then
  echo "[entrypoint] AUTO_PULL enabled, pulling latest..."
  cd /app && git pull --ff-only || echo "[entrypoint] git pull failed, continuing with current code"
fi

if [ -f "/app/package-lock.json" ]; then
  if [ ! -d "/app/node_modules" ] || [ "${REBUILD}" = "true" ]; then
    echo "[entrypoint] Installing dependencies..."
    cd /app && npm ci
  fi
  if [ -f "/app/package.json" ]; then
    echo "[entrypoint] Building..."
    cd /app && npm run build
  fi
fi

echo "[entrypoint] Starting node process..."
exec node dist/index.js catscompany
