import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { ARTIFACT_FRAME_BRIDGE_CONTRACT } from '../artifact-context';
import ControlledArtifactPreview from './controlled-artifact-preview';

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
  await act(async () => Simulate.load(frame));
  expect(onBindingChange).toHaveBeenLastCalledWith(expect.objectContaining({
    bridge: ARTIFACT_FRAME_BRIDGE_CONTRACT,
    bridgeReady: true,
  }));

  await act(async () => Simulate.load(frame));
  expect(onError).toHaveBeenCalledWith('artifact_frame_navigated');
  expect(onBindingChange).toHaveBeenLastCalledWith(null);
});
