import { missingInfoChoices, readMissingInfo } from './agent-persona';

const scenarios = [
  {
    id: 'dinner',
    question: '家里有鸡蛋和面条，晚饭吃什么？',
    reason: '两样食材都能用上，一口锅就能做。',
    step: '水开后下面条，快熟时打个蛋。',
    detail: '想吃拌面的话，把面捞出来沥干再拌就行。',
    voices: {
      自然随和: { short: '鸡蛋面。', answer: '做碗鸡蛋面吧。', ask: '你想吃带汤的，还是干拌的？' },
      正式专业: { short: '建议做鸡蛋面。', answer: '建议做一碗鸡蛋面。', ask: '您更喜欢汤面还是拌面？' },
      温和鼓励: { short: '来碗热乎的鸡蛋面吧。', answer: '这两样就够了，给自己煮碗热乎的鸡蛋面吧。', ask: '今晚更想喝点热汤，还是吃碗拌面呀？' },
      轻松俏皮: { short: '今晚鸡蛋面！', answer: '今晚让鸡蛋和面条搭个伙，煮碗蛋花面！', ask: '今晚站汤面队，还是拌面队？' },
    },
  },
  {
    id: 'weekend',
    question: '周末有一个小时，想出门走走，怎么安排？',
    reason: '路上花的时间少，就能把这一小时留给散步。',
    step: '挑一条熟悉的小路，走二十分钟就往回走。',
    detail: '剩下的时间可以在路边坐一会儿，再慢慢回家。',
    voices: {
      自然随和: { short: '在家附近散步。', answer: '就在家附近走一圈吧。', ask: '你想安静散散心，还是逛点热闹的地方？' },
      正式专业: { short: '建议就近散步。', answer: '建议选择住处附近的步行路线。', ask: '您希望沿途清静一些，还是有街市可逛？' },
      温和鼓励: { short: '去附近慢慢走一圈吧。', answer: '一个小时挺好，去附近慢慢走一圈吧。', ask: '这个下午，你更想松口气，还是去热闹的地方看看？' },
      轻松俏皮: { short: '出门遛个弯！', answer: '来趟家门口的小旅行吧！', ask: '这趟遛弯，是找清静还是凑热闹？' },
    },
  },
];

export const replyExampleScenarios = scenarios.map(({ id, question }) => ({ id, question }));

// These are authored examples. Free-form descriptions stay in the saved prompt.
export function getReplyExample(priority, parts, scenarioId = 'dinner') {
  const scene = scenarios.find(item => item.id === scenarioId) || scenarios[0];
  const tone = Object.keys(scene.voices).find(name => parts.includes('回复语气：' + name));
  const voice = scene.voices[tone || '自然随和'];
  const mode = priority?.value;
  const missing = readMissingInfo(parts);
  if (missing === missingInfoChoices[0]) return voice.ask;
  if (missing === missingInfoChoices[1]) {
    const open = scene.id === 'dinner' ? '汤面还是拌面待定。' : '路线待定，看你想逛街还是找个安静的地方。';
    const advice = mode === '尽量简短' ? voice.short : mode === '给出下一步' ? scene.step : voice.answer;
    return advice + (mode === '给出判断依据' ? scene.reason : '') + open;
  }
  if (mode === '给出下一步') return scene.step;
  if (mode === '有疑问先问我' || (!mode && parts.includes('回复模式：用问题引导我'))) return voice.ask;
  if (mode === '尽量简短') return voice.short;

  const brief = parts.includes('回复篇幅：尽量简短');
  const giveReason = mode === '给出判断依据';
  if (brief) return giveReason ? voice.short + scene.reason : voice.short;

  const detailed = parts.includes('回复篇幅：详细展开');
  if (giveReason) return voice.answer + scene.reason + (detailed ? scene.step : '');
  if (mode === '先说结论' || detailed) return voice.answer + scene.step + (detailed ? scene.detail : '');
  return voice.answer;
}
