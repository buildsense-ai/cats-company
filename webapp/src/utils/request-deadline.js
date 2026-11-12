// Bound the whole JSON operation, not just receipt of HTTP headers. A caller
// must settle even when a transport/mock fails to honor AbortSignal in its body.
export async function withRequestDeadline(operation, { signal, timeoutMs }) {
  const controller = new AbortController();
  let timer;
  let cancel;
  const stopped = new Promise((_, reject) => {
    cancel = (timedOut = false) => {
      const error = new Error(timedOut ? '请求超时，请稍后重试' : '请求已取消');
      error.code = timedOut ? 'REQUEST_TIMEOUT' : 'REQUEST_ABORTED';
      reject(error);
      controller.abort();
    };
  });
  const abortFromCaller = () => cancel(false);
  if (signal?.aborted) abortFromCaller();
  else signal?.addEventListener('abort', abortFromCaller, { once: true });
  timer = setTimeout(() => cancel(true), timeoutMs);
  try {
    const result = controller.signal.aborted ? stopped : operation(controller.signal);
    return await Promise.race([result, stopped]);
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', abortFromCaller);
  }
}
