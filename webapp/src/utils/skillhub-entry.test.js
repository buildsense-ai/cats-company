import {
  compareSkillHubVersions,
  formatSkillHubVersion,
  isPrivateSkillHubReference,
  normalizeSkillHubSkills,
  resolveSkillHubUpdateStatus,
} from './skillhub-entry';

const hash = (seed) => seed.repeat(64);
const installedReference = (version, contentHash, skillId = 'tools/summarize') => ({
  source: 'skillhub', skillId, version, contentHash,
});
const catalogueSkill = (latestVersion, contentHash) => ({ latestVersion, contentHash });

describe('skillhub entry versions', () => {
  it('formats a version label with one lowercase v prefix', () => {
    expect(formatSkillHubVersion('1.0.6')).toBe('v1.0.6');
    expect(formatSkillHubVersion('v1.0.6')).toBe('v1.0.6');
    expect(formatSkillHubVersion('V1.0.6')).toBe('v1.0.6');
    expect(formatSkillHubVersion('')).toBe('');
  });

  it('ranks versions by their leading numeric fields only', () => {
    expect(compareSkillHubVersions('1.10.0', '1.9.0')).toBe(1);
    expect(compareSkillHubVersions('v1.0.0', '1.0')).toBe(0);
    expect(compareSkillHubVersions('1.0.0-beta', '1.0.0')).toBe(0);
    expect(compareSkillHubVersions('2026.09.16', '2026.09.15')).toBe(1);
    expect(compareSkillHubVersions('latest', '1.0.0')).toBe(null);
  });

  it('offers an update only when the catalogue copy is newer', () => {
    expect(resolveSkillHubUpdateStatus(null, catalogueSkill('1.0.0', hash('b')))).toBe('add');
    expect(resolveSkillHubUpdateStatus(
      installedReference('1.0.5', hash('a')), catalogueSkill('1.0.6', hash('b')),
    )).toBe('update');
    expect(resolveSkillHubUpdateStatus(
      installedReference('1.0.5', hash('a')), catalogueSkill('1.0.5', hash('a')),
    )).toBe('current');
    // Republishing an existing version replaces the payload, so it is an update.
    expect(resolveSkillHubUpdateStatus(
      installedReference('1.0.5', hash('a')), catalogueSkill('1.0.5', hash('b')),
    )).toBe('update');
    // An installed copy that is ahead of the catalogue is never downgraded.
    expect(resolveSkillHubUpdateStatus(
      installedReference('2.0.0', hash('a')), catalogueSkill('1.0.0', hash('b')),
    )).toBe('current');
    // No published version, or no catalogue metadata, keeps the card as-is.
    expect(resolveSkillHubUpdateStatus(
      installedReference('1.0.5', hash('a')), catalogueSkill('', hash('b')),
    )).toBe('unknown');
    expect(resolveSkillHubUpdateStatus(installedReference('1.0.5', hash('a')), null)).toBe('unknown');
  });

  it('never offers an update that would rewrite a Bot-private capability', () => {
    expect(isPrivateSkillHubReference('priv_0123456789abcdef')).toBe(true);
    expect(isPrivateSkillHubReference('private/alice/demo')).toBe(true);
    expect(isPrivateSkillHubReference('alice/local-demo')).toBe(false);
    // Private references carry `source: 'skillhub'` too, so the skillId prefix is the gate.
    expect(resolveSkillHubUpdateStatus(
      installedReference('1.0.5', hash('a'), 'priv_0123456789abcdef'),
      catalogueSkill('1.0.6', hash('b')),
    )).toBe('current');
    expect(resolveSkillHubUpdateStatus(
      installedReference('1.0.5', hash('a'), 'tools/summarize'),
      catalogueSkill('1.0.6', hash('b')),
    )).toBe('update');
  });

  it('never offers an update for a capability that is not a catalogue reference', () => {
    expect(resolveSkillHubUpdateStatus(
      { source: 'local', skillId: 'local:demo', version: '1.0.5', contentHash: hash('a') },
      catalogueSkill('1.0.6', hash('b')),
    )).toBe('current');
  });

  it('reads the hash of a catalogue entry as the list route returns it', () => {
    const [entry] = normalizeSkillHubSkills([{
      id: 'tools/summarize',
      name: 'Summarize',
      latest_version: '1.0.6',
      content_hash: hash('b'),
    }]);
    expect(entry).toMatchObject({ latestVersion: '1.0.6', contentHash: hash('b') });
    expect(resolveSkillHubUpdateStatus(installedReference('1.0.5', hash('a')), entry)).toBe('update');
  });
});
