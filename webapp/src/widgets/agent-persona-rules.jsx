import React, { useLayoutEffect, useRef, useState } from 'react';
import { Check, Plus, X } from 'lucide-react';
import { replaceChoiceLines } from '../utils/agent-persona';
import './agent-persona-rules.css';

const suggestions = [
  { occasion: '帮我做选择时', instruction: '先推荐一个，再告诉我最重要的理由。' },
  { occasion: '教我学新东西时', instruction: '先举个生活里的例子，再让我自己试一遍。' },
];

const boundarySuggestions = [
  { label: '替我发消息', text: '替我发消息前，先跟我确认。' },
  { label: '修改原文件', text: '修改原文件前，先跟我确认。' },
];

export default function AgentPersonaRules({ rules, boundaries, onChange, saving }) {
  const section = useRef(null);
  const focusAfterChange = useRef(null);
  const [touchedRule, setTouchedRule] = useState(null);
  const boundaryLines = boundaries.split(/(\r\n|\n|\r)/);

  useLayoutEffect(() => {
    if (!focusAfterChange.current) return;
    section.current?.querySelector(focusAfterChange.current)?.focus();
    focusAfterChange.current = null;
  }, [rules]);

  function addRule(suggestion) {
    const id = rules.reduce((highest, rule) => Math.max(highest, rule.id), -1) + 1;
    const rule = { id, occasion: '', instruction: '', ...suggestion };
    focusAfterChange.current = suggestion ? '#cc-persona-how-' + id : '#cc-persona-when-' + id;
    onChange({ rules: [...rules, rule] });
  }

  function editRule(id, field, value) {
    onChange({ rules: rules.map(rule => rule.id === id ? { ...rule, [field]: value } : rule) });
  }

  function removeRule(index) {
    const neighbor = rules[index + 1] || rules[index - 1];
    focusAfterChange.current = neighbor ? '#cc-persona-when-' + neighbor.id : '.cc-persona-add';
    if (touchedRule === rules[index].id) setTouchedRule(null);
    onChange({ rules: rules.filter((_, i) => i !== index) });
  }

  return <>
    <section className="cc-persona-rules" ref={section} aria-labelledby="cc-persona-rules-title">
      <div className="cc-persona-heading">
        <h3 id="cc-persona-rules-title">场景约定</h3>
        <button type="button" className="cc-persona-add" disabled={saving} onClick={() => addRule()}>
          <Plus size={15} aria-hidden="true" />自定义约定
        </button>
      </div>
      <div className="cc-persona-rule-suggestions">
        {suggestions.map(item => {
          const added = rules.some(rule => rule.occasion.trim() === item.occasion);
          const Icon = added ? Check : Plus;
          return <button type="button" key={item.occasion} disabled={saving || added} onClick={() => addRule(item)}>
            <Icon size={13} aria-hidden="true" />{item.occasion}
          </button>;
        })}
      </div>
      {rules.map((rule, index) => {
        const incomplete = Boolean(rule.occasion.trim() && !rule.instruction.trim());
        const showError = incomplete && touchedRule === rule.id;
        return <div className="cc-persona-rule" key={rule.id}>
          <label><span>什么场景（可选）</span>
            <input id={'cc-persona-when-' + rule.id} value={rule.occasion} disabled={saving}
              placeholder="例如：帮我改稿时" onChange={event => editRule(rule.id, 'occasion', event.target.value)} />
          </label>
          <label><span>希望助手怎么做</span>
            <textarea id={'cc-persona-how-' + rule.id} rows={2} value={rule.instruction} disabled={saving}
              placeholder="例如：先指出最值得改的一处"
              data-incomplete={incomplete || undefined} aria-invalid={showError || undefined}
              aria-describedby={showError ? 'cc-persona-error-' + rule.id : undefined}
              onBlur={() => setTouchedRule(rule.id)}
              onChange={event => editRule(rule.id, 'instruction', event.target.value)} />
            {showError && <small id={'cc-persona-error-' + rule.id}>写下希望助手做的事。</small>}
          </label>
          <button type="button" className="cc-persona-remove" aria-label={'移除第 ' + (index + 1) + ' 条约定'}
            disabled={saving} onClick={() => removeRule(index)}><X size={15} aria-hidden="true" /></button>
        </div>;
      })}
    </section>
    <section className="cc-persona-bounds">
      <div className="cc-persona-heading"><label htmlFor="cc-persona-boundaries">哪些事要先问过你？</label></div>
      <p className="cc-persona-hint">这里约定助手何时先询问。实际操作权限由设备与授权设置控制。</p>
      <div className="cc-persona-rule-suggestions">
        {boundarySuggestions.map(item => {
          const matches = line => line.trim() === item.text;
          const selected = boundaryLines.some(matches);
          const Icon = selected ? Check : Plus;
          return <button key={item.text} type="button" aria-pressed={selected} disabled={saving}
            onClick={() => onChange({ boundaries: replaceChoiceLines(boundaryLines, matches, selected ? '' : item.text) })}>
            <Icon size={13} aria-hidden="true" />{item.label}
          </button>;
        })}
      </div>
      <textarea id="cc-persona-boundaries" rows={2} value={boundaries} disabled={saving}
        placeholder="也可以写自己的约定" onChange={event => onChange({ boundaries: event.target.value })} />
    </section>
  </>;
}
