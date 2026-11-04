import React, { act, StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, expect, test, vi } from 'vitest';
import { GatewayAnnotationPopover } from './cloud-artifacts-panel';

let root;
let container;
let frame;
afterEach(async () => {
  await act(async () => root?.unmount());
  container?.remove(); frame?.remove();
});

async function mountPopover(screenshotResult = Promise.resolve({ screenshots: [] })) {
  container = document.createElement('div');
  frame = document.createElement('iframe');
  document.body.append(container, frame);
  root = createRoot(container);
  const onScreenshot = vi.fn().mockReturnValue(screenshotResult);
  const onSubmit = vi.fn();
  const onCancel = vi.fn();
  const capture = { frame, target: { rect: { x: .1, y: .1, width: .2, height: .2 } } };
  await act(async () => root.render(<StrictMode><GatewayAnnotationPopover capture={capture}
    onScreenshot={onScreenshot} onSubmit={onSubmit} onCancel={onCancel} /></StrictMode>));
  return { onScreenshot, onSubmit, onCancel, capture };
}

test('StrictMode starts one renderer job instead of racing a canceled mount', async () => {
  const { onScreenshot } = await mountPopover();
  expect(onScreenshot).toHaveBeenCalledTimes(1);
  expect(onScreenshot.mock.calls[0][1].aborted).toBe(false);
});

test('comment limits count Unicode characters consistently with the server', async () => {
  const { onSubmit } = await mountPopover();
  const input = document.querySelector('.gateway-annotation-popover textarea');
  const comment = '🐈'.repeat(2000);
  await act(async () => {
    input.value = comment;
    Simulate.change(input);
  });
  const button = document.querySelector('.gateway-annotation-popover-actions button:last-child');
  expect(button.disabled).toBe(false);
  await act(async () => button.click());
  expect(onSubmit.mock.calls[0][1]).toBe(comment);
});

test('explicit text-only choice cancels capture and ignores late screenshot pixels', async () => {
  let complete;
  const { onSubmit, onScreenshot } = await mountPopover(new Promise(resolve => { complete = resolve; }));
  const input = document.querySelector('.gateway-annotation-popover textarea');
  await act(async () => { input.value = '先发送文字'; Simulate.change(input); });
  await act(async () => document.querySelector('.gateway-annotation-screenshots input').click());
  expect(onScreenshot.mock.calls[0][1].aborted).toBe(true);
  await act(async () => complete({ screenshots: [{ role: 'full', data_url: 'data:image/jpeg;base64,late' }] }));
  expect(document.querySelector('.gateway-annotation-screenshots img')).toBeNull();
  await act(async () => document.querySelector('.gateway-annotation-popover-actions button:last-child').click());
  expect(onSubmit.mock.calls[0].slice(1)).toEqual(['先发送文字', null]);
});

test('composition Enter does not send even with a ready screenshot', async () => {
  const { onSubmit, onCancel } = await mountPopover();
  const input = document.querySelector('.gateway-annotation-popover textarea');
  await act(async () => { input.value = '修改标题'; Simulate.change(input); });
  expect(document.querySelector('.gateway-annotation-popover-actions button:last-child').disabled).toBe(false);
  await act(async () => Simulate.keyDown(input, { key: 'Enter', metaKey: true, nativeEvent: { isComposing: true } }));
  await act(async () => Simulate.keyDown(input, { key: 'Escape', nativeEvent: { isComposing: true } }));
  expect(onSubmit).not.toHaveBeenCalled(); expect(onCancel).not.toHaveBeenCalled();
});
