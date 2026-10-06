import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  GATEWAY_ANNOTATION_DRAFT_MAX_ANNOTATIONS,
  GATEWAY_ANNOTATION_DRAFT_MAX_BYTES,
  GATEWAY_ANNOTATIONS_CONTRACT,
  gatewayAnnotationDraftKey,
  readGatewayAnnotationDrafts,
  writeGatewayAnnotationDrafts,
} from './gateway-annotation-drafts';

const USER_A = 'usr7';
const TOPIC_1 = 'topic-alpha';
const TOPIC_2 = 'topic-beta';
const AGENT_A = 42;
const AGENT_B = 99;
const APP_A = 'board';
const APP_B = 'sheet';

function sampleAnnotation(id = 'a1', overrides = {}) {
  return {
    id,
    kind: 'element',
    label: '发布按钮',
    body: '改成蓝色',
    target: { element_id: 'submit-btn', selector: 'button#submit-btn' },
    ...overrides,
  };
}

describe('gateway annotation drafts', () => {
  let storage;

  function createMemoryStorage() {
    const values = new Map();
    return {
      get length() { return values.size; },
      getItem(key) { return values.get(String(key)) ?? null; },
      setItem(key, value) { values.set(String(key), String(value)); },
      removeItem(key) { values.delete(String(key)); },
    };
  }

  beforeEach(() => {
    storage = createMemoryStorage();
  });

  it('round-trips one draft per topic × agent × app bucket', () => {
    writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, [sampleAnnotation()], storage);
    writeGatewayAnnotationDrafts(USER_A, TOPIC_2, AGENT_A, APP_A, [sampleAnnotation('b1')], storage);
    writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_B, APP_A, [sampleAnnotation('c1')], storage);
    writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_B, [sampleAnnotation('d1')], storage);

    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, storage).map(a => a.id)).toEqual(['a1']);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_2, AGENT_A, APP_A, storage).map(a => a.id)).toEqual(['b1']);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_B, APP_A, storage).map(a => a.id)).toEqual(['c1']);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_B, storage).map(a => a.id)).toEqual(['d1']);
  });

  it('isolates drafts between users', () => {
    writeGatewayAnnotationDrafts('usr1', TOPIC_1, AGENT_A, APP_A, [sampleAnnotation('mine')], storage);
    writeGatewayAnnotationDrafts('usr2', TOPIC_1, AGENT_A, APP_A, [sampleAnnotation('theirs')], storage);
    expect(readGatewayAnnotationDrafts('usr1', TOPIC_1, AGENT_A, APP_A, storage).map(a => a.id)).toEqual(['mine']);
  });

  it('drops invalid annotation rows without breaking the rest', () => {
    writeGatewayAnnotationDrafts(
      USER_A,
      TOPIC_1,
      AGENT_A,
      APP_A,
      [null, { id: '', kind: 'element', body: 'x', target: {} }, sampleAnnotation('ok'), { ...sampleAnnotation('bad-kind'), kind: 'point' }],
      storage,
    );
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, storage).map(a => a.id)).toEqual(['ok']);
  });

  it('clears the bucket when writing an empty list', () => {
    writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, [sampleAnnotation()], storage);
    writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, [], storage);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, storage)).toEqual([]);
  });

  it('never persists more than the ingestion bound of annotations', () => {
    const many = Array.from({ length: 30 }, (_, index) => sampleAnnotation(`a${index}`, { body: '短' }));
    writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, many, storage);
    const stored = readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, storage);
    expect(stored.length).toBe(GATEWAY_ANNOTATION_DRAFT_MAX_ANNOTATIONS);
  });

  it('refuses an oversized draft and preserves the previously persisted rows', () => {
    // A valid short draft first.
    const keep = [sampleAnnotation('keep', { body: 'keep-me' })];
    expect(writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, keep, storage)).toBe(true);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, storage).map(a => a.body)).toEqual(['keep-me']);

    // Adding oversized rows is refused as a whole; the previously persisted
    // rows survive and the caller gets a false result to surface as an error.
    const rows = [...keep, ...Array.from({ length: 3 }, (_, index) => sampleAnnotation(`heavy${index}`, {
      body: '批'.repeat(1900),
    }))];
    expect(writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, rows, storage)).toBe(false);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, storage).map(a => a.body)).toEqual(['keep-me']);

    // UTF-8 byte counting, not UTF-16 units: a single CJK body of 6000 chars
    // is ~18KiB of bytes but well under that in `.length`.
    const wide = sampleAnnotation('wide', { body: '批'.repeat(6000) });
    expect(writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, [wide], storage)).toBe(false);

    // An empty list still clears the bucket explicitly.
    expect(writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, [], storage)).toBe(true);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, storage)).toEqual([]);

    // A previously persisted bucket is never resurrect-safe from a refused
    // write: nothing was deleted, so the earlier keep-me rows are retrievable
    // right up to the explicit clear.
    expect(writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, keep, storage)).toBe(true);
    // ASCII bodies are ~2KiB per row, so enough rows must be stacked to
    // exceed the canonical 16KiB bound.
    expect(writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, [...keep, ...Array.from({ length: 10 }, (_, i2) => sampleAnnotation(`h2_${i2}`, { body: 'x'.repeat(1900) }))], storage)).toBe(false);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, storage).map(a => a.body)).toEqual(['keep-me']);
  });

  it('reports a storage refusal without destroying the last saved draft', () => {
    expect(writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, [sampleAnnotation('keep')], storage)).toBe(true);
    storage.setItem = () => { throw new Error('quota exceeded'); };
    expect(writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, [sampleAnnotation('new')], storage)).toBe(false);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, storage).map(row => row.id)).toEqual(['keep']);
  });

  it('returns an empty list when the bucket key is incomplete', () => {
    writeGatewayAnnotationDrafts(USER_A, '', AGENT_A, APP_A, [sampleAnnotation()], storage);
    expect(readGatewayAnnotationDrafts(USER_A, '', AGENT_A, APP_A, storage)).toEqual([]);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, 0, APP_A, storage)).toEqual([]);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, '', storage)).toEqual([]);
  });

  it('refuses to resurrect a bucket whose certificate names a different agent', () => {
    // Simulate a corrupted/tampered certificate: bucket key says agent 42,
    // certificate says 99. The rows must not resurrect under this bucket.
    const key = gatewayAnnotationDraftKey(TOPIC_1, AGENT_A, APP_A);
    writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, [sampleAnnotation()], storage);
    const stored = JSON.parse(
      storage.getItem(`catsco_gateway_annotation_drafts:v1:${USER_A}`),
    );
    stored[`${key}.meta`] = { ...storage.meta, agent_uid: 999 };
    storage.setItem(`catsco_gateway_annotation_drafts:v1:${USER_A}`, JSON.stringify({
      ...JSON.parse(storage.getItem(`catsco_gateway_annotation_drafts:v1:${USER_A}`)),
      [`${key}.meta`]: { topic_id: TOPIC_1, agent_uid: 999, app_id: APP_A, updated_at: 1 },
    }));
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, storage)).toEqual([]);
  });

  it('issues stable bucket keys', () => {
    expect(gatewayAnnotationDraftKey(TOPIC_1, AGENT_A, APP_A)).toBe(`${TOPIC_1}|42|${APP_A}`);
  });

  it('declares the frozen client contract value', () => {
    expect(GATEWAY_ANNOTATIONS_CONTRACT).toBe('catsco.gateway-annotations.v1');
  });
});
