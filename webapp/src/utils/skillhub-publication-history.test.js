import { normalizePublicationHistory } from './skillhub-publication-history';

const id = `pubentry_${'a'.repeat(32)}`;
const recordedAt = '2026-10-29T04:00:00Z';
const published = { stage: 'published', recordedAt,
  result: { skillId: 'author/test', version: '1.0.1', publishedAt: recordedAt } };
const response = history => ({ schemaVersion: 1, entryRequestId: id, descriptionReleaseHistory: history });

it('projects only explicit unknown or validated terminal history', () => {
  expect(normalizePublicationHistory(response({ stage: 'not_recorded' }), id)).toEqual({ stage: 'not_recorded' });
  expect(normalizePublicationHistory(response(published), id)).toEqual(published);
  const failure = { stage: 'publish_failed', recordedAt,
    code: 'skillhub.publication_description_release_commit_rejected' };
  expect(normalizePublicationHistory(response(failure), id)).toEqual(failure);
});

it.each([
  { stage: 'publishing' },
  { stage: 'not_recorded', result: published.result },
  { stage: 'not_recorded', recordedAt },
  { stage: 'published', result: published.result },
  { ...published, recordedAt: 'not a date' },
  { ...published, recordedAt: '2026-10-29' },
  { ...published, packageId: 'private-package' },
  { ...published, result: { ...published.result, checksum: 'private-hash' } },
  { ...published, result: { ...published.result, version: '' } },
  { ...published, result: { ...published.result, skillId: 'a'.repeat(257) } },
  { stage: 'publish_failed', recordedAt, code: '<script>raw peer error</script>' },
  { stage: 'publish_failed', recordedAt, code: 'skillhub.publication_description_release_commit_rejected', result: null },
])('rejects malformed, overbroad or fabricated history: %j', value => {
  expect(() => normalizePublicationHistory(response(value), id)).toThrow();
});

it('binds the response to the exact request and returns detached display fields', () => {
  expect(() => normalizePublicationHistory(response(published), `pubentry_${'b'.repeat(32)}`)).toThrow();
  expect(() => normalizePublicationHistory({ ...response(published), private: 'secret' }, id)).toThrow();
  expect(() => normalizePublicationHistory({ ...response(published), schemaVersion: 2 }, id)).toThrow();
  expect(() => normalizePublicationHistory(response(published), '../path')).toThrow();
  const copy = normalizePublicationHistory(response(published), id);
  copy.result.skillId = 'changed';
  expect(published.result.skillId).toBe('author/test');
});
