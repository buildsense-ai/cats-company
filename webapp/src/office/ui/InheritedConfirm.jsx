// Inherited-element confirmation.
//
// A shape whose geometry or formatting comes from the slide layout is shared by every
// slide using that layout. Editing it on one slide without asking is the fastest way
// to make a user believe the editor is broken, because other slides change too.
//
// Decision D5: default is "this slide only". Editing every slide using the layout is
// available but must be an explicit second choice, never the default.

import React from 'react';

const COPY = {
  geometry: {
    title: '这个元素来自版式',
    body: '它的位置由版式决定。改动可以只作用在这一页，也可以应用到所有使用同一版式的页。',
    only: '只改这一页',
    all: '所有使用该版式的页',
  },
  insert: {
    title: '插入元素',
    body: '插入会写在这一页上，不影响其他页。',
    only: '继续',
    all: null,
  },
  delete: {
    title: '删除这个元素',
    body: '这个元素来自版式。只从这一页移除不会影响其他页，但如果只删这一页，下次套用版式时它会重新出现。',
    only: '只在当前页隐藏',
    all: '从版式中删除（影响所有页）',
  },
};

export default function InheritedConfirm({ request, shape, onCancel, onConfirm }) {
  const kind = request?.kind || 'geometry';
  const copy = COPY[kind] || COPY.geometry;

  return (
    <div className="office-confirm-backdrop" role="dialog" aria-modal="true"
      style={{
        position: 'fixed', inset: 0, background: 'rgba(16,20,28,.32)', zIndex: 60,
        display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 20,
      }}
      onClick={(event) => { if (event.target === event.currentTarget) onCancel?.(); }}>
      <div style={{
        background: '#fff', borderRadius: 12, padding: '20px 22px', maxWidth: 420, width: '100%',
        boxShadow: '0 12px 40px rgba(16,20,28,.22)', fontSize: 13, lineHeight: 1.65,
      }}>
        <h3 style={{ margin: '0 0 8px', fontSize: 15, fontWeight: 650 }}>{copy.title}</h3>
        <p style={{ margin: '0 0 14px', color: '#474c56' }}>{copy.body}</p>
        {shape ? (
          <p style={{
            margin: '0 0 16px', fontSize: 11.5, color: '#7c818b',
            fontFamily: 'ui-monospace, Menlo, Consolas, monospace',
          }}>
            {shape.label} · {shape.elId}
          </p>
        ) : null}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', flexWrap: 'wrap' }}>
          <button type="button" onClick={() => onCancel?.()} style={btnGhost}>取消</button>
          {copy.all ? (
            <button type="button" onClick={() => onConfirm?.('all')} style={btnGhost}>
              {copy.all}
            </button>
          ) : null}
          <button type="button" onClick={() => onConfirm?.('only')} style={btnPrimary}>
            {copy.only}
          </button>
        </div>
      </div>
    </div>
  );
}

const btnGhost = {
  border: '1px solid #dcdfe5', background: '#fff', color: '#2c323d',
  borderRadius: 6, padding: '5px 12px', fontSize: 12.5, cursor: 'pointer',
};

const btnPrimary = {
  border: '1px solid #1f5eff', background: '#1f5eff', color: '#fff',
  borderRadius: 6, padding: '5px 14px', fontSize: 12.5, cursor: 'pointer',
};

export { COPY as INHERITED_CONFIRM_COPY };
