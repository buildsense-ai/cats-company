#!/usr/bin/env node
import process from 'node:process';
import { ShimoLoginManager } from './login_manager.mjs';
import { PlaywrightShimoEngine } from './reader.mjs';
import { createShimoWorkerServer } from './service.mjs';
import { EncryptedSessionStore, sessionKeyFromEnv } from './session_store.mjs';

const host = process.env.SHIMO_WORKER_HOST || '0.0.0.0';
const port = Number(process.env.SHIMO_WORKER_PORT || 7070);
const internalToken = process.env.SHIMO_WORKER_TOKEN || '';
const publicBaseURL = process.env.SHIMO_WORKER_PUBLIC_BASE_URL || '';
const stateDirectory = process.env.SHIMO_WORKER_STATE_DIR || '/var/lib/catsco-shimo';
const maxConcurrency = Number(process.env.SHIMO_WORKER_MAX_CONCURRENCY || 2);
const maxQueue = Number(process.env.SHIMO_WORKER_MAX_QUEUE || 8);

const store = new EncryptedSessionStore({ directory: stateDirectory, key: sessionKeyFromEnv(process.env.SHIMO_WORKER_SESSION_KEY) });

// A session directory that exists but is owned by another uid accepts no
// writes, and every finished login then fails with nothing in the log.  Say it
// once, loudly, at startup instead.
try {
  store.verifyWritable();
} catch (error) {
  process.stderr.write(`shimo-browser-worker cannot write sessions to ${stateDirectory}: ${error?.message || error}; finished logins will not be saved until the directory is writable by uid ${process.getuid?.() ?? 'unknown'}\n`);
}
const reader = new PlaywrightShimoEngine();
const loginManager = new ShimoLoginManager({ store, publicBaseURL, callbackAuthToken: internalToken });
const server = createShimoWorkerServer({ internalToken, store, reader, loginManager, limits: { maxConcurrency, maxQueue } });

server.listen(port, host, () => process.stdout.write(`shimo-browser-worker listening on ${host}:${port} concurrency=${maxConcurrency} queue=${maxQueue}\n`));
for (const signal of ['SIGTERM', 'SIGINT']) process.once(signal, async () => {
  await loginManager.shutdown();
  server.close(() => process.exit(0));
});
