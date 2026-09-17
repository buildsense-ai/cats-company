// A brand-new cloud worker is provisioning right after a purchase or a create
// click; its runtime only reports connected once the instance is up and the
// worker process registers. Only fresh, not-yet-connected workers count as
// "pending": an established worker that dropped offline, and any self-hosted
// assistant, must not surface the provisioning notice again.
export const CLOUD_WORKER_PENDING_WINDOW_MS = 30 * 60 * 1000;

export const isCloudWorkerPending = (worker, now = Date.now()) => {
  if (!worker) return false;
  if (!worker.runtime_status || worker.runtime_status === 'connected') return false;
  const createdAt = Date.parse(worker.created_time || worker.created_at || '');
  if (!Number.isFinite(createdAt)) return false;
  const age = now - createdAt;
  return age >= 0 && age < CLOUD_WORKER_PENDING_WINDOW_MS;
};