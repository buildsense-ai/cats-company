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

  it('trims an oversized draft until it fits the 16KiB bound', () => {
    const heavy = Array.from({ length: 10 }, (_, index) => sampleAnnotation(`a${index}`, {
      body: 'x'.repeat(1900), // 10 × ~1.9KB > 16KiB
    }));
    writeGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, heavy, storage);
    const stored = readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, APP_A, storage);
    expect(stored.length).toBeGreaterThan(0);
    expect(JSON.stringify(stored).length).toBeLessThanOrEqual(GATEWAY_ANNOTATION_DRAFT_MAX_BYTES);
  });

  it('returns an empty list when the bucket key is incomplete', () => {
    writeGatewayAnnotationDrafts(USER_A, '', AGENT_A, APP_A, [sampleAnnotation()], storage);
    expect(readGatewayAnnotationDrafts(USER_A, '', AGENT_A, APP_A, storage)).toEqual([]);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, 0, APP_A, storage)).toEqual([]);
    expect(readGatewayAnnotationDrafts(USER_A, TOPIC_1, AGENT_A, '', storage)).toEqual([]);
  });

  it('issues stable bucket keys', () => {
    expect(gatewayAnnotationDraftKey(TOPIC_1, AGENT_A, APP_A)).toBe(`${TOPIC_1}|42|${APP_A}`);
  });

  it('declares the frozen client contract value', () => {
    expect(GATEWAY_ANNOTATIONS_CONTRACT).toBe('catsco.gateway-annotations.v1');
  });
});
