import { describe, expect, test, vi } from 'vitest';
import { createDraftTaskCoordinator } from './draft-task-coordinator';

describe('task draft transition ownership', () => {
  test('annotation and normal send cannot transition the same draft at once', () => {
    const coordinator = createDraftTaskCoordinator(() => 'draft-1');
    const annotation = coordinator.acquire();
    expect(annotation.isCurrent()).toBe(true);
    expect(coordinator.acquire()).toBeNull();
    annotation.release();
    const send = coordinator.acquire();
    annotation.release();
    expect(coordinator.acquire()).toBeNull();
    send.release();
    expect(coordinator.acquire()).not.toBeNull();
  });

  test('retry reuses the real topic instead of creating an empty task twice', async () => {
    const coordinator = createDraftTaskCoordinator(() => 'draft-1');
    const create = vi.fn().mockResolvedValue({ topicId: 'grp_20' });
    const [a, b] = await Promise.all([coordinator.ensure(create), coordinator.ensure(create)]);
    expect(a).toBe(b);
    expect(await coordinator.ensure(create)).toBe(a);
    expect(create).toHaveBeenCalledTimes(1);
  });

  test('creation failures allow retry and explicit rollback forgets a deleted topic', async () => {
    const coordinator = createDraftTaskCoordinator(() => 'draft-1');
    const create = vi.fn().mockRejectedValueOnce(new Error('offline')).mockResolvedValue({ topicId: 'grp_20' });
    await expect(coordinator.ensure(create)).rejects.toThrow('offline');
    await coordinator.ensure(create);
    coordinator.forget();
    await coordinator.ensure(create);
    expect(create).toHaveBeenCalledTimes(3);
  });

  test('task/Agent/project/auth changes invalidate old callbacks and creation reuse', async () => {
    let scope = 'user:1|auth:1|task:1|agent:7|project:1';
    const coordinator = createDraftTaskCoordinator(() => scope);
    const lease = coordinator.acquire();
    const create = vi.fn().mockResolvedValue({ topicId: 'grp_20' });
    await coordinator.ensure(create);
    scope = 'user:1|auth:2|task:2|agent:8|project:2';
    expect(lease.isCurrent()).toBe(false);
    await coordinator.ensure(create);
    expect(create).toHaveBeenCalledTimes(2);
    lease.release();
    expect(coordinator.acquire().isCurrent()).toBe(true);
  });
});
