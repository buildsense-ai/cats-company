import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { ARTIFACT_FRAME_BRIDGE_CONTRACT } from '../artifact-context';
import ControlledArtifactPreview from './controlled-artifact-preview';

vi.mock('../auth-session', () => ({
  getToken: () => 'artifact-user',
  getAuthRevision: () => 1,
  isCurrentAuthSession: (token, revision) => token === 'artifact-user' && revision === 1,
  request: vi.fn(async (_method, _path, body) => {
    const url = new URL(body.url);
    url.searchParams.set('media_token', 'scoped');
    return { url: url.href, expires_in: 900 };
  }),
}));

let container;
let root;

const file = {
  artifact_agent_uid: 440,
  artifact_id: 'lesson-game',
  publish_version: 2,
  url: 'https://artifacts.example.test/artifacts/lesson-game/v2/',
};

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});

test('reports a bound cross-origin Artifact only after its iframe loads', async () => {
  const onBindingChange = vi.fn();
  const onReady = vi.fn();
  await act(async () => {
    root.render(
      <ControlledArtifactPreview
        file={file}
        descriptor={{ isRemoteArtifact: true, isSameOriginRemoteArtifact: false, url: file.url }}
        onBindingChange={onBindingChange}
        onReady={onReady}
      />,
    );
  });

  const frame = container.querySelector('iframe');
  expect(frame).toBeTruthy();
  expect(onReady).not.toHaveBeenCalled();

  await act(async () => Simulate.load(frame));

  expect(onReady).toHaveBeenCalledTimes(1);
  expect(onBindingChange).toHaveBeenLastCalledWith(expect.objectContaining({
    frame,
    artifactId: 'lesson-game',
    agentUid: 440,
    url: file.url,
  }));
  expect(frame.src).toContain('media_token=scoped');
  expect(frame.hasAttribute('credentialless')).toBe(false);
});

test('invalidates a cross-origin binding after a second document load', async () => {
  const onBindingChange = vi.fn();
  const onError = vi.fn();
  await act(async () => {
    root.render(
      <ControlledArtifactPreview
        file={file}
        descriptor={{ isRemoteArtifact: true, isSameOriginRemoteArtifact: false, url: file.url }}
        onBindingChange={onBindingChange}
        onError={onError}
      />,
    );
  });

  const frame = container.querySelector('iframe');
  await act(async () => Simulate.load(frame));
  const firstBinding = onBindingChange.mock.calls.at(-1)?.[0];
  expect(firstBinding?.signal?.aborted).toBe(false);

  await act(async () => Simulate.load(frame));
  expect(firstBinding.signal.aborted).toBe(true);
  expect(onError).toHaveBeenCalledWith('artifact_frame_navigated');
  expect(onBindingChange).toHaveBeenLastCalledWith(null);
});

test('uses the opaque bridge once and rejects a later same-origin document load', async () => {
  const sameOriginFile = {
    ...file,
    url: `${window.location.origin}/artifacts/lesson-game/v2/`,
  };
  const onBindingChange = vi.fn();
  const onError = vi.fn();
  await act(async () => {
    root.render(
      <ControlledArtifactPreview
        file={sameOriginFile}
        descriptor={{
          isRemoteArtifact: true,
          isSameOriginRemoteArtifact: true,
          url: sameOriginFile.url,
        }}
        onBindingChange={onBindingChange}
        onError={onError}
      />,
    );
  });

  const frame = container.querySelector('iframe');
  expect(frame.src).toContain('catsco_bridge_nonce=');
  const transport = new URL(frame.src);
  expect(transport.searchParams.get('media_token')).toBe('scoped');
  expect(sameOriginFile.url).not.toContain('media_token');
  await act(async () => Simulate.load(frame));
  expect(onBindingChange).toHaveBeenLastCalledWith(expect.objectContaining({
    bridge: ARTIFACT_FRAME_BRIDGE_CONTRACT,
    bridgeReady: true,
    url: sameOriginFile.url,
    bridgeNonce: new URLSearchParams(transport.hash.slice(1)).get('catsco_bridge_nonce'),
  }));

  await act(async () => Simulate.load(frame));
  expect(onError).toHaveBeenCalledWith('artifact_frame_navigated');
  expect(onBindingChange).toHaveBeenLastCalledWith(null);
});

test('preserves gateway launch URLs without a static media exchange', async () => {
  const { request } = await import('../auth-session');
  request.mockClear();
  const url = 'https://artifact.catsco.cc/_launch/one-time-code?next=/lesson/';
  await act(async () => root.render(
    <ControlledArtifactPreview file={{ ...file, url }}
      descriptor={{ isRemoteArtifact: true, url }} />,
  ));
  expect(container.querySelector('iframe').src).toBe(url);
  expect(request).not.toHaveBeenCalled();
});
