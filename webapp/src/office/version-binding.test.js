import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  applyOfficeLatestVersion, isOfficeVersionFile, latestVersionFromUpload,
  localVersionStorageKey, officeVersionSource, performExplicitOfficeSync,
  performQuietOfficeSave, readLocalVersionBinding, resolveLatestOfficeVersion,
  writeLocalVersionBinding,
} from './version-binding.js';

const SOURCE = {
  topic_id: 'p2p_7_9',
  source_message_id: 71,
  source_attachment_index: 0,
  source_file_key: 'source.pptx',
};

const SOURCE_FILE = {
  name: 'source.pptx',
  file_key: 'source.pptx',
  url: '/uploads/files/source.pptx',
  mime_type: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  annotation_source: {
    topic_id: 'p2p_7_9',
    message_id: 71,
    attachment_index: 0,
    file_key: 'source.pptx',
  },
};

const LATEST = {
  file_key: 'latest.pptx',
  url: '/uploads/files/latest.pptx',
  name: 'latest.pptx',
  size: 8192,
  mime_type: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
};

describe('Office version identity and cache', () => {
  beforeEach(() => localStorage.clear());

  it('derives the immutable source triple from annotation identity', () => {
    expect(officeVersionSource(SOURCE_FILE)).toEqual(SOURCE);
    expect(isOfficeVersionFile(SOURCE_FILE)).toBe(true);
  });

  it('falls back to the URL basename only when no stored key exists', () => {
    const file = {
      name: 'deck.pptx',
      url: 'https://app.catsco.cc/uploads/files/source%20deck.pptx?download=1',
      annotation_source: { topic_id: 'p2p_7_9', message_id: 71, attachment_index: 1 },
    };
    expect(officeVersionSource(file)).toEqual({
      ...SOURCE, source_attachment_index: 1, source_file_key: 'source deck.pptx',
    });
  });

  it('keys local cache by topic, message and attachment, not just the file name', () => {
    const a = localVersionStorageKey(SOURCE);
    const b = localVersionStorageKey({ ...SOURCE, source_message_id: 72 });
    expect(a).not.toBe(b);
    expect(a).toContain(`.${SOURCE.source_message_id}.${SOURCE.source_attachment_index}`);
    expect(writeLocalVersionBinding(SOURCE, LATEST)).toBe(true);
    expect(readLocalVersionBinding(SOURCE)).toEqual(LATEST);
  });

  it('prefers the server binding and refreshes the local fallback cache', async () => {
    const getBinding = vi.fn().mockResolvedValue({ latest_file_key: 'latest.pptx',
      latest_url: '/uploads/files/latest.pptx', latest_name: 'latest.pptx',
      latest_size: 8192, latest_mime_type: LATEST.mime_type });
    const resolved = await resolveLatestOfficeVersion(SOURCE_FILE, getBinding);
    expect(getBinding).toHaveBeenCalledWith(SOURCE);
    expect(resolved).toMatchObject({
      file_key: 'latest.pptx', url: '/uploads/files/latest.pptx',
      office_version_unsynced: true, annotation_source: undefined,
      office_version_source: SOURCE,
    });
    expect(readLocalVersionBinding(SOURCE)).toEqual(LATEST);
  });

  it('uses the local pointer when the server is unavailable or returns no binding', async () => {
    writeLocalVersionBinding(SOURCE, LATEST);
    const unavailable = await resolveLatestOfficeVersion(
      SOURCE_FILE, vi.fn().mockRejectedValue(new Error('offline')),
    );
    const notFound = await resolveLatestOfficeVersion(SOURCE_FILE, vi.fn().mockResolvedValue(null));
    expect(unavailable.url).toBe(LATEST.url);
    expect(notFound.url).toBe(LATEST.url);
  });

  it('returns the original attachment when both server and local pointers are absent', async () => {
    const original = { ...SOURCE_FILE, office_version_source: SOURCE };
    const resolved = await resolveLatestOfficeVersion(original, vi.fn().mockResolvedValue(null));
    expect(resolved).toBe(original);
  });
});

describe('quiet save and explicit sync', () => {
  beforeEach(() => localStorage.clear());

  it('uploads and upserts a binding without sending a conversation message', async () => {
    const uploadFile = vi.fn().mockResolvedValue(LATEST);
    const upsertBinding = vi.fn().mockResolvedValue({ id: 1, ...LATEST });
    const result = await performQuietOfficeSave({
      file: SOURCE_FILE, bytes: new Uint8Array([1, 2, 3]), fileName: 'source.pptx',
      family: 'pptx', topicId: 'p2p_7_9', uploadFile, upsertBinding,
    });

    expect(uploadFile).toHaveBeenCalledTimes(1);
    expect(uploadFile.mock.calls[0][1]).toBe('file');
    expect(upsertBinding).toHaveBeenCalledWith({
      topic_id: 'p2p_7_9', source_message_id: 71, source_attachment_index: 0,
      source_file_key: 'source.pptx', latest_file_key: 'latest.pptx',
      latest_url: '/uploads/files/latest.pptx', latest_name: 'latest.pptx',
      latest_size: 8192, latest_mime_type: LATEST.mime_type,
    });
    expect(result).toMatchObject({
      localOnly: false,
      notice: '已保存最新版，但尚未发给小八；会话里仍是旧附件',
      nextFile: { file_key: 'latest.pptx', office_version_unsynced: true,
        annotation_source: undefined, office_version_source: SOURCE },
    });
    expect(readLocalVersionBinding(SOURCE)).toEqual(LATEST);
  });

  it('allows a local-only save but never fabricates a source binding', async () => {
    const uploadFile = vi.fn().mockResolvedValue(LATEST);
    const upsertBinding = vi.fn();
    const file = { name: 'orphan.pptx', file_key: 'orphan.pptx', url: '/uploads/files/orphan.pptx' };
    const result = await performQuietOfficeSave({
      file, bytes: new Uint8Array([1]), fileName: 'orphan.pptx', family: 'pptx',
      topicId: 'p2p_7_9', uploadFile, upsertBinding,
    });
    expect(upsertBinding).not.toHaveBeenCalled();
    expect(result.localOnly).toBe(true);
    expect(result.notice).toContain('仅本机可见');
    expect(localStorage.length).toBe(0);
  });

  it('keeps the new version reachable when the backend cannot record it', async () => {
    // The deployed backend may predate the version route. The save must still leave a local
    // pointer, otherwise reopening the original attachment silently shows the old bytes and
    // the user's edit looks lost even in the same browser.
    const uploadFile = vi.fn().mockResolvedValue(LATEST);
    const upsertBinding = vi.fn().mockRejectedValue(
      Object.assign(new Error('no route'), { code: 'office-version-unsupported' }),
    );

    const result = await performQuietOfficeSave({
      file: SOURCE_FILE, bytes: new Uint8Array([1, 2, 3]), fileName: 'source.pptx',
      family: 'pptx', topicId: 'p2p_7_9', uploadFile, upsertBinding,
    });

    expect(result.serverUnsupported).toBe(true);
    expect(result.nextFile.file_key).toBe('latest.pptx');
    expect(readLocalVersionBinding(SOURCE)).toMatchObject({ url: LATEST.url });

    // Reopening the original attachment must resolve to the saved version with no server help.
    const reopened = await resolveLatestOfficeVersion(SOURCE_FILE, vi.fn().mockResolvedValue(null));
    expect(reopened.url).toBe(LATEST.url);
    expect(reopened.office_version_unsynced).toBe(true);
  });

  it('sends exactly one message whose file payload is the current latest version', async () => {
    const sendMessage = vi.fn().mockResolvedValue({ id: 501, topic_id: 'p2p_7_9' });
    const result = await performExplicitOfficeSync({
      file: SOURCE_FILE, latestVersion: LATEST, family: 'pptx', topicId: 'p2p_7_9',
      sendMessage, slideCount: 4, changes: [{ op: 'text' }],
    });

    expect(sendMessage).toHaveBeenCalledTimes(1);
    expect(sendMessage).toHaveBeenCalledWith('p2p_7_9', {
      type: 'text',
      content: result.message.content,
      content_blocks: result.message.content_blocks,
    });
    expect(result.message.content_blocks.map(block => block.type)).toEqual(['text', 'file']);
    expect(result.message.payload).toEqual(LATEST);
    expect(result.message.content_blocks[1].payload).toEqual(LATEST);
    expect(result.message.content).toContain('4 页');
    expect(result.message.content).toContain('1 处');
  });

  it('refuses to sync when there is no latest upload', async () => {
    await expect(performExplicitOfficeSync({
      file: SOURCE_FILE, latestVersion: null, family: 'pptx', topicId: 'p2p_7_9',
      sendMessage: vi.fn(),
    })).rejects.toThrow('还没有可同步的最新版');
  });
});

describe('latest file merge', () => {
  it('clears annotation only when the displayed file actually changed', () => {
    const same = applyOfficeLatestVersion(SOURCE_FILE, SOURCE, latestVersionFromUpload(SOURCE_FILE));
    expect(same.annotation_source).toEqual(SOURCE_FILE.annotation_source);
    expect(same.office_version_unsynced).toBe(false);
    const changed = applyOfficeLatestVersion(SOURCE_FILE, SOURCE, LATEST);
    expect(changed.annotation_source).toBeUndefined();
    expect(changed.office_version_unsynced).toBe(true);
    expect(changed.office_version_source).toEqual(SOURCE);
    expect(changed.office_latest).toEqual(LATEST);
  });
});
