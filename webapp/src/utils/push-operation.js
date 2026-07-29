let operationTail = Promise.resolve();

export function enqueuePushOperation(operation) {
  const result = operationTail.then(operation);
  operationTail = result.catch(() => {});
  return result;
}
