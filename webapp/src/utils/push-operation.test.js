import { enqueuePushOperation } from './push-operation';

describe('push operation queue', () => {
  test('finishes session cleanup before reconciling the next session', async () => {
    let finishOldReconcile;
    const order = [];

    const oldReconcile = enqueuePushOperation(async () => {
      order.push('old-start');
      await new Promise((resolve) => {
        finishOldReconcile = resolve;
      });
      order.push('old-finish');
    });
    const cleanup = enqueuePushOperation(() => {
      order.push('cleanup');
    });
    const newReconcile = enqueuePushOperation(() => {
      order.push('new-reconcile');
    });

    await vi.waitFor(() => expect(finishOldReconcile).toBeTypeOf('function'));
    expect(order).toEqual(['old-start']);
    finishOldReconcile();
    await Promise.all([oldReconcile, cleanup, newReconcile]);

    expect(order).toEqual([
      'old-start',
      'old-finish',
      'cleanup',
      'new-reconcile',
    ]);
  });
});
