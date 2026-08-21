const PRIVATE_CONVERSATION_SHARE_PATH_PREFIXES = [
  '/uploads/files/',
  '/uploads/images/',
  '/uploads/feedback/',
  '/api/shared-conversations/',
  '/share/',
];
const CONVERSATION_SHARE_URL_CANDIDATE_PATTERN = /(?:https?:\/\/[^\s<>"'(),，。！？；：\p{Script=Han}]+|(?:\/|%2f)[^\s<>"'(),，。！？；：\p{Script=Han}]+)/giu;
const MAX_CONVERSATION_SHARE_URL_DECODE_DEPTH = 2;

function decodeURLComponent(value) {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

function privateConversationSharePath(value) {
  let decoded = String(value || '');
  for (let attempt = 0; attempt < 2; attempt += 1) {
    const unescaped = decodeURLComponent(decoded);
    if (unescaped === decoded) break;
    decoded = unescaped;
  }
  decoded = decoded.replaceAll('\\', '/');
  const segments = [];
  decoded.split('/').forEach((segment) => {
    if (!segment || segment === '.') return;
    if (segment === '..') {
      segments.pop();
      return;
    }
    segments.push(segment);
  });
  const normalized = `/${segments.join('/')}`;
  return PRIVATE_CONVERSATION_SHARE_PATH_PREFIXES.some((prefix) => normalized.startsWith(prefix));
}

function privateConversationShareURLCandidate(value, depth = 0) {
  const candidate = String(value || '').trim();
  if (!candidate || depth > MAX_CONVERSATION_SHARE_URL_DECODE_DEPTH) return false;

  let parsed;
  try {
    parsed = new URL(candidate, 'https://conversation-share.invalid');
  } catch {
    const decoded = decodeURLComponent(candidate);
    return decoded !== candidate && privateConversationShareURLCandidate(decoded, depth + 1);
  }

  if (privateConversationSharePath(parsed.pathname)) return true;
  for (const nested of parsed.searchParams.values()) {
    if (privateConversationShareURLCandidate(nested, depth + 1)) return true;
  }
  return privateConversationShareURLCandidate(parsed.hash.replace(/^#/, ''), depth + 1);
}

export function sanitizeConversationShareText(value) {
  return String(value || '').replace(CONVERSATION_SHARE_URL_CANDIDATE_PATTERN, (candidate) => (
    privateConversationShareURLCandidate(candidate) ? '' : candidate
  )).trim();
}
