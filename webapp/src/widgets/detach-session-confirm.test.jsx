import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import {
  DETACH_SESSION_CONFIRM_ACTION,
  DETACH_SESSION_CONFIRM_MESSAGE,
  useDetachSessionConfirm,
} from './detach-session-confirm';

let container;
let root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  document.body.innerHTML = '';
  vi.restoreAllMocks();
});

function Harness({ action, app = { id: 'board', title: '课题调研' } }) {
  const { requestDetach, dialog } = useDetachSessionConfirm();
  return (
    <div>
      <button type="button" onClick={() => requestDetach(app, action)}>新页面打开</button>
      {dialog}
    </div>
  );
}

async function render(element) {
  await act(async () => root.render(element));
  return container;
}

function buttonByText(text) {
  return [...document.querySelectorAll('button')].find((button) => button.textContent.trim() === text);
}

// The dialog is portalled onto document.body so no ancestor's dimming or
// clipping can wash it out; queries therefore start at the document, not at the
// container this component was mounted in.
function dialogInDocument() {
  return document.querySelector('[role="alertdialog"]');
}

test('asks before detaching the application from the conversation', async () => {
  const action = vi.fn();
  await render(<Harness action={action} />);

  expect(container.textContent).not.toContain(DETACH_SESSION_CONFIRM_MESSAGE);
  await act(async () => buttonByText('新页面打开').click());

  const dialog = dialogInDocument();
  expect(dialog).not.toBeNull();
  expect(dialog.textContent).toContain(DETACH_SESSION_CONFIRM_MESSAGE);
  expect(dialog.textContent).toContain('课题调研');
  // Nothing happens until the user agrees.
  expect(action).not.toHaveBeenCalled();
});

test('opens only after confirmation', async () => {
  const action = vi.fn();
  await render(<Harness action={action} />);

  await act(async () => buttonByText('新页面打开').click());
  await act(async () => buttonByText(DETACH_SESSION_CONFIRM_ACTION).click());

  expect(action).toHaveBeenCalledTimes(1);
  expect(dialogInDocument()).toBeNull();
});

test('cancelling leaves the page exactly as it was', async () => {
  const action = vi.fn();
  await render(<Harness action={action} />);

  await act(async () => buttonByText('新页面打开').click());
  await act(async () => buttonByText('取消').click());

  expect(action).not.toHaveBeenCalled();
  expect(dialogInDocument()).toBeNull();
});

test('a second request replaces the first instead of stacking dialogs', async () => {
  const first = vi.fn();
  const second = vi.fn();
  function Two() {
    const { requestDetach, dialog } = useDetachSessionConfirm();
    return (
      <div>
        <button type="button" onClick={() => requestDetach({ id: 'a', title: '应用A' }, first)}>开A</button>
        <button type="button" onClick={() => requestDetach({ id: 'b', title: '应用B' }, second)}>开B</button>
        {dialog}
      </div>
    );
  }
  await render(<Two />);

  await act(async () => buttonByText('开A').click());
  await act(async () => buttonByText('开B').click());

  expect(document.querySelectorAll('[role="alertdialog"]')).toHaveLength(1);
  await act(async () => buttonByText(DETACH_SESSION_CONFIRM_ACTION).click());

  expect(first).not.toHaveBeenCalled();
  expect(second).toHaveBeenCalledTimes(1);
});

test('never calls an action that was not provided', async () => {
  await render(<Harness action={null} />);
  await act(async () => buttonByText('新页面打开').click());
  // No dialog, and no crash from a missing callback.
  expect(dialogInDocument()).toBeNull();
});
