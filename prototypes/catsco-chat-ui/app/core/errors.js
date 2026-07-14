window.AppErrors = (() => {
  const statusMessages = {
    400: '请求内容不正确',
    401: '登录状态已失效，请重新登录',
    403: '当前账号没有执行此操作的权限',
    404: '请求的内容不存在',
    409: '当前数据已发生变化，请刷新后重试',
    413: '提交的内容或文件过大',
    422: '提交的数据无法处理',
    429: '操作过于频繁，请稍后重试',
    500: '服务暂时不可用，请稍后重试',
    502: '上游服务暂时不可用',
    503: '服务正在维护或尚未启动',
    504: '服务响应超时，请重试',
  };

  function classify(error) {
    const status = Number(error?.status || error?.response?.status || 0);
    const code = String(error?.code || '').toUpperCase();
    const message = String(error?.message || '');
    if (error?.name === 'AbortError' || code === 'ABORT_ERR' || /中止|停止|aborted/i.test(message)) return 'cancelled';
    if (code === 'NETWORK_ERROR' || error instanceof TypeError || /failed to fetch|network|无法连接/i.test(message)) return 'network';
    if (status === 401) return 'auth';
    if (status === 403) return 'permission';
    if (status === 404) return 'not-found';
    if (status === 408 || status === 504 || /timeout|超时/i.test(message)) return 'timeout';
    if (status === 409) return 'conflict';
    if (status === 413) return 'too-large';
    if (status === 400 || status === 422) return 'validation';
    if (status === 429) return 'rate-limit';
    if (status >= 500) return 'server';
    return 'unknown';
  }

  class AppError extends Error {
    constructor(message, details = {}) {
      super(message || '操作失败');
      this.name = 'AppError';
      this.status = details.status || 0;
      this.code = details.code || '';
      this.data = details.data ?? null;
      this.context = details.context || '';
      this.cause = details.cause;
      this.kind = details.kind || classify(this);
      this.retryable = details.retryable ?? (!this.status || this.status === 429 || this.status >= 500);
    }
  }

  function extractMessage(data, fallback) {
    if (typeof data === 'string' && data.trim()) return data.trim();
    if (data && typeof data === 'object') {
      const candidate = data.error?.message || data.error || data.detail || data.message;
      if (typeof candidate === 'string' && candidate.trim()) return candidate.trim();
    }
    return fallback || '操作失败';
  }

  function normalize(error, options = {}) {
    if (error instanceof AppError) {
      if (options.context && !error.context) error.context = options.context;
      return error;
    }
    const status = Number(error?.status || error?.response?.status || 0);
    const kind = classify(error);
    const fallback = kind === 'network'
      ? '无法连接服务，请检查后端是否运行'
      : (statusMessages[status] || options.fallback || '操作失败');
    return new AppError(extractMessage(error?.data, error?.message || fallback), {
      status,
      code: error?.code || (kind === 'network' ? 'NETWORK_ERROR' : ''),
      kind,
      data: error?.data,
      context: options.context,
      cause: error,
      retryable: kind === 'network' || kind === 'timeout' || status === 429 || status >= 500,
    });
  }

  async function fromResponse(response) {
    let data = null;
    const contentType = response.headers?.get?.('content-type') || '';
    try { data = contentType.includes('json') ? await response.json() : await response.text(); }
    catch (_) { data = null; }
    if (!response.ok) {
      throw new AppError(extractMessage(data, statusMessages[response.status] || '请求失败'), {
        status: response.status,
        data,
        retryable: response.status === 429 || response.status >= 500,
      });
    }
    return data;
  }

  function userMessage(error, fallback = '') {
    const normalized = normalize(error, { fallback });
    const messages = {
      network: '网络连接失败，请检查网络或后端服务是否运行',
      auth: '登录状态已失效，请重新登录',
      permission: '当前账号没有执行此操作的权限',
      'not-found': '请求的内容不存在或已被删除',
      timeout: '任务响应超时，可以稍后重新生成',
      conflict: '数据状态已经变化，请刷新后重试',
      'too-large': '提交的内容或文件过大',
      validation: '提交内容不完整或格式不正确',
      'rate-limit': '请求过于频繁，请稍后再试',
      server: '服务暂时不可用，请稍后重试',
      cancelled: '任务已中止',
    };
    return messages[normalized.kind] || statusMessages[normalized.status] || normalized.message || fallback || '操作失败';
  }

  function presentation(error, fallback = '') {
    const normalized = normalize(error, { fallback });
    const titles = {
      network: '连接失败', auth: '登录失效', permission: '没有权限',
      'not-found': '内容不存在', timeout: '响应超时', conflict: '状态冲突',
      'too-large': '内容过大', validation: '内容有误', 'rate-limit': '请求频繁',
      server: '服务异常', cancelled: '任务已中止', unknown: '任务失败',
    };
    return { kind: normalized.kind, title: titles[normalized.kind] || titles.unknown, message: userMessage(normalized, fallback), retryable: normalized.retryable };
  }

  function report(error, options = {}) {
    const normalized = normalize(error, options);
    if (!options.silent) {
      const notify = options.notify || (typeof showToast === 'function' ? showToast : null);
      notify?.(userMessage(normalized, options.fallback));
    }
    if (options.log !== false) console.error('[CatsCo]', normalized.context || 'operation', normalized);
    return normalized;
  }

  async function run(action, options = {}) {
    try { return await action(); }
    catch (error) {
      const normalized = report(error, options);
      if (options.rethrow) throw normalized;
      return options.fallbackValue;
    }
  }

  return { AppError, classify, normalize, fromResponse, userMessage, presentation, report, run };
})();
