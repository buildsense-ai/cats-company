// A conversation's Agent can ask the viewer to open one of its applications:
// the user says "open the topic-research app" and the Agent answers with a
// structured command instead of hoping the client parses prose.
//
// The command is only a request. The server decides whether it is allowed —
// it stamps the Agent identity after checking that the sending bot really
// speaks for this conversation and really owns the application — and the client
// treats anything it cannot verify as absent. Reading a command is therefore
// safe by construction: a forged one either never reaches the client or fails
// here.
//
// No React and no DOM in this file so the rules can be tested on their own.

export const APP_OPEN_METADATA_KEY = 'app_open';
export const APP_OPEN_CONTRACT = 'catsco.app-open.v1';

function isPlainObject(value) {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

// Returns the canonical command, or null when the value is not a well-formed
// command. Unknown contract versions are rejected rather than guessed at.
//
// `agentUid` may be absent. A current server stamps the sending Agent after
// checking ownership, but a server that predates this feature passes the block
// through untouched. Absence is therefore not an error here: the caller falls
// back to the authenticated sender of the message, which is the same authority
// the stamp would have derived from.
export function normalizeAppOpenCommand(value) {
  if (!isPlainObject(value)) return null;
  if (String(value.contract_version ?? '') !== APP_OPEN_CONTRACT) return null;
  const appId = String(value.app_id ?? '').trim();
  if (!appId) return null;
  const rawAgentUid = value.agent_uid;
  if (rawAgentUid === undefined || rawAgentUid === null || rawAgentUid === '') {
    return { agentUid: 0, appId };
  }
  const agentUid = Number(rawAgentUid);
  if (!Number.isFinite(agentUid) || agentUid <= 0) return null;
  return { agentUid, appId };
}

export function appOpenCommandFromMessage(message) {
  const metadata = message?.metadata;
  if (!isPlainObject(metadata)) return null;
  const command = normalizeAppOpenCommand(metadata[APP_OPEN_METADATA_KEY]);
  if (!command) return null;
  if (command.agentUid > 0) return command;
  // No stamped identity: fall back to the authenticated sender of the message.
  // That is the same value the server would have stamped, so trusting it here
  // grants no authority the sender did not already have.
  const senderUid = Number(message?.from_uid ?? message?.from ?? 0);
  if (!Number.isFinite(senderUid) || senderUid <= 0) return null;
  return { agentUid: senderUid, appId: command.appId };
}

// A command only ever applies to the conversation it arrived in, and only for
// that conversation's Agent. A command naming a different Agent is not
// "somebody else's business" — it is a mismatch, and mismatches are dropped.
export function appOpenCommandAppliesToTopic(command, topicAgentUid) {
  if (!command) return false;
  const expected = Number(topicAgentUid ?? 0);
  if (!Number.isFinite(expected) || expected <= 0) return false;
  return command.agentUid === expected;
}

export function appOpenMessageKey(message) {
  if (!message) return '';
  const id = message.id ?? message.seq_id ?? message.seqId;
  if (id !== undefined && id !== null && String(id).trim() !== '') return String(id);
  // Without a server id there is nothing stable to deduplicate on; fall back to
  // the content plus sender so at least exact repeats collapse.
  const from = String(message.from_uid ?? message.from ?? '');
  const content = String(message.content ?? '');
  if (!from && !content) return '';
  return `${from}:${content.length}:${content.slice(0, 32)}`;
}

// Decides whether one incoming message should open an application, and returns
// what to open. Everything that cannot be trusted is answered with null.
export function appOpenCommandForMessage({
  message,
  topic,
  topicAgentUid,
  seenKeys,
} = {}) {
  if (!message) return null;
  const messageTopic = String(message.topic ?? '');
  if (messageTopic && String(topic ?? '') && messageTopic !== String(topic)) return null;
  const command = appOpenCommandFromMessage(message);
  if (!command) return null;
  if (!appOpenCommandAppliesToTopic(command, topicAgentUid)) return null;
  const key = appOpenMessageKey(message);
  if (key && seenKeys?.has?.(key)) return null;
  return { ...command, key };
}

// Finds the application a command names in the Agent's own catalogue. The
// catalogue is the authority on address and title: a command carries an id, not
// a URL, so nothing in the message can point the viewer somewhere else.
export function findAppOpenTarget(apps, appId) {
  const wanted = String(appId ?? '').trim();
  if (!wanted || !Array.isArray(apps)) return null;
  const app = apps.find((item) => String(item?.id ?? item?.artifact_id ?? '').trim() === wanted);
  if (!app) return null;
  const id = String(app.id ?? app.artifact_id ?? '').trim();
  const url = String(app.url ?? '').trim();
  if (!id || !url) return null;
  return { ...app, id, url };
}

// Words that make "the research app" a request rather than a description. A bare
// 用 is deliberately absent: it appears in questions and in refusals ("不用"),
// where nothing should open.
const USE_INTENT = /(打开|启用|调起|启动|调用|用一下|用下|使用|用用|帮我开|给我开|开一下|进入)/;

// A refusal wins over an intent word: "先不用打开" is not a request.
const NEGATED = /(不用|不要|别|不需要|先不|暂时不|不想)/;

// People shorten a name: the application is called 课题调研应用 and the user types
// 打开课题调研. A trailing generic word is therefore not part of the name.
const GENERIC_SUFFIX = /(应用|助手|系统|工具|平台|工作台|小工具)$/;

// True when `needle` occurs in `haystack` as a standalone name.
//
// Chinese offers no spaces, so a substring test alone would read the 分析 inside
// "帮我分析一下" as the application 分析. Latin names are checked at word
// boundaries; a CJK name must be at least three characters long, which is the
// shortest length that is a name rather than a common word.
function hasStandaloneMatch(haystack, needle) {
  if (!needle) return false;
  const isLatin = /^[\x20-\x7e]+$/.test(needle);
  if (isLatin) {
    const escaped = needle.split('').map((char) => (
      '.^$*+?()[]{}|\\'.includes(char) ? `\\${char}` : char
    )).join('');
    const pattern = new RegExp(`(^|[^A-Za-z0-9])${escaped}([^A-Za-z0-9]|$)`, 'i');
    return pattern.test(haystack);
  }
  const candidates = [needle];
  const stripped = needle.replace(GENERIC_SUFFIX, '');
  if (stripped && stripped !== needle) candidates.push(stripped);
  return candidates.some((candidate) => candidate.length >= 3 && haystack.includes(candidate));
}

// Matches what the user typed against the Agent's catalogue, so the panel can
// open the application immediately instead of waiting for a round trip. Only a
// single unambiguous name counts: two applications whose names both appear is a
// sentence about applications, not a request for one of them.
export function applicationMentionedInText(text, apps) {
  const value = String(text ?? '').trim();
  if (!value || !USE_INTENT.test(value) || NEGATED.test(value) || !Array.isArray(apps)) return null;
  const haystack = value.toLowerCase();
  const matches = [];
  for (const app of apps) {
    const id = String(app?.id ?? app?.artifact_id ?? '').trim();
    const url = String(app?.url ?? '').trim();
    const title = String(app?.title ?? app?.name ?? '').trim();
    if (!id || !url || title.length < 2) continue;
    if (!hasStandaloneMatch(haystack, title.toLowerCase())) continue;
    matches.push({ ...app, id, url, title });
  }
  if (matches.length !== 1) return null;
  return matches[0];
}
