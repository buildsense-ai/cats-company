import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import CommandPalette, { CHAT_COMMANDS, commandMatchesDraft } from './command-palette';

describe('command palette', () => {
  it('offers only the three supported commands and filters partial drafts', () => {
    expect(commandMatchesDraft('/')?.commands).toEqual(CHAT_COMMANDS);
    expect(commandMatchesDraft('/co')?.commands.map((command) => command.name)).toEqual(['/compact']);
    expect(commandMatchesDraft('/停止')?.commands.map((command) => command.name)).toEqual(['/stop']);
    expect(commandMatchesDraft('普通消息')).toBeNull();
  });

  it('selects the highlighted command with the same callback used by a click', async () => {
    const container = document.createElement('div');
    document.body.appendChild(container);
    const root = createRoot(container);
    const onSelect = vi.fn();
    const onHighlight = vi.fn();
    await act(async () => root.render(
      <CommandPalette
        commands={CHAT_COMMANDS}
        activeIndex={1}
        onSelect={onSelect}
        onHighlight={onHighlight}
      />,
    ));

    const options = container.querySelectorAll('[role="option"]');
    expect(options).toHaveLength(3);
    expect(options[1].getAttribute('aria-selected')).toBe('true');
    await act(async () => options[2].dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true })));
    expect(onSelect).toHaveBeenCalledWith(CHAT_COMMANDS[2]);
    await act(async () => Simulate.mouseEnter(options[0]));
    expect(onHighlight).toHaveBeenCalledWith(0);

    await act(async () => root.unmount());
    container.remove();
  });
});
