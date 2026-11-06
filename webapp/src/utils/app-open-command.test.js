import { describe, expect, it } from 'vitest';
import {
  APP_OPEN_CONTRACT,
  appOpenCommandAppliesToTopic,
  appOpenCommandForMessage,
  appOpenCommandFromMessage,
  findAppOpenTarget,
  normalizeAppOpenCommand,
} from './app-open-command';

const APP = { id: 'research', url: 'https://artifact.catsco.cc/research/', title: '课题调研' };

function command(overrides = {}) {
  return { contract_version: APP_OPEN_CONTRACT, app_id: 'research', agent_uid: 9, ...overrides };
}

function message(overrides = {}) {
  return { id: 501, from_uid: 9, topic: 'p2p_7_9', content: '已打开', metadata: { app_open: command() }, ...overrides };
}

describe('command normalization', () => {
  it('accepts a well-formed command', () => {
    expect(normalizeAppOpenCommand(command())).toEqual({ agentUid: 9, appId: 'research' });
  });

  it('rejects an unknown contract version instead of guessing', () => {
    expect(normalizeAppOpenCommand(command({ contract_version: 'catsco.app-open.v2' }))).toBeNull();
    expect(normalizeAppOpenCommand(command({ contract_version: undefined }))).toBeNull();
  });

  it('rejects a command without an app id', () => {
    expect(normalizeAppOpenCommand(command({ app_id: '  ' }))).toBeNull();
    expect(normalizeAppOpenCommand(command({ app_id: undefined }))).toBeNull();
    // An identity that is present but unusable is a malformed command.
    expect(normalizeAppOpenCommand(command({ agent_uid: 0 }))).toBeNull();
    expect(normalizeAppOpenCommand(command({ agent_uid: 'nine' }))).toBeNull();
  });

  it('accepts a command whose sender could not stamp an identity', () => {
    // A server that predates this feature forwards the block untouched, so the
    // identity is simply absent rather than wrong.
    expect(normalizeAppOpenCommand(command({ agent_uid: undefined }))).toEqual({ agentUid: 0, appId: 'research' });
    expect(normalizeAppOpenCommand(command({ agent_uid: null }))).toEqual({ agentUid: 0, appId: 'research' });
  });

  it('falls back to the message sender when nothing stamped the identity', () => {
    const unstamped = message({
      metadata: { app_open: command({ agent_uid: undefined }) },
    });
    expect(appOpenCommandFromMessage(unstamped)).toEqual({ agentUid: 9, appId: 'research' });
  });

  it('refuses an unstamped command whose sender is unknown', () => {
    const anonymous = message({
      from_uid: 0,
      metadata: { app_open: command({ agent_uid: undefined }) },
    });
    expect(appOpenCommandFromMessage(anonymous)).toBeNull();
  });

  it('rejects non-objects, including arrays', () => {
    expect(normalizeAppOpenCommand(null)).toBeNull();
    expect(normalizeAppOpenCommand('open research')).toBeNull();
    expect(normalizeAppOpenCommand([command()])).toBeNull();
  });

  it('does not carry a URL from the message', () => {
    const normalized = normalizeAppOpenCommand(command({ url: 'https://evil.example/' }));
    expect(normalized).toEqual({ agentUid: 9, appId: 'research' });
  });
});

describe('reading a command off a message', () => {
  it('reads the metadata block', () => {
    expect(appOpenCommandFromMessage(message())).toEqual({ agentUid: 9, appId: 'research' });
  });

  it('ignores messages without metadata', () => {
    expect(appOpenCommandFromMessage(message({ metadata: undefined }))).toBeNull();
    expect(appOpenCommandFromMessage({ id: 1 })).toBeNull();
  });

  it('ignores an unrelated metadata block', () => {
    expect(appOpenCommandFromMessage(message({ metadata: { artifact_context_ref: 'x' } }))).toBeNull();
  });
});

describe('agent ownership', () => {
  it('matches the conversation Agent', () => {
    expect(appOpenCommandAppliesToTopic({ agentUid: 9, appId: 'research' }, 9)).toBe(true);
  });

  it('rejects a command that names another Agent', () => {
    expect(appOpenCommandAppliesToTopic({ agentUid: 9, appId: 'research' }, 10)).toBe(false);
  });

  it('rejects when the conversation has no resolvable Agent', () => {
    expect(appOpenCommandAppliesToTopic({ agentUid: 9, appId: 'research' }, 0)).toBe(false);
    expect(appOpenCommandAppliesToTopic({ agentUid: 9, appId: 'research' }, null)).toBe(false);
  });
});

describe('deciding whether a message opens an application', () => {
  it('opens for a valid command in the matching conversation', () => {
    const result = appOpenCommandForMessage({ message: message(), topic: 'p2p_7_9', topicAgentUid: 9 });
    expect(result).toMatchObject({ agentUid: 9, appId: 'research', key: '501' });
  });

  it('ignores a command addressed to a different conversation', () => {
    expect(appOpenCommandForMessage({ message: message(), topic: 'p2p_7_10', topicAgentUid: 9 })).toBeNull();
  });

  it('ignores a command from a different Agent than the conversation owner', () => {
    expect(appOpenCommandForMessage({ message: message(), topic: 'p2p_7_9', topicAgentUid: 10 })).toBeNull();
  });

  it('opens once per message', () => {
    const seen = new Set();
    const first = appOpenCommandForMessage({ message: message(), topic: 'p2p_7_9', topicAgentUid: 9, seenKeys: seen });
    expect(first).not.toBeNull();
    seen.add(first.key);
    // History replay and a live fanout of the same message must not reopen it.
    expect(appOpenCommandForMessage({ message: message(), topic: 'p2p_7_9', topicAgentUid: 9, seenKeys: seen })).toBeNull();
  });

  it('keeps a stable key across a replay that changes nothing', () => {
    const a = appOpenCommandForMessage({ message: message(), topic: 'p2p_7_9', topicAgentUid: 9 });
    const b = appOpenCommandForMessage({ message: message(), topic: 'p2p_7_9', topicAgentUid: 9 });
    expect(a.key).toBe(b.key);
  });

  it('does not open on prose that merely mentions an application', () => {
    const prose = message({ content: '打开课题调研应用', metadata: undefined });
    expect(appOpenCommandForMessage({ message: prose, topic: 'p2p_7_9', topicAgentUid: 9 })).toBeNull();
  });

  it('does not open on a negated sentence, because there is no command at all', () => {
    const negated = message({ content: '先别打开课题调研应用', metadata: {} });
    expect(appOpenCommandForMessage({ message: negated, topic: 'p2p_7_9', topicAgentUid: 9 })).toBeNull();
  });
});

describe('resolving the application to open', () => {
  it('finds the application in the Agent catalogue', () => {
    expect(findAppOpenTarget([APP], 'research')).toMatchObject({ id: 'research', url: APP.url });
  });

  it('refuses an application the Agent does not publish', () => {
    expect(findAppOpenTarget([APP], 'somebody-elses-app')).toBeNull();
    expect(findAppOpenTarget([], 'research')).toBeNull();
    expect(findAppOpenTarget(null, 'research')).toBeNull();
  });

  it('refuses an entry without a usable address', () => {
    expect(findAppOpenTarget([{ id: 'research' }], 'research')).toBeNull();
    expect(findAppOpenTarget([{ id: 'research', url: '  ' }], 'research')).toBeNull();
  });

  it('does not let the message URL override the catalogue address', () => {
    const target = findAppOpenTarget([APP], 'research');
    expect(target.url).toBe(APP.url);
  });
});
