import { beforeEach, describe, expect, it } from 'vitest';
import {
  collapsePendingVersions, listPendingVersionDocuments, listPendingVersions,
  markVersionSynced, markVersionsSynced, pendingVersionAttachments,
} from './pending-versions.js';
import { writeLocalVersionBinding } from './version-binding.js';

const SOURCE_A = {
  topic_id: 'grp_1',
  source_message_id: 10,
  source_attachment_index: 0,
  source_file_key: 'deck.pptx',
};
const SOURCE_B = { ...SOURCE_A, source_message_id: 11 };
const SOURCE_OTHER_TOPIC = { ...SOURCE_A, topic_id: 'grp_2' };

const LATEST_A = {
  file_key: 'a.pptx', url: '/uploads/files/a.pptx', name: 'deck.pptx',
  size: 10, mime_type: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
};
const LATEST_B = { ...LATEST_A, file_key: 'b.pptx', url: '/uploads/files/b.pptx' };

function entry(name, url, savedAt = 0) {
  return {
    source: { ...SOURCE_A },
    latest: { ...LATEST_A, name, file_key: url.split('/').pop(), url },
    savedAt,
  };
}

describe('pending saved versions', () => {
  beforeEach(() => localStorage.clear());

  it('lists only the versions belonging to the requested conversation', () => {
    writeLocalVersionBinding(SOURCE_A, LATEST_A);
    writeLocalVersionBinding(SOURCE_B, LATEST_B);
    writeLocalVersionBinding(SOURCE_OTHER_TOPIC, LATEST_A);

    const listed = listPendingVersions('grp_1');
    expect(listed).toHaveLength(2);
    expect(listed.map(row => row.latest.url)).toEqual([
      '/uploads/files/a.pptx', '/uploads/files/b.pptx',
    ]);
    expect(listPendingVersions('grp_2')).toHaveLength(1);
  });

  it('returns nothing for a conversation with no saved version', () => {
    expect(listPendingVersions('grp_missing')).toEqual([]);
  });

  it('ignores records that are not valid JSON instead of throwing', () => {
    localStorage.setItem('office.fileVersion.broken', '{not json');
    expect(() => listPendingVersions('grp_1')).not.toThrow();
    expect(listPendingVersions('grp_1')).toEqual([]);
  });

  it('builds composer attachments in the shape the sender reads', () => {
    writeLocalVersionBinding(SOURCE_A, LATEST_A);
    const [attachment] = pendingVersionAttachments('grp_1');
    expect(attachment.type).toBe('file');
    expect(attachment.name).toBe('deck.pptx');
    expect(attachment.content.payload).toMatchObject({
      file_key: 'a.pptx', url: '/uploads/files/a.pptx', name: 'deck.pptx',
    });
  });

  it('skips a file the user already attached by hand', () => {
    writeLocalVersionBinding(SOURCE_A, LATEST_A);
    const existing = [{
      type: 'file', name: 'deck.pptx',
      content: { payload: { url: '/uploads/files/a.pptx' } },
    }];
    expect(pendingVersionAttachments('grp_1', existing)).toEqual([]);
  });

  it('drops the record once the file has been sent', () => {
    writeLocalVersionBinding(SOURCE_A, LATEST_A);
    expect(listPendingVersions('grp_1')).toHaveLength(1);
    expect(markVersionSynced(SOURCE_A)).toBe(true);
    expect(listPendingVersions('grp_1')).toEqual([]);
  });
});

describe('one document, several saves', () => {
  beforeEach(() => localStorage.clear());

  it('keeps only the newest save of the same document', () => {
    const { latest, superseded } = collapsePendingVersions([
      entry('deck.pptx', '/uploads/files/1.pptx', 100),
      entry('deck.pptx', '/uploads/files/2.pptx', 200),
      entry('deck.pptx', '/uploads/files/3.pptx', 300),
    ]);
    expect(latest).toHaveLength(1);
    expect(latest[0].latest.url).toBe('/uploads/files/3.pptx');
    expect(superseded).toHaveLength(2);
  });

  it('treats copy-suffixed names as the same document', () => {
    const { latest } = collapsePendingVersions([
      entry('报告.docx', '/uploads/files/a.docx', 100),
      entry('报告(1).docx', '/uploads/files/b.docx', 200),
      entry('报告 - 副本.docx', '/uploads/files/c.docx', 300),
    ]);
    expect(latest).toHaveLength(1);
    expect(latest[0].latest.url).toBe('/uploads/files/c.docx');
  });

  it('keeps documents with different names apart', () => {
    const { latest } = collapsePendingVersions([
      entry('报告.docx', '/uploads/files/a.docx', 100),
      entry('鸟类清单.docx', '/uploads/files/b.docx', 200),
    ]);
    expect(latest).toHaveLength(2);
  });

  it('does not merge nameless records, which would drop a file', () => {
    const nameless = { latest: { ...LATEST_A, name: '', url: '/uploads/files/x.pptx' } };
    const other = {
      source: { ...SOURCE_A, source_message_id: 99 },
      latest: { ...LATEST_A, name: '', url: '/uploads/files/y.pptx' },
    };
    expect(collapsePendingVersions([nameless, other]).latest).toHaveLength(2);
  });

  it('carries one attachment per document even after repeated saves', () => {
    writeLocalVersionBinding(SOURCE_A, LATEST_A);
    // Distinct source attachment indexes are what a second save on a reopened document
    // looks like: a new immutable attachment, so a new storage key.
    writeLocalVersionBinding({ ...SOURCE_A, source_attachment_index: 1 }, LATEST_B);
    const attachments = pendingVersionAttachments('grp_1');
    expect(attachments).toHaveLength(1);
    expect(attachments[0].content.payload.url).toBe('/uploads/files/b.pptx');
  });

  it('clears the replaced saves so they never come back later', () => {
    writeLocalVersionBinding(SOURCE_A, LATEST_A);
    writeLocalVersionBinding({ ...SOURCE_A, source_attachment_index: 1 }, LATEST_B);
    const { latest, superseded } = listPendingVersionDocuments('grp_1');
    expect(markVersionsSynced([...latest, ...superseded])).toBe(2);
    expect(listPendingVersions('grp_1')).toEqual([]);
  });
});