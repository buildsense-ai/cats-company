import { normalizePublicationRepairTask as normalize, repairTaskStateLabel, validRepairScope, validRepairTopic } from './skillhub-publication-repair-task';

const id = `pubentry_${'a'.repeat(32)}`;
const fixture = (state = 'queued', roundsUsed = 0) => ({ schemaVersion: 1, entryRequestId: id, botUid: '42',
  topicId: 'grp_77', taskId: `pubrepair_${'b'.repeat(32)}`, state, roundsUsed,
  maxFormatRepairRounds: 3, automaticRepairEnabled: false });

it.each([['queued', 0], ['queued', 1], ['queued', 2], ['running', 1], ['running', 3],
  ['waiting_confirmation', 2], ['validated', 1], ['stopped', 1], ['stopped_no_progress', 1], ['stopped_budget', 3]])(
  'reads a retained %s / %s checkpoint, never resetting budget', (state, round) => {
    const input = fixture(state, round);
    expect(normalize(input, id, '42')).toEqual(input);
    expect(normalize(input, id, '42')).not.toBe(input);
    expect(repairTaskStateLabel(state)).toBeTruthy();
  },
);

it('permits an original actor to discover a different existing topic without rebinding it', () => {
  const v = fixture(); v.topicId = 'grp_88';
  expect(normalize(v, id, '42').topicId).toBe('grp_88');
});

it.each([
  v => { v.entryRequestId = `pubentry_${'d'.repeat(32)}`; }, v => { v.schemaVersion = 2; },
  v => { v.botUid = '43'; }, v => { v.botUid = 42; }, v => { v.topicId = 'grp_077'; },
  v => { v.topicId = 'grp_9223372036854775808'; }, v => { v.taskId = 'bad'; },
  v => { v.taskId = [v.taskId]; }, v => { v.topicId = [v.topicId]; },
  v => { v.automaticRepairEnabled = true; }, v => { v.maxFormatRepairRounds = 4; },
  v => { v.roundsUsed = -1; }, v => { v.roundsUsed = 4; }, v => { v.roundsUsed = 0.5; },
  v => { v.roundsUsed = '0'; }, v => { v.roundsUsed = 3; }, v => { v.state = 'running'; },
  v => { v.state = 'stopped_budget'; }, v => { v.state = 'validated'; },
  v => { v.state = 'waiting_confirmation'; }, v => { v.state = 'published'; },
  v => { v.state = 'stopped'; }, v => { v.state = 'stopped_no_progress'; },
  v => { v.receipt = 'private'; }, v => { delete v.taskId; },
])('rejects incoherent or private task projections (%#)', mutate => {
  const v = fixture(); mutate(v);
  expect(() => normalize(v, id, '42')).toThrow('格式不匹配');
});

it.each(['grp_0', 'grp_01', 'grp_-1', 'grp_1/other', 'grp_9223372036854775808', 'grp_100000000000000000000', null, 77, [['grp_77']]])(
  'rejects noncanonical topics: %s', value => expect(validRepairTopic(value)).toBe(false),
);
it('accepts canonical int64 boundary identifiers without Number precision loss', () => {
  expect(validRepairScope(id, '9223372036854775807', 'grp_9223372036854775807')).toBe(true);
  expect(validRepairScope(id, '9223372036854775808', 'grp_77')).toBe(false);
  expect(validRepairScope([id], '42', 'grp_77')).toBe(false);
  expect(validRepairScope(id, 42, 'grp_77')).toBe(false);
});

it('never labels ledger state as proof of a live executor or publication', () => {
  expect(repairTaskStateLabel('queued')).toContain('尚未启动自动修复');
  expect(repairTaskStateLabel('running')).toContain('不代表执行器当前仍在运行');
  expect(repairTaskStateLabel('validated')).toContain('不代表已发布');
});
