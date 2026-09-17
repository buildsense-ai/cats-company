import { describe, expect, test } from 'vitest';

import { CLOUD_WORKER_PENDING_WINDOW_MS, isCloudWorkerPending } from './cloud-worker-pending';

describe('isCloudWorkerPending', () => {
  const now = Date.parse('2026-09-17T06:00:00Z');

  test('true for a fresh worker that has not connected yet', () => {
    expect(isCloudWorkerPending({ runtime_status: 'not_connected', created_time: '2026-09-17T05:58:00Z' }, now)).toBe(true);
  });

  test('false once connected', () => {
    expect(isCloudWorkerPending({ runtime_status: 'connected', created_time: '2026-09-17T05:58:00Z' }, now)).toBe(false);
  });

  test('false for an established worker that dropped offline', () => {
    const old = new Date(now - CLOUD_WORKER_PENDING_WINDOW_MS - 1000).toISOString();
    expect(isCloudWorkerPending({ runtime_status: 'not_connected', created_time: old }, now)).toBe(false);
  });

  test('false without a trustworthy creation time', () => {
    expect(isCloudWorkerPending({ runtime_status: 'not_connected' }, now)).toBe(false);
    expect(isCloudWorkerPending({ runtime_status: 'not_connected', created_time: 'not-a-date' }, now)).toBe(false);
    expect(isCloudWorkerPending({ runtime_status: 'not_connected', created_time: '2026-09-17T07:00:00Z' }, now)).toBe(false);
  });

  test('false for missing workers and confirmed states', () => {
    expect(isCloudWorkerPending(null, now)).toBe(false);
    expect(isCloudWorkerPending({}, now)).toBe(false);
    expect(isCloudWorkerPending({ runtime_status: 'unknown' }, now)).toBe(false);
  });
});
