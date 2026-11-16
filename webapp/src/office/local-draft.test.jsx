import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import LocalDraftTextarea from './ui/LocalDraftTextarea.jsx';

let frames;
let nextFrameID;

function flushFrame() {
  const pending = frames;
  frames = [];
  return act(async () => {
    pending.forEach(callback => callback(performance.now()));
  });
}

function type(textarea, value) {
  textarea.value = value;
  Simulate.change(textarea, { target: { value, selectionStart: value.length } });
}

function mount(value, onCommit, extraProps = {}) {
  const container = document.createElement('div');
  document.body.appendChild(container);
  const root = createRoot(container);
  act(() => {
    root.render(<LocalDraftTextarea value={value} onCommit={onCommit} {...extraProps} />);
  });
  const textarea = container.querySelector('textarea');
  return { container, root, textarea };
}

describe('LocalDraftTextarea', () => {
  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    frames = [];
    nextFrameID = 0;
    vi.stubGlobal('requestAnimationFrame', (callback) => {
      frames.push(callback);
      nextFrameID += 1;
      return nextFrameID;
    });
    vi.stubGlobal('cancelAnimationFrame', (id) => {
      frames = [];
    });
  });

  afterEach(() => {
    document.body.innerHTML = '';
    vi.unstubAllGlobals();
  });

  it('echoes keystrokes immediately while coalescing the model commit to one frame', async () => {
    const onCommit = vi.fn();
    const { root, textarea } = mount('start', onCommit);

    act(() => type(textarea, 'start-a'));
    act(() => type(textarea, 'start-ab'));

    expect(textarea.value).toBe('start-ab');
    expect(onCommit).not.toHaveBeenCalled();
    expect(frames).toHaveLength(1);

    await flushFrame();
    expect(onCommit).toHaveBeenCalledTimes(1);
    expect(onCommit).toHaveBeenCalledWith('start-ab');
    act(() => root.unmount());
  });

  it('flushes on blur without waiting for the animation frame', () => {
    const onCommit = vi.fn();
    const { root, textarea } = mount('before', onCommit);

    act(() => type(textarea, 'after'));
    act(() => Simulate.blur(textarea));

    expect(onCommit).toHaveBeenCalledWith('after');
    expect(frames).toHaveLength(0);
    act(() => root.unmount());
  });

  it('waits for IME composition to finish before committing', async () => {
    const onCommit = vi.fn();
    const { root, textarea } = mount('', onCommit);

    act(() => Simulate.compositionStart(textarea));
    act(() => type(textarea, '中文'));
    expect(textarea.value).toBe('中文');
    expect(frames).toHaveLength(0);
    expect(onCommit).not.toHaveBeenCalled();

    act(() => {
      textarea.value = '中文输入';
      Simulate.compositionEnd(textarea, { currentTarget: { value: '中文输入' } });
    });
    await flushFrame();
    expect(onCommit).toHaveBeenCalledWith('中文输入');
    act(() => root.unmount());
  });

  it('adopts an external value when the field is not focused', () => {
    const onCommit = vi.fn();
    const { root, textarea } = mount('old', onCommit);
    act(() => root.render(<LocalDraftTextarea value="external" onCommit={onCommit} />));
    expect(textarea.value).toBe('external');
    expect(onCommit).not.toHaveBeenCalled();
    act(() => root.unmount());
  });

  it('flushes the last local value when selection unmounts the field', () => {
    const onCommit = vi.fn();
    const { root, textarea } = mount('base', onCommit);
    act(() => type(textarea, 'last character'));
    act(() => root.unmount());
    expect(onCommit).toHaveBeenCalledWith('last character');
  });
});
