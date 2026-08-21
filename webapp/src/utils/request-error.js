export const REQUEST_FAILURE_KIND = Object.freeze({
  OFFLINE: 'offline',
  UNREACHABLE: 'unreachable',
  TIMEOUT: 'timeout',
  SERVICE_UNAVAILABLE: 'service_unavailable',
  SERVER_ERROR: 'server_error',
  REQUEST_ERROR: 'request_error',
});

export function requestFailureKind(error, online = globalThis.navigator?.onLine) {
  if (error?.code === 'REQUEST_TIMEOUT') return REQUEST_FAILURE_KIND.TIMEOUT;
  if (error?.code === 'NETWORK_ERROR') {
    return online === false
      ? REQUEST_FAILURE_KIND.OFFLINE
      : REQUEST_FAILURE_KIND.UNREACHABLE;
  }

  const status = Number(error?.status || 0);
  if ([502, 503, 504].includes(status)) return REQUEST_FAILURE_KIND.SERVICE_UNAVAILABLE;
  if (status >= 500) return REQUEST_FAILURE_KIND.SERVER_ERROR;
  return REQUEST_FAILURE_KIND.REQUEST_ERROR;
}

function formatLoadedAt(value) {
  const timestamp = Number(value);
  if (!Number.isFinite(timestamp) || timestamp <= 0) return '';
  return new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(new Date(timestamp));
}

export function describeResourceLoadError(error, resource, options = {}) {
  const { hasPreviousData = false, loadedAt = 0 } = options;
  const kind = requestFailureKind(error);
  const time = formatLoadedAt(loadedAt);
  const previousData = time
    ? `当前显示 ${time} 加载的${resource}。`
    : `当前显示上次加载的${resource}。`;

  if (hasPreviousData) {
    if (kind === REQUEST_FAILURE_KIND.OFFLINE) return `当前无网络连接。${previousData}`;
    if (kind === REQUEST_FAILURE_KIND.TIMEOUT) return `更新${resource}超时。${previousData}`;
    if (kind === REQUEST_FAILURE_KIND.SERVICE_UNAVAILABLE) return `服务暂时不可用。${previousData}`;
    if (kind === REQUEST_FAILURE_KIND.UNREACHABLE) return `暂时无法连接服务。${previousData}`;
    return `暂时无法更新${resource}。${previousData}`;
  }

  if (kind === REQUEST_FAILURE_KIND.OFFLINE) return '当前无网络连接，连接网络后再试。';
  if (kind === REQUEST_FAILURE_KIND.TIMEOUT) return `获取${resource}超时，请重试。`;
  if (kind === REQUEST_FAILURE_KIND.SERVICE_UNAVAILABLE) return `服务暂时不可用，暂时无法获取${resource}。`;
  if (kind === REQUEST_FAILURE_KIND.UNREACHABLE) return `暂时无法连接服务，无法获取${resource}。`;
  return `暂时无法获取${resource}，请重试。`;
}
