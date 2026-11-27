import React, { useId, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Dice5, Plus, RefreshCw, Undo2 } from 'lucide-react';
import {
  chooseReplyPreference, personaFields, personaInspiration, readPersona, readReplyPriority,
  replyDetailFields, replyPreferences, writePersona, readMissingInfo, chooseMissingInfo, missingInfoChoices,
} from '../utils/agent-persona';
import { getReplyExample, replyExampleScenarios } from '../utils/persona-reply-example';
import AgentDetailTabs from './agent-detail-tabs';
import PersonaChoices, { personaTabs } from './agent-persona-choices';
import AgentPersonaRules from './agent-persona-rules';
import AgentPersonaTrial from './agent-persona-trial';
import AgentPersonaPortrait from './agent-persona-portrait';

export default function AgentPersonaEditor({ uid, ownerUID, agent, onAvatarApplied, onPortraitStateChange, saved, draft, onChange, saving, tab, onTabChange }) {
  const [form, setForm] = useState(() => {
    const initial = readPersona(draft);
    return { ...initial, rules: initial.rules.map((rule, id) => ({ ...rule, id })) };
  });
  const [exampleIndex, setExampleIndex] = useState(0);
  const [suggestion, setSuggestion] = useState('');
  const [undoDescription, setUndoDescription] = useState(null);
  const [ideasExhausted, setIdeasExhausted] = useState(false);
  const previousIdea = useRef('');
  const bodyRef = useRef(null);
  const diceRef = useRef(null);
  const radioGroup = useId();

  const parts = useMemo(() => form.description.split(/(\r\n|\n|\r)/), [form.description]);
  const priority = readReplyPriority(parts);
  const missingInfo = readMissingInfo(parts);
  const legacyChoices = replyPreferences.filter(item => parts.includes(item.field + '：' + item.value));
  const scenario = replyExampleScenarios[exampleIndex];
  const rerolling = undoDescription !== null && !undoDescription.trim();
  const rollLabel = suggestion ? '换一组建议' : rerolling ? '换一份草稿' : form.description.trim() ? '补全设定' : '随机起草';

  useLayoutEffect(() => {
    bodyRef.current.scrollTop = 0;
  }, [tab]);

  function update(next) {
    setForm(next);
    const incomplete = next.rules.some(rule => rule.occasion.trim() && !rule.instruction.trim());
    onChange(writePersona(next), incomplete);
  }

  function editDescription(value) {
    setUndoDescription(null);
    setSuggestion('');
    setIdeasExhausted(false);
    update({ ...form, description: value });
  }

  function generate() {
    const idea = personaInspiration(previousIdea.current, rerolling ? '' : form.description);
    previousIdea.current = idea;
    setIdeasExhausted(!idea);
    bodyRef.current.scrollTop = 0;
    if (!form.description.trim() || rerolling) {
      setUndoDescription(undoDescription ?? form.description);
      update({ ...form, description: idea });
      setSuggestion('');
    } else {
      setSuggestion(idea);
    }
  }

  function acceptSuggestion() {
    setUndoDescription(form.description);
    update({ ...form, description: form.description + '\n\n' + suggestion });
    setSuggestion('');
    diceRef.current?.focus();
  }

  function undoGeneration() {
    update({ ...form, description: undoDescription });
    setUndoDescription(null);
    setSuggestion('');
    setIdeasExhausted(false);
    diceRef.current?.focus();
  }

  function renderChoices(field) {
    return <PersonaChoices key={field.title} field={field} parts={parts} disabled={saving} onChange={editDescription} />;
  }

  function clearPreference(event, nextDescription) {
    event.currentTarget.closest('fieldset').querySelector('input').focus();
    editDescription(nextDescription);
  }

  return <>
    <div className="cc-persona-tabs">
      <AgentDetailTabs ariaLabel="助手设定分类" idPrefix="cc-persona" tabs={personaTabs} value={tab} onChange={onTabChange} />
      <button type="button" className="cc-persona-roll" ref={diceRef} onClick={generate} disabled={saving}
        aria-label={rollLabel} title={rollLabel}>
        <Dice5 size={16} aria-hidden="true" /><span>{rollLabel}</span>
      </button>
    </div>
    <div className="cc-persona-body" id="cc-persona-content" role="tabpanel" aria-labelledby={'cc-persona-tab-' + tab} ref={bodyRef}>
      {suggestion ? <aside className="cc-persona-idea" aria-label="待加入的设定">
        <h3>建议补充</h3>
        <p role="status">{suggestion}</p>
        <div className="cc-persona-tools">
          <button type="button" disabled={saving} onClick={() => { setSuggestion(''); diceRef.current?.focus(); }}>收起</button>
          <button type="button" disabled={saving} onClick={acceptSuggestion}><Plus size={14} aria-hidden="true" />加入我的设定</button>
        </div>
      </aside> : ideasExhausted ? <p className="cc-persona-hint" role="status">当前设定已齐全。</p> : null}
      {undoDescription !== null && !suggestion && <div className="cc-persona-undo-row">
        {rerolling ? <details><summary>查看随机草稿</summary><p>{form.description}</p></details> : <span role="status">已加入草稿</span>}
        <button type="button" className="cc-persona-undo" disabled={saving} onClick={undoGeneration}>
          <Undo2 size={14} aria-hidden="true" />撤回
        </button>
      </div>}
      {tab === 'reply' && <>
        <fieldset className="cc-reply-preferences" disabled={saving}>
          <legend>回复时，你最希望助手做到哪一点？
            {(priority || legacyChoices.length > 0) && <button type="button" className="cc-preference-clear" disabled={saving}
              aria-label="清除回复优先项" onClick={event => clearPreference(event, chooseReplyPreference(parts, null))}>清除</button>}
          </legend>
          {!priority && legacyChoices.length > 1 && <p className="cc-persona-hint">已保留原来的组合偏好，选一项即可切换。</p>}
          <div>{replyPreferences.map(item => <label key={item.value}>
            <input type="radio" name={radioGroup} value={item.value} checked={priority === item}
              onChange={() => editDescription(chooseReplyPreference(parts, item))} />
            {item.label}
          </label>)}</div>
        </fieldset>
        <fieldset className="cc-reply-preferences cc-persona-missing" disabled={saving}>
          <legend>缺少关键信息时，怎么做？
            {missingInfo && <button type="button" className="cc-preference-clear" aria-label="清除信息不足时的偏好" disabled={saving}
              onClick={event => clearPreference(event, chooseMissingInfo(parts, ''))}>清除</button>}
          </legend>
          <div>{missingInfoChoices.map(value => <label key={value}>
            <input type="radio" name={radioGroup + '-missing'} value={value} checked={missingInfo === value}
              onChange={() => editDescription(chooseMissingInfo(parts, value))} />{value}
          </label>)}</div>
        </fieldset>
        <section className="cc-persona-example" aria-label="回复示例">
          <div className="cc-persona-heading">
            <h3>回复示例</h3>
            <button type="button" onClick={() => setExampleIndex((exampleIndex + 1) % replyExampleScenarios.length)}>
              <RefreshCw size={13} aria-hidden="true" />换个问题
            </button>
          </div>
          <p><span>你</span>{scenario.question}</p>
          <p aria-live="polite" aria-atomic="true"><span>助手</span>{getReplyExample(priority, parts, scenario.id)}</p>
          <details className="cc-persona-disclosure cc-persona-followups">
            <summary>看后续对话</summary>
            {scenario.followups.map(([question, answer]) => <div key={question}>
              <p><span>你</span>{question}</p>
              <p><span>助手</span>{answer}</p>
            </div>)}
          </details>
        </section>
      </>}
      <AgentPersonaTrial uid={uid} saved={saved} draft={draft} hidden={tab !== 'reply'}
        disabled={saving || form.rules.some(r => r.occasion.trim() && !r.instruction.trim())} />
      {tab === 'reply' && <>
        <details className="cc-persona-disclosure">
          <summary>更多回复设置</summary>
          {replyDetailFields.map(field => {
            if (field.title !== '回复篇幅' || priority?.value !== '尽量简短') return renderChoices(field);
            return parts.some(line => line.startsWith('回复篇幅（自定义）：'))
              ? renderChoices({ ...field, options: [] }) : null;
          })}
        </details>
      </>}
      <AgentPersonaPortrait key={JSON.stringify([ownerUID, uid])} ownerUID={ownerUID} agent={agent} description={form.description} hidden={tab !== 'person'}
        saving={saving} onApplied={onAvatarApplied} onStateChange={onPortraitStateChange} />
      {tab === 'person' && <>
        {personaFields.person.filter(field => !field.background).map(renderChoices)}
        <details className="cc-persona-disclosure">
          <summary>角色背景</summary>
          {personaFields.person.filter(field => field.background).map(renderChoices)}
        </details>
      </>}
      {tab === 'rules' ? <AgentPersonaRules rules={form.rules} boundaries={form.boundaries}
        saving={saving} onChange={patch => update({ ...form, ...patch })} />
        : <details className="cc-persona-disclosure" key={'description-' + tab}>
          <summary>自由编辑描述</summary>
          <textarea id="cc-persona-description" aria-label="助手描述" className="cc-persona-description" rows={4}
            value={form.description} disabled={saving} onChange={event => editDescription(event.target.value)}
            placeholder="写下希望助手记住的做法或说话习惯" />
        </details>}
    </div>
  </>;
}
