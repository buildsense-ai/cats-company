// Insert menu.
//
// Scope is fixed by decision D4: text box, picture, rectangle family, and table. Nothing
// else is offered. Each additional primitive multiplies the XML validation surface, and
// the ones left out (chart, SmartArt, equation) are whole projects on their own.
//
// The picture entry owns a hidden file input because a picture insert needs real bytes.
// Handing a path to the writer would produce a file that opens with an empty box.

import React, { useRef, useState } from 'react';

const PRIMITIVES = [
  { key: 'text', label: '文本框', hint: '一段可编辑文字' },
  { key: 'rect', label: '矩形', hint: '实心矩形' },
  { key: 'roundRect', label: '圆角矩形', hint: '带圆角的矩形' },
  { key: 'line', label: '直线', hint: '水平细线，可拖动' },
  { key: 'table', label: '表格', hint: '默认 3 × 3' },
  { key: 'picture', label: '图片', hint: '选择本地图片' },
];

export default function InsertMenu({ onInsert, onPicture, disabled }) {
  const [open, setOpen] = useState(false);
  const inputRef = useRef(null);

  const pick = (key) => {
    setOpen(false);
    if (key === 'picture') {
      inputRef.current?.click?.();
      return;
    }
    onInsert?.(key);
  };

  return (
    <div className="office-insert">
      <button type="button" className="office-page-btn" disabled={disabled}
        onClick={() => setOpen(value => !value)} aria-expanded={open}>
        插入
      </button>
      {open ? (
        <ul className="office-insert-menu">
          {PRIMITIVES.map(item => (
            <li key={item.key}>
              <button type="button" onClick={() => pick(item.key)}>
                <span className="office-insert-label">{item.label}</span>
                <span className="office-insert-hint">{item.hint}</span>
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      <input
        ref={inputRef}
        type="file"
        accept="image/png,image/jpeg,image/gif,image/webp,image/svg+xml"
        style={{ display: 'none' }}
        onChange={(event) => {
          const file = event.target.files?.[0];
          // Reset the input so picking the same file twice still fires a change event.
          event.target.value = '';
          if (file) onPicture?.(file);
        }}
      />
    </div>
  );
}

export { PRIMITIVES };
