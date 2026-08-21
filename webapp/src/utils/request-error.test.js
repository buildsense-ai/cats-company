import {
  describeResourceLoadError,
  REQUEST_FAILURE_KIND,
  requestFailureKind,
} from './request-error';

describe('request failure semantics', () => {
  test('distinguishes gateway availability from generic server failures', () => {
    expect(requestFailureKind({ status: 502 })).toBe(REQUEST_FAILURE_KIND.SERVICE_UNAVAILABLE);
    expect(requestFailureKind({ status: 503 })).toBe(REQUEST_FAILURE_KIND.SERVICE_UNAVAILABLE);
    expect(requestFailureKind({ status: 500 })).toBe(REQUEST_FAILURE_KIND.SERVER_ERROR);
  });

  test('uses observable browser connectivity for network failures', () => {
    const error = { code: 'NETWORK_ERROR' };
    expect(requestFailureKind(error, false)).toBe(REQUEST_FAILURE_KIND.OFFLINE);
    expect(requestFailureKind(error, true)).toBe(REQUEST_FAILURE_KIND.UNREACHABLE);
  });

  test('describes unavailable fresh data without blaming the backend', () => {
    expect(describeResourceLoadError({ status: 502 }, '聊天记录')).toBe(
      '服务暂时不可用，暂时无法获取聊天记录。',
    );
  });

  test('states when previously loaded data remains visible', () => {
    const message = describeResourceLoadError(
      { status: 503 },
      '聊天记录',
      { hasPreviousData: true, loadedAt: new Date(2026, 7, 21, 14, 32).getTime() },
    );

    expect(message).toMatch(/^服务暂时不可用。当前显示 14:32 加载的聊天记录。$/);
  });
});
