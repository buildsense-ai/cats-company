// Per-conversation state for the right-hand cloud-artifacts (application)
// panel.
//
// The panel used to be a single global thing: switching topic cleared it, so an
// application opened in conversation A vanished as soon as the user looked at
// conversation B. The state now belongs to the conversation that opened it, and
// the last set of conversations is remembered across a page reload.
//
// Kept free of React and of the DOM so the rules can be unit tested on their
// own; messages-view.jsx only wires them to components.

export const ARTIFACT_PANEL_STORAGE_PREFIX = 'catsco:artifact-panel:v1:';

// How many conversations keep a live panel.
//
// A live panel is what keeps an application's process running: the frame holds
// the session and its work while the user is elsewhere, and coming back is
// instant because nothing is rebuilt. Each panel is a real iframe, so the
// ceiling is a memory budget rather than a UI preference — eight covers the
// conversations someone actually works across, and panels holding a running
// application are always chosen before panels holding only a list.
export const MAX_MOUNTED_ARTIFACT_PANELS = 8;

// How many conversations are persisted for a reload. Much larger than the
// mounted ceiling on purpose: a record is a few hundred bytes, while a live
// panel is a browser tab's worth of memory.
export const MAX_PERSISTED_ARTIFACT_TOPICS = 24;

const PANEL_TABS = ['active', 'deleted', 'files', 'gateway'];

export function artifactPanelStorageKey(uid) {
  return `${ARTIFACT_PANEL_STORAGE_PREFIX}${String(uid ?? '')}`;
}

export function emptyArtifactPanelEntry() {
  return { open: false, agentUid: 0, app: null, tab: 'files', returnOpen: false };
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

// Only the fields the panel needs to re-open itself survive a round trip. An
// application record comes from the server, so it is copied field by field
// rather than trusted wholesale — a persisted blob must never be able to inject
// arbitrary props into the panel.
function normalizeArtifactPanelApp(value) {
  if (!isPlainObject(value)) return null;
  const id = String(value.id ?? value.artifact_id ?? '').trim();
  const url = String(value.url ?? '').trim();
  if (!id || !url) return null;
  const app = {
    id,
    url,
    title: String(value.title ?? '').trim(),
    agent_uid: Number(value.agent_uid ?? value.agentUid ?? 0) || 0,
  };
  return app;
}

export function normalizeArtifactPanelEntry(value) {
  if (!isPlainObject(value)) return emptyArtifactPanelEntry();
  const tab = PANEL_TABS.includes(value.tab) ? value.tab : 'files';
  const app = normalizeArtifactPanelApp(value.app);
  return {
    open: value.open === true,
    agentUid: Math.max(0, Number(value.agentUid ?? value.agent_uid ?? 0) || 0),
    app,
    tab,
    // The "back to the app list" flag only means something while an
    // application is actually open.
    returnOpen: value.returnOpen === true && Boolean(app),
  };
}

export function emptyArtifactPanelStore() {
  return { topics: {}, order: [] };
}

function normalizeOrder(order, topics) {
  const seen = new Set();
  const next = [];
  for (const raw of Array.isArray(order) ? order : []) {
    const topicId = String(raw ?? '');
    if (!topicId || seen.has(topicId) || !topics[topicId]) continue;
    seen.add(topicId);
    next.push(topicId);
  }
  // Anything present in topics but missing from order is appended, so a
  // hand-edited or partially written store still behaves predictably.
  for (const topicId of Object.keys(topics)) {
    if (!seen.has(topicId)) next.push(topicId);
  }
  return next;
}

export function loadArtifactPanelStore(storage, uid) {
  const store = emptyArtifactPanelStore();
  if (!storage || typeof storage.getItem !== 'function') return store;
  let raw = null;
  try {
    raw = storage.getItem(artifactPanelStorageKey(uid));
  } catch {
    // Private mode, disabled storage, quota errors: treat as "nothing saved"
    // rather than breaking the conversation view.
    return store;
  }
  if (!raw) return store;
  let parsed = null;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return store;
  }
  if (!isPlainObject(parsed) || !isPlainObject(parsed.topics)) return store;
  for (const [topicId, entry] of Object.entries(parsed.topics)) {
    const key = String(topicId ?? '');
    if (!key) continue;
    store.topics[key] = normalizeArtifactPanelEntry(entry);
  }
  store.order = normalizeOrder(parsed.order, store.topics);
  return store;
}

export function saveArtifactPanelStore(storage, uid, store) {
  if (!storage || typeof storage.setItem !== 'function') return;
  const topics = isPlainObject(store?.topics) ? store.topics : {};
  const order = normalizeOrder(store?.order, topics);
  try {
    if (!order.length) {
      storage.removeItem?.(artifactPanelStorageKey(uid));
      return;
    }
    storage.setItem(artifactPanelStorageKey(uid), JSON.stringify({ topics, order }));
  } catch {
    // Persisting is a convenience; a full quota must not surface as an error
    // in the middle of the user's work.
  }
}

// Reads the stored entry for one conversation. Always returns an entry, so
// callers do not have to null-check the common "never opened here" case.
export function readArtifactPanel(store, topicId) {
  const key = String(topicId ?? '');
  if (!key || !isPlainObject(store?.topics)) return emptyArtifactPanelEntry();
  const entry = store.topics[key];
  return entry ? normalizeArtifactPanelEntry(entry) : emptyArtifactPanelEntry();
}

function trimStore(store, maxTopics) {
  const limit = Math.max(1, Number(maxTopics) || MAX_PERSISTED_ARTIFACT_TOPICS);
  const topics = { ...store.topics };
  const order = normalizeOrder(store.order, topics).slice(0, limit);
  const kept = {};
  for (const topicId of order) kept[topicId] = topics[topicId];
  return { topics: kept, order };
}

// Records the panel state for a conversation and marks it most recently used.
export function rememberArtifactPanel(store, topicId, entry, maxTopics = MAX_PERSISTED_ARTIFACT_TOPICS) {
  const key = String(topicId ?? '');
  if (!key) return store;
  const base = isPlainObject(store?.topics) ? store : emptyArtifactPanelStore();
  const topics = { ...base.topics, [key]: normalizeArtifactPanelEntry(entry) };
  const order = [key, ...normalizeOrder(base.order, topics).filter((item) => item !== key)];
  return trimStore({ topics, order }, maxTopics);
}

// Dropping an entry is an explicit user intent ("close this panel"), so the
// conversation must not silently re-open the application on the next visit.
export function forgetArtifactPanel(store, topicId) {
  const key = String(topicId ?? '');
  if (!key || !isPlainObject(store?.topics) || !store.topics[key]) return store;
  const topics = { ...store.topics };
  delete topics[key];
  return { topics, order: normalizeOrder(store.order, topics).filter((item) => item !== key) };
}

// Conversations whose panel is live right now.
//
// The conversation on screen always takes a slot. The rest go to panels holding
// an application before panels holding only a list: a list costs nothing to
// rebuild, while unmounting an application stops whatever it was running — which
// is the whole point of keeping a panel mounted. Anything beyond the ceiling is
// still remembered and is restored when the user opens that conversation.
export function mountedArtifactTopics(store, activeTopicId, maxMounted = MAX_MOUNTED_ARTIFACT_PANELS) {
  const limit = Math.max(1, Number(maxMounted) || MAX_MOUNTED_ARTIFACT_PANELS);
  const active = String(activeTopicId ?? '');
  const openTopics = normalizeOrder(store?.order, store?.topics)
    .filter((topicId) => store.topics[topicId]?.open === true);
  const rank = (topicId) => {
    if (topicId === active) return 0;
    return store.topics[topicId]?.app?.id ? 1 : 2;
  };
  return openTopics
    .map((topicId, index) => ({ topicId, index }))
    .sort((left, right) => (rank(left.topicId) - rank(right.topicId)) || (left.index - right.index))
    .slice(0, limit)
    .map((item) => item.topicId);
}

// Entries that are open but no longer mounted are still remembered; they come
// back the next time the conversation is opened.
export function isArtifactPanelMounted(store, topicId, activeTopicId, maxMounted = MAX_MOUNTED_ARTIFACT_PANELS) {
  return mountedArtifactTopics(store, activeTopicId, maxMounted).includes(String(topicId ?? ''));
}
