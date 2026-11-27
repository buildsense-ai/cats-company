import React, { useRef, useState } from 'react';
import { Check, Circle, CircleDot, PencilLine, Plus } from 'lucide-react';

import { replaceChoiceLines, setPersonaChoice } from '../utils/agent-persona';

export const personaTabs = [['reply', '回复方式'], ['person', '人物设定'], ['rules', '场景与边界']];

export default function PersonaChoices({ field, parts, disabled, onChange }) {
  const { title, options, multi, example } = field;
  const [writing, setWriting] = useState(false);
  const trigger = useRef(null);
  const prefix = title + '：';
  const customPrefix = title + '（自定义）：';
  const custom = parts.find(line => line.startsWith(customPrefix))?.slice(customPrefix.length) || '';
  const owned = line => line.startsWith(customPrefix) || (line.startsWith(prefix) && options.includes(line.slice(prefix.length)));

  function toggle(label) {
    const sentence = prefix + label;
    const selected = parts.includes(sentence);
    onChange(setPersonaChoice(parts, field, label, !selected));
    if (!multi) setWriting(false);
  }

  function setCustom(text) {
    const matches = !multi && text ? owned : line => line.startsWith(customPrefix);
    onChange(replaceChoiceLines(parts, matches, text.trim() ? customPrefix + text : ''));
  }

  return <fieldset className="cc-persona-choices" disabled={disabled}>
    <legend>{field.label || title}{multi && <small>可多选</small>}</legend>
    <div>{options.map(label => {
      const selected = parts.includes(prefix + label);
      const Icon = multi ? (selected ? Check : Plus) : (selected ? CircleDot : Circle);
      return <button key={label} type="button" aria-pressed={selected}
        onClick={() => toggle(label)}><Icon size={13} aria-hidden="true" />{field.optionLabels?.[label] || label}</button>;
    })}
      <button type="button" className="cc-persona-custom" ref={trigger} aria-label={'自定义' + title}
        data-filled={Boolean(custom)} aria-expanded={writing} aria-description={custom || undefined} onClick={() => setWriting(!writing)}>
        <PencilLine size={13} aria-hidden="true" /><span>{custom || '自定义'}</span>
      </button>
      {writing && <input autoFocus className="cc-persona-choice-input" aria-label={'填写' + title}
        placeholder={example} value={custom} onChange={event => setCustom(event.target.value)}
        onKeyDown={event => {
          if (event.nativeEvent.isComposing || !['Enter', 'Escape'].includes(event.key)) return;
          event.preventDefault(); event.stopPropagation(); setWriting(false); trigger.current?.focus();
        }} />}
    </div>
  </fieldset>;
}
