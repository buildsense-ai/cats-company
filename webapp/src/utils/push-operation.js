let operationTail = Promise.resolve();

export function enqueuePushOperation(operation) {
  const run = () => {
    if (navigator?.locks?.request) {
      return navigator.locks.request('catsco-push-subscription', operation);
    }
    return operation();
  };
  const result = operationTail.then(run);
  operationTail = result.catch(() => {});
  return result;
}
