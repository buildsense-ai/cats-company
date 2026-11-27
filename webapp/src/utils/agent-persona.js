const emptyPersona = text => ({ description: text, rules: [], boundaries: '' });

// Keep an ordinary prompt verbatim. Extra fields use readable keys in the same
// cloud prompt, so user-entered line breaks and punctuation survive a round trip.
export function readPersona(text) {
  if (!text.trimStart().startsWith('{')) return emptyPersona(text);
  try {
    const data = JSON.parse(text);
    if (Object.keys(data).length !== 3 || typeof data.助手设定 !== 'string'
      || typeof data.行为边界 !== 'string' || !Array.isArray(data.场景约定)
      || !data.场景约定.every(rule => rule && Object.keys(rule).length === 2
        && typeof rule.场景 === 'string' && typeof rule.要求 === 'string')) {
      return emptyPersona(text);
    }
    return {
      description: data.助手设定,
      rules: data.场景约定.map(rule => ({ occasion: rule.场景, instruction: rule.要求 })),
      boundaries: data.行为边界,
    };
  } catch {
    return emptyPersona(text);
  }
}

export function writePersona({ description, rules, boundaries }) {
  const filled = rules.filter(rule => rule.occasion.trim() || rule.instruction.trim());
  if (!filled.length && !boundaries.trim() && readPersona(description).description === description) return description;
  return JSON.stringify({
    助手设定: description,
    场景约定: filled.map(rule => ({ 场景: rule.occasion, 要求: rule.instruction })),
    行为边界: boundaries,
  }, null, 2);
}

export const personaFields = {
  person: [
    { title: '相处身份', options: ['搭档', '教练', '学姐', '朋友'], example: '例如：和我一起做项目的学长' },
    { title: '怎么称呼我', label: '需要称呼时', optionLabels: { '直接用“你”': '自然称呼' }, options: ['直接用“你”', '同学', '伙伴'], example: '写下你喜欢的称呼' },
    { title: '性格', multi: true, options: ['坦率', '耐心', '幽默', '好奇', '沉稳'], example: '例如：慢热，但熟悉之后很健谈' },
    { title: '兴趣爱好', multi: true, options: ['阅读', '音乐', '电影', '旅行', '游戏', '画画'], example: '例如：古生物、收集黑胶唱片' },
    { title: '性别', background: true, options: ['女', '男'], example: '写下助手的性别设定' },
    { title: '虚拟身高', background: true, options: ['155 cm', '165 cm', '175 cm', '185 cm'], example: '例如：172.5 cm' },
    { title: '日常穿搭', background: true, options: ['休闲', '运动', '复古', '极简'], example: '例如：常穿一件蓝色工装外套' },
  ],
  reply: [
    { title: '回复模式', options: ['直接作答', '用问题引导我', '一起推敲'], example: '例如：教我做题时给提示，核对答案时直接说结果' },
    { title: '回复语气', options: ['自然随和', '正式专业', '温和鼓励', '轻松俏皮'], example: '例如：像一起共事很久的编辑' },
    { title: '回复篇幅', options: ['尽量简短', '适中', '详细展开'], example: '例如：平时三句话，复盘时可以详细' },
    { title: '回答结构', multi: true, options: ['先说结论', '给出判断依据', '分步说明', '举例解释', '列出选项'], example: '例如：先给一个可直接用的版本' },
    { title: '互动习惯', multi: true, options: ['有疑问先问我', '少说客套话', '提醒我看风险'], example: '例如：结束时只留一个值得追问的问题' },
    { title: '常用语', multi: true, options: ['交给我吧', '我有个想法', '搞定了'], example: '例如：收工，明天见！' },
  ],
};

export const replyPreferences = [
  { field: '回答结构', value: '先说结论', label: '先告诉我结论' },
  { field: '回复篇幅', value: '尽量简短', label: '只讲重点' },
  { field: '回答结构', value: '给出判断依据', label: '给建议时说清依据' },
  { field: '回答结构', value: '给出下一步', label: '告诉我下一步怎么做' },
];

export const missingInfoChoices = ['先问我', '先给一版，标出待定内容'];
const missingPrefix = '信息不足时：';
const oldAskLines = ['回复优先项：有疑问先问我', '互动习惯：有疑问先问我'];
export function readMissingInfo(parts) {
  return missingInfoChoices.find(value => parts.includes(missingPrefix + value))
    || (oldAskLines.some(line => parts.includes(line)) ? missingInfoChoices[0] : '');
}
export function chooseMissingInfo(parts, value) {
  return replaceChoiceLines(parts, line => line.startsWith(missingPrefix) || oldAskLines.includes(line),
    value ? missingPrefix + value : '');
}
const priorityPrefix = '回复优先项：';
const legacyPriorityLine = item => item.field + '：' + item.value;
const legacyPriorityLines = new Set(replyPreferences.map(legacyPriorityLine));
const lengthField = personaFields.reply.find(field => field.title === '回复篇幅');

// Recognize the old choices when reading. New detail controls only offer
// complementary choices, so a fine adjustment cannot silently select a priority.
export const replyDetailFields = personaFields.reply.map(field => ({
  ...field,
  options: field.options.filter(value => !legacyPriorityLines.has(field.title + '：' + value)
    && !oldAskLines.includes(field.title + '：' + value)),
}));

export function readReplyPriority(parts) {
  const explicit = parts.find(line => line.startsWith(priorityPrefix));
  if (explicit && explicit !== oldAskLines[0]) return replyPreferences.find(item => explicit === priorityPrefix + item.value) || null;
  const legacy = replyPreferences.filter(item => parts.includes(legacyPriorityLine(item)));
  return legacy.length === 1 ? legacy[0] : null;
}

// Preserve unrelated prose, including its original whitespace and line endings.
export function replaceChoiceLines(parts, matches, addition) {
  const remaining = [];
  let found = false;
  for (let i = 0; i < parts.length; i += 2) {
    let line = parts[i];
    if (matches(line)) {
      const replaceHere = !found && addition;
      found = true;
      if (!replaceHere) continue;
      line = addition;
    }
    if (remaining.length) remaining.push(parts[i - 1]);
    remaining.push(line);
  }
  const text = remaining.join('');
  if (found || !addition) return text;
  if (!text) return addition;
  if (/[\r\n]$/.test(text)) return text + addition + parts[parts.length - 2];
  return text + (parts[1] || '\n') + addition;
}

export function setPersonaChoice(parts, field, value, enabled) {
  const prefix = field.title + '：';
  const line = prefix + value;
  const matches = !field.multi && enabled
    ? item => item.startsWith(field.title + '（自定义）：') || (item.startsWith(prefix) && field.options.includes(item.slice(prefix.length)))
    : item => item === line;
  return replaceChoiceLines(parts, matches, enabled ? line : '');
}

export function chooseReplyPreference(parts, preference) {
  if (parts.includes(oldAskLines[0])) {
    parts = chooseMissingInfo(parts, readMissingInfo(parts)).split(/(\r\n|\n|\r)/);
  }
  const replaced = new Set(legacyPriorityLines);
  if (preference?.field === lengthField.title) {
    for (const value of lengthField.options) replaced.add(lengthField.title + '：' + value);
  }
  return replaceChoiceLines(parts, line => line.startsWith(priorityPrefix) || replaced.has(line),
    preference ? priorityPrefix + preference.value : '');
}

function pick(items) {
  return items[Math.floor(Math.random() * items.length)];
}

export function personaInspiration(previous = '', existing = '') {
  const lines = existing.split(/\r\n|\r|\n/);
  const supplement = Boolean(existing.trim());
  const last = previous.split('\n');
  const priority = supplement ? readReplyPriority(lines) : (
    pick(replyPreferences.filter(item => !last.includes(priorityPrefix + item.value))) || pick(replyPreferences)
  );
  const usingPriority = Boolean(priority) || lines.some(line => legacyPriorityLines.has(line) || line.startsWith(priorityPrefix));
  const detailFields = replyDetailFields.filter(({ title }) => {
    if (title === '回复模式' && usingPriority) return false;
    return title !== lengthField.title || priority?.field !== lengthField.title;
  });
  const available = [...personaFields.person.filter(field => !field.background), ...detailFields].filter(({ title }) =>
    !lines.some(line => line.startsWith(title + '：') || line.startsWith(title + '（自定义）：')));
  if (!available.length) {
    const ideas = ['改我的稿子时，尽量留住原来的语气。', '讨论新点子时，举个可以动手试的小例子。', '帮我复盘时，先问哪一件事值得保留。'];
    const unused = ideas.filter(idea => !lines.includes(idea));
    return pick(unused.filter(idea => idea !== previous)) || unused[0] || '';
  }
  // Empty forms get a full draft. For an existing description, offer a small
  // addition and leave every explicit choice, including custom values, intact.
  const suggestions = supplement ? [] : [priorityPrefix + priority.value, missingPrefix + pick(missingInfoChoices)];
  const count = supplement ? Math.min(3, available.length) : available.length;
  for (let i = 0; i < count; i++) {
    const index = supplement ? Math.floor(Math.random() * available.length) : 0;
    const [field] = available.splice(index, 1);
    const choices = field.options.map(option => field.title + '：' + option);
    suggestions.push(pick(choices.filter(line => !last.includes(line))) || pick(choices));
  }
  return suggestions.join('\n');
}
