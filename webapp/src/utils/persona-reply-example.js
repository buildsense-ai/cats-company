import { missingInfoChoices, readMissingInfo } from './agent-persona';

const scenarios = [
  {
    id: 'dinner',
    question: '家里有鸡蛋和面条，晚饭吃什么？',
    reason: '两样食材都能用上，一口锅就能做。',
    step: '水开后下面条，快熟时打个蛋。',
    detail: '想吃拌面的话，把面捞出来沥干再拌就行。',
    followups: [
      ['想吃干拌的。', '那就把面捞出来拌点酱油，煎个蛋盖在上面。'],
      ['行，今晚就吃这个。', '好，趁热吃。'],
    ],
    voices: {
      自然随和: { short: '鸡蛋面。', answer: '做碗鸡蛋面吧。', ask: '你想吃带汤的，还是干拌的？' },
      正式专业: { short: '建议做鸡蛋面。', answer: '建议做一碗鸡蛋面。', ask: '您更喜欢汤面还是拌面？' },
      温和鼓励: { short: '来碗热乎的鸡蛋面吧。', answer: '这两样就够了，给自己煮碗热乎的鸡蛋面吧。', ask: '今晚更想喝点热汤，还是吃碗拌面呀？' },
      轻松俏皮: { short: '今晚鸡蛋面！', answer: '今晚让鸡蛋和面条搭个伙，煮碗蛋花面！', ask: '今晚站汤面队，还是拌面队？' },
    },
  },
  {
    id: 'client',
    question: '原定周四交稿，改到周五。帮我写一句给客户。',
    followups: [
      ['再短一点。', '您好，交稿改到周五，抱歉。'],
      ['就用这版。', '好。'],
    ],
  },
  {
    id: 'meeting',
    question: '明天的组会改时间了，帮我写条通知。',
    followups: [
      ['改到下午三点。', '明天组会改到下午三点，大家到时见。'],
      ['就这样。', '好。'],
    ],
  },
];

export const replyExampleScenarios = scenarios.map(({ id, question, followups }) => ({ id, question, followups }));

// These are authored examples. Free-form descriptions stay in the saved prompt.
export function getReplyExample(priority, parts, scenarioId = 'dinner') {
  const scene = scenarios.find(item => item.id === scenarioId) || scenarios[0];
  const mode = priority?.value;
  if (scene.id === 'client') {
    return mode === '尽量简短' ? '您好，交稿改到周五，给您添麻烦了。'
      : '您好，原定周四交付的稿件将于周五发给您，抱歉让您久等。';
  }
  if (scene.id === 'meeting') {
    return readMissingInfo(parts) === missingInfoChoices[1] ? '明天组会改到【新时间】，请大家按新时间参会。' : '改到几点了？';
  }
  const tone = Object.keys(scene.voices).find(name => parts.includes('回复语气：' + name));
  const voice = scene.voices[tone || '自然随和'];
  if (mode === '给出下一步') return scene.step;
  if (!mode && parts.includes('回复模式：用问题引导我')) return voice.ask;
  if (mode === '尽量简短') return voice.short;

  const brief = parts.includes('回复篇幅：尽量简短');
  const giveReason = mode === '给出判断依据';
  if (brief) return giveReason ? voice.short + scene.reason : voice.short;

  const detailed = parts.includes('回复篇幅：详细展开');
  if (giveReason) return voice.answer + scene.reason + (detailed ? scene.step : '');
  if (mode === '先说结论' || detailed) return voice.answer + scene.step + (detailed ? scene.detail : '');
  return voice.answer;
}
