export const REQUEST_FAILURE_KIND = Object.freeze({
  OFFLINE: 'offline',
  UNREACHABLE: 'unreachable',
  TIMEOUT: 'timeout',
  SERVICE_UNAVAILABLE: 'service_unavailable',
  SERVER_ERROR: 'server_error',
  REQUEST_ERROR: 'request_error',
});

export const REQUEST_ERROR_CODE = Object.freeze({
  ABORTED: 'REQUEST_ABORTED',
  NETWORK: 'NETWORK_ERROR',
  TIMEOUT: 'REQUEST_TIMEOUT',
});

const RESOURCE_LOAD_FAILURE_COPY = Object.freeze({
  [REQUEST_FAILURE_KIND.OFFLINE]: {
    previous: () => '当前无网络连接。',
    initial: () => '当前无网络连接，连接网络后再试。',
  },
  [REQUEST_FAILURE_KIND.TIMEOUT]: {
    previous: (resource) => `更新${resource}超时。`,
    initial: (resource) => `获取${resource}超时，请重试。`,
  },
  [REQUEST_FAILURE_KIND.SERVICE_UNAVAILABLE]: {
    previous: () => '服务暂时不可用。',
    initial: (resource) => `服务暂时不可用，暂时无法获取${resource}。`,
  },
  [REQUEST_FAILURE_KIND.UNREACHABLE]: {
    previous: () => '暂时无法连接服务。',
    initial: (resource) => `暂时无法连接服务，无法获取${resource}。`,
  },
});

export function requestFailureKind(error, online = globalThis.navigator?.onLine) {
  if (error?.code === REQUEST_ERROR_CODE.TIMEOUT) return REQUEST_FAILURE_KIND.TIMEOUT;
  if (error?.code === REQUEST_ERROR_CODE.NETWORK) {
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

function resourceLoadFailureCopy(kind, resource, hasPreviousResult) {
  const phase = hasPreviousResult ? 'previous' : 'initial';
  const fallback = hasPreviousResult
    ? (name) => `暂时无法更新${name}。`
    : (name) => `暂时无法获取${name}，请重试。`;
  return (RESOURCE_LOAD_FAILURE_COPY[kind]?.[phase] || fallback)(resource);
}

export function describeResourceLoadError(error, resource, options = {}) {
  const { hasPreviousResult = false, loadedAt = 0 } = options;
  const kind = requestFailureKind(error);
  const time = formatLoadedAt(loadedAt);
  const previousData = time
    ? `当前显示 ${time} 加载的${resource}。`
    : `当前显示上次加载的${resource}。`;
  const failure = resourceLoadFailureCopy(kind, resource, hasPreviousResult);

  return hasPreviousResult ? `${failure}${previousData}` : failure;
}
