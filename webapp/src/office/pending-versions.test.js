import { beforeEach, describe, expect, it } from 'vitest';
import {
  listPendingVersions, markVersionSynced, pendingVersionAttachments,
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

describe('pending saved versions', () => {
  beforeEach(() => localStorage.clear());

  it('lists only the versions belonging to the requested conversation', () => {
    writeLocalVersionBinding(SOURCE_A, LATEST_A);
    writeLocalVersionBinding(SOURCE_B, LATEST_B);
    writeLocalVersionBinding(SOURCE_OTHER_TOPIC, LATEST_A);

    const listed = listPendingVersions('grp_1');
    expect(listed).toHaveLength(2);
    expect(listed.map(entry => entry.latest.url)).toEqual(['/uploads/files/a.pptx', '/uploads/files/b.pptx']);
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
    const existing = [{ type: 'file', name: 'deck.pptx', content: { payload: { url: '/uploads/files/a.pptx' } } }];
    expect(pendingVersionAttachments('grp_1', existing)).toEqual([]);
  });

  it('drops the record once the file has been sent', () => {
    writeLocalVersionBinding(SOURCE_A, LATEST_A);
    expect(listPendingVersions('grp_1')).toHaveLength(1);
    expect(markVersionSynced(SOURCE_A)).toBe(true);
    expect(listPendingVersions('grp_1')).toEqual([]);
  });
});