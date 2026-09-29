import React from 'react';

export const CHAT_COMMANDS = [
  { name: '/compact', label: '压缩上下文', description: '保存检查点并整理较早的对话' },
  { name: '/stop', label: '停止当前处理', description: '中断正在运行的请求' },
  { name: '/clear', label: '清空上下文', description: '清空当前会话的上下文' },
];

export function commandMatchesDraft(value) {
  const match = /^\s*\/([^\s]*)$/u.exec(String(value || ''));
  if (!match) return null;
  const query = match[1].toLowerCase();
  const commands = CHAT_COMMANDS.filter((command) => (
    !query
      || command.name.slice(1).startsWith(query)
      || command.label.includes(query)
  ));
  return { query, commands };
}

export default function CommandPalette({ commands, activeIndex, onSelect, onHighlight }) {
  if (!commands?.length) return null;
  const safeIndex = Math.min(Math.max(activeIndex, 0), commands.length - 1);
  return (
    <div id="command-palette" className="oc-command-palette v3-composer-command-palette" role="listbox" aria-label="可用命令">
      {commands.map((command, index) => (
        <button
          key={command.name}
          id={`command-option-${command.name.slice(1)}`}
          className={`oc-command-item${index === safeIndex ? ' is-active' : ''}`}
          type="button"
          role="option"
          aria-selected={index === safeIndex}
          onMouseDown={(event) => {
            event.preventDefault();
            onSelect(command);
          }}
          onMouseEnter={() => onHighlight?.(index)}
        >
          <span className="oc-command-name">{command.name}</span>
          <span className="oc-command-copy">
            <span className="oc-command-label">{command.label}</span>
            <span className="oc-command-description">{command.description}</span>
          </span>
        </button>
      ))}
    </div>
  );
}
