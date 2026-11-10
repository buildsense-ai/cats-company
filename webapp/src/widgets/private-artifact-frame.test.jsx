import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import PrivateArtifactFrame, { isPrivateArtifactFrameURL } from './private-artifact-frame';

const auth = vi.hoisted(() => ({ token: 'alice', revision: 1, request: vi.fn() }));
vi.mock('../auth-session', () => ({
  getToken: () => auth.token,
  getAuthRevision: () => auth.revision,
  isCurrentAuthSession: (token, revision) => Boolean(token) && token === auth.token && revision === auth.revision,
  request: auth.request,
}));

const src = 'https://artifacts.example.test/by-agent/440/lesson/v2/?view=full#route';
const signed = (url = src, token = 'scoped') => {
  const parsed = new URL(url);
  parsed.searchParams.set('media_token', token);
  return parsed.href;
};
const deferred = () => {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
let container;
let root;
let pending;
beforeEach(() => {
  auth.token = 'alice';
  auth.revision = 1;
  pending = deferred();
  auth.request.mockReset().mockReturnValue(pending.promise);
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});
const render = async (props = {}) => {
  await act(async () => root.render(<PrivateArtifactFrame src={src} title="Private artifact" {...props} />));
};
const changeAuth = (token) => {
  auth.token = token;
  auth.revision += 1;
  window.dispatchEvent(new CustomEvent('cc:auth-changed'));
};

test('waits for the current session before mounting and forwards iframe props, refs and load events', async () => {
  const frameRef = React.createRef();
  const onLoad = vi.fn();
  await render({ ref: frameRef, onLoad, messageId: 42, className: 'preview', sandbox: 'allow-scripts', credentialless: '' });
  expect(container.querySelector('iframe')).toBeNull();
  expect(onLoad).not.toHaveBeenCalled();
  expect(auth.request).toHaveBeenCalledWith('POST', '/api/files/artifact-session', { url: src, message_id: 42 }, {
    authToken: 'alice', signal: expect.any(AbortSignal), timeoutMs: 15_000,
  });
  await act(async () => pending.resolve({ url: signed(), expires_in: 900 }));
  const frame = container.querySelector('iframe');
  expect(frameRef.current).toBe(frame);
  expect(frame.src).toBe(signed());
  expect(frame.className).toBe('preview');
  expect(frame.getAttribute('sandbox')).toBe('allow-scripts');
  expect(frame.hasAttribute('credentialless')).toBe(false);
  expect(frame.hasAttribute('messageId')).toBe(false);
  await act(async () => Simulate.load(frame));
  expect(onLoad).toHaveBeenCalledTimes(1);
  expect(auth.request).toHaveBeenCalledTimes(1);
});

test('does not mint again when parent callbacks, refs or state rerender', async () => {
  await render({ ref: vi.fn() });
  await act(async () => pending.resolve({ url: signed() }));
  const frame = container.querySelector('iframe');
  await render({ ref: vi.fn(), onLoad: vi.fn(), style: { width: '100%' } });
  expect(auth.request).toHaveBeenCalledTimes(1);
  expect(container.querySelector('iframe')).toBe(frame);
});

test.each([401, 403, 404])('reports refusal %s through onError without a guest or external frame', async (status) => {
  const onError = vi.fn();
  await render({ onError });
  const error = Object.assign(new Error('refused'), { status });
  await act(async () => pending.reject(error));
  expect(onError).toHaveBeenCalledWith(error);
  expect(container.querySelector('iframe')).toBeNull();
  expect(container.querySelector('a')).toBeNull();
  expect(auth.request).toHaveBeenCalledTimes(1);
});

test.each(['https://external.example/guest?media_token=x', src, 'javascript:alert(1)'])('refuses an invalid transport URL %s', async (url) => {
  const onError = vi.fn();
  await render({ onError });
  await act(async () => pending.resolve({ url }));
  expect(onError).toHaveBeenCalledWith(expect.objectContaining({ message: 'artifact_session_invalid_url' }));
  expect(container.querySelector('iframe')).toBeNull();
});

test('aborts an old request and ignores its late response after an account switch', async () => {
  await render();
  const oldSignal = auth.request.mock.calls[0][3].signal;
  const next = deferred();
  auth.request.mockReturnValueOnce(next.promise);
  await act(async () => changeAuth('bob'));
  expect(oldSignal.aborted).toBe(true);
  expect(auth.request.mock.calls[1][3].authToken).toBe('bob');
  await act(async () => pending.resolve({ url: signed(src, 'alice') }));
  expect(container.querySelector('iframe')).toBeNull();
  await act(async () => next.resolve({ url: signed(src, 'bob') }));
  expect(container.querySelector('iframe').src).toBe(signed(src, 'bob'));
});

test('removes a loaded frame synchronously on logout and never restores it from a late response', async () => {
  const frameRef = React.createRef();
  const onError = vi.fn();
  await render({ ref: frameRef, onError });
  await act(async () => pending.resolve({ url: signed() }));
  const oldFrame = frameRef.current;
  await act(async () => {
    changeAuth(null);
    expect(container.querySelector('iframe')).toBeNull();
    expect(frameRef.current).toBeNull();
    expect(oldFrame.isConnected).toBe(false);
  });
  expect(auth.request).toHaveBeenCalledTimes(1);
  expect(onError).toHaveBeenCalled();
});

test('ignores a response whose auth revision changed even without a notification', async () => {
  await render();
  auth.revision += 1;
  await act(async () => pending.resolve({ url: signed() }));
  expect(container.querySelector('iframe')).toBeNull();
});

test('aborts on URL changes and unmount, keeping only the new canonical source', async () => {
  await render();
  const oldSignal = auth.request.mock.calls[0][3].signal;
  const nextURL = src.replace('/v2/', '/v3/');
  const next = deferred();
  auth.request.mockReturnValueOnce(next.promise);
  await render({ src: nextURL });
  expect(oldSignal.aborted).toBe(true);
  await act(async () => pending.resolve({ url: signed() }));
  expect(container.querySelector('iframe')).toBeNull();
  await act(async () => next.resolve({ url: signed(nextURL) }));
  expect(container.querySelector('iframe').src).toBe(signed(nextURL));
  const signal = auth.request.mock.calls[1][3].signal;
  await act(async () => root.render(null));
  expect(signal.aborted).toBe(true);
});

test('requires an authenticated session and distinguishes static artifacts from gateway launches', async () => {
  auth.token = null;
  const onError = vi.fn();
  await render({ onError });
  expect(auth.request).not.toHaveBeenCalled();
  expect(onError).toHaveBeenCalledWith(expect.objectContaining({ message: 'artifact_session_required' }));
  expect(isPrivateArtifactFrameURL(src)).toBe(true);
  expect(isPrivateArtifactFrameURL('/artifacts/by-agent/440/lesson/v2/')).toBe(true);
  expect(isPrivateArtifactFrameURL('https://artifact.catsco.cc/_launch/code?next=/lesson/')).toBe(false);
  expect(isPrivateArtifactFrameURL('https://artifact.catsco.cc/lesson/')).toBe(false);
});

test('removes a loaded frame during an account switch and keeps it absent if the new actor is refused', async () => {
  const frameRef = React.createRef();
  const onError = vi.fn();
  await render({ ref: frameRef, onError });
  await act(async () => pending.resolve({ url: signed() }));
  const oldFrame = frameRef.current;
  const next = deferred();
  auth.request.mockReturnValueOnce(next.promise);
  await act(async () => {
    changeAuth('bob');
    expect(oldFrame.isConnected).toBe(false);
    expect(frameRef.current).toBeNull();
    expect(container.querySelector('iframe')).toBeNull();
  });
  await act(async () => next.reject(Object.assign(new Error('refused'), { status: 403 })));
  expect(container.querySelector('iframe')).toBeNull();
  expect(container.querySelector('a')).toBeNull();
  expect(onError).toHaveBeenLastCalledWith(expect.objectContaining({ status: 403 }));
});

test('keeps the canonical version and bridge nonce intact and adds no absent message hint', async () => {
  const canonical = `${src}&catsco_bridge_nonce=nonce_123`;
  await render({ src: canonical });
  expect(auth.request.mock.calls[0][2]).toEqual({ url: canonical });
  await act(async () => pending.resolve({ url: signed(canonical), expires_in: 900 }));
  const transport = new URL(container.querySelector('iframe').src);
  expect(transport.pathname).toContain('/v2/');
  expect(transport.hash).toBe(new URL(canonical).hash);
  transport.searchParams.delete('media_token');
  expect(transport.href).toBe(canonical);
  expect(auth.request.mock.calls[0][2].url).toBe(canonical);
});
