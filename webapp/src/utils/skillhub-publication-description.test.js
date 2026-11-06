import { normalizePublicationDescription as normalize, samePublicationDescription as same } from './skillhub-publication-description';

const id = `pubentry_${'a'.repeat(32)}`;
const fixture = () => ({ schemaVersion: 1, entryRequestId: id, botUid: '42', topicId: 'grp_77',
  taskId: `pubrepair_${'b'.repeat(32)}`, automaticRepairEnabled: false,
  draft: { confirmationKey: 'c'.repeat(64), round: 1, location: { relativePath: 'SKILL.md', field: 'description', entryIndex: 0 },
    text: '  Read documents.\r\n<script>untrusted</script>\t ', state: 'awaiting_confirmation' } });

it('retains exact text and clones the location without treating it as a filesystem path', () => {
  const input = fixture();
  const result = normalize(input, id);
  expect(result).toEqual(input);
  input.draft.location.entryIndex = 4;
  expect(result.draft.location.entryIndex).toBe(0);
});

it('supports skill.json without an entry index and exactly 8192 UTF8 bytes', () => {
  const v = fixture();
  v.draft.location = { relativePath: 'skill.json', field: 'description' };
  v.draft.text = '中'.repeat(2730) + 'ab';
  expect(normalize(v, id)).toEqual(v);
});

it.each([
  v => { v.entryRequestId = `pubentry_${'d'.repeat(32)}`; },
  v => { v.schemaVersion = 2; }, v => { v.automaticRepairEnabled = true; },
  v => { v.botUid = 42; }, v => { v.topicId = 'private'; }, v => { v.taskId = 'bad'; },
  v => { v.receipt = {}; }, v => { v.draft.privateBytes = 'secret'; },
  v => { v.draft.confirmationKey = 'c'.repeat(63); }, v => { v.draft.round = 4; },
  v => { v.draft.state = 'published'; }, v => { v.draft.text = ''; },
  v => { v.draft.text = '中'.repeat(2731); }, v => { v.draft.text = '\u0000secret'; },
  v => { v.draft.text = '\ud800'; }, v => { v.draft.location.entryIndex = -1; },
  v => { delete v.draft.location.entryIndex; }, v => { v.draft.location.relativePath = '../SKILL.md'; },
  v => { v.draft.location.field = 'name'; },
  v => { v.draft.location = { relativePath: 'skill.json', field: 'description', entryIndex: 0 }; },
  v => { v.topicId = [v.topicId]; }, v => { v.taskId = [v.taskId]; },
  v => { v.draft.confirmationKey = [v.draft.confirmationKey]; },
])('rejects malformed, substituted or private projections (%#)', mutate => {
  const v = fixture(); mutate(v);
  expect(() => normalize(v, id)).toThrow('格式不匹配');
});

it('compares all identity/text/selector fields while allowing only approval state to change', () => {
  const a = fixture();
  const b = fixture(); b.draft.state = 'confirmed';
  expect(same(a, b)).toBe(true);
  for (const mutate of [v => { v.botUid = '99'; }, v => { v.topicId = 'grp_88'; },
    v => { v.taskId = `pubrepair_${'d'.repeat(32)}`; }, v => { v.draft.text = v.draft.text.trim(); },
    v => { v.draft.round = 2; }, v => { v.draft.confirmationKey = 'e'.repeat(64); },
    v => { v.draft.location.entryIndex = 1; }]) {
    const changed = fixture(); mutate(changed);
    expect(same(a, changed)).toBe(false);
  }
});
