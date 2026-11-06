import { describe, expect, it, vi } from 'vitest';
import {
  emptyArtifactPanelEntry,
  forgetArtifactPanel,
  isArtifactPanelMounted,
  loadArtifactPanelStore,
  mountedArtifactTopics,
  normalizeArtifactPanelEntry,
  readArtifactPanel,
  rememberArtifactPanel,
  saveArtifactPanelStore,
} from './artifact-panel-state';

const APP = { id: 'board', url: 'https://artifact.catsco.cc/board/', title: '课题调研', agent_uid: 9 };

function memoryStorage(initial = {}) {
  const data = new Map(Object.entries(initial));
  return {
    getItem: (key) => (data.has(key) ? data.get(key) : null),
    setItem: (key, value) => data.set(key, String(value)),
    removeItem: (key) => data.delete(key),
    dump: () => Object.fromEntries(data),
  };
}

function openEntry(overrides = {}) {
  return { open: true, agentUid: 9, app: APP, tab: 'gateway', returnOpen: false, ...overrides };
}

describe('artifact panel entry normalization', () => {
  it('drops an app that has no usable address', () => {
    const entry = normalizeArtifactPanelEntry({ open: true, app: { id: 'board' } });
    expect(entry.app).toBeNull();
    expect(entry.returnOpen).toBe(false);
  });

  it('keeps only the fields the panel needs', () => {
    const entry = normalizeArtifactPanelEntry({
      open: true,
      agentUid: '9',
      tab: 'gateway',
      app: { ...APP, injected: 'nope', onClick: () => {} },
    });
    expect(entry.app).toEqual({ id: 'board', url: APP.url, title: '课题调研', agent_uid: 9 });
    expect(entry.agentUid).toBe(9);
  });

  it('falls back to a closed entry for junk input', () => {
    expect(normalizeArtifactPanelEntry(null)).toEqual(emptyArtifactPanelEntry());
    expect(normalizeArtifactPanelEntry({ tab: 'bogus' }).tab).toBe('files');
  });
});

describe('remembering panel state per conversation', () => {
  it('keeps each conversation independent', () => {
    let store = rememberArtifactPanel({ topics: {}, order: [] }, 'p2p_7_9', openEntry());
    store = rememberArtifactPanel(store, 'p2p_7_10', openEntry({ agentUid: 10 }));

    expect(readArtifactPanel(store, 'p2p_7_9').agentUid).toBe(9);
    expect(readArtifactPanel(store, 'p2p_7_10').agentUid).toBe(10);
  });

  it('returns a closed entry for a conversation that never opened one', () => {
    const store = rememberArtifactPanel({ topics: {}, order: [] }, 'p2p_7_9', openEntry());
    expect(readArtifactPanel(store, 'p2p_7_11')).toEqual(emptyArtifactPanelEntry());
  });

  it('records a close instead of deleting the conversation when the panel closes', () => {
    let store = rememberArtifactPanel({ topics: {}, order: [] }, 'p2p_7_9', openEntry());
    store = rememberArtifactPanel(store, 'p2p_7_9', { ...openEntry(), open: false });

    expect(readArtifactPanel(store, 'p2p_7_9').open).toBe(false);
  });

  it('moves a re-remembered conversation to the front', () => {
    let store = rememberArtifactPanel({ topics: {}, order: [] }, 'a', openEntry());
    store = rememberArtifactPanel(store, 'b', openEntry());
    store = rememberArtifactPanel(store, 'a', openEntry());

    expect(store.order).toEqual(['a', 'b']);
  });

  it('drops the least recently used conversation beyond the cap', () => {
    let store = { topics: {}, order: [] };
    for (const topic of ['a', 'b', 'c']) store = rememberArtifactPanel(store, topic, openEntry(), 3);
    store = rememberArtifactPanel(store, 'd', openEntry(), 3);

    expect(store.order).toEqual(['d', 'c', 'b']);
    expect(store.topics.a).toBeUndefined();
  });
});

describe('forgetting a panel', () => {
  it('removes the conversation so it does not come back on the next visit', () => {
    let store = rememberArtifactPanel({ topics: {}, order: [] }, 'p2p_7_9', openEntry());
    store = forgetArtifactPanel(store, 'p2p_7_9');

    expect(store.topics['p2p_7_9']).toBeUndefined();
    expect(readArtifactPanel(store, 'p2p_7_9').open).toBe(false);
    expect(store.order).toEqual([]);
  });

  it('is a no-op for an unknown conversation', () => {
    const store = rememberArtifactPanel({ topics: {}, order: [] }, 'a', openEntry());
    expect(forgetArtifactPanel(store, 'zzz')).toBe(store);
  });
});

describe('mounted panel selection', () => {
  it('keeps the conversation on screen mounted', () => {
    let store = { topics: {}, order: [] };
    store = rememberArtifactPanel(store, 'a', openEntry());
    store = rememberArtifactPanel(store, 'b', openEntry());

    expect(mountedArtifactTopics(store, 'b', 1)).toEqual(['b']);
    expect(mountedArtifactTopics(store, 'a', 1)).toEqual(['a']);
  });

  it('mounts applications ahead of list-only panels under the ceiling', () => {
    const listOnly = { open: true, agentUid: 9, app: null, tab: 'files', returnOpen: false };
    let store = rememberArtifactPanel({ topics: {}, order: [] }, 'app-a', openEntry());
    store = rememberArtifactPanel(store, 'list-1', listOnly);
    store = rememberArtifactPanel(store, 'list-2', listOnly);
    store = rememberArtifactPanel(store, 'list-3', listOnly);

    // A list costs nothing to rebuild; an application loses its process.
    expect(mountedArtifactTopics(store, 'list-3', 2)).toEqual(['list-3', 'app-a']);
  });

  it('reports whether a conversation currently holds a mounted panel', () => {
    let store = rememberArtifactPanel({ topics: {}, order: [] }, 'a', openEntry());
    store = rememberArtifactPanel(store, 'b', openEntry());

    expect(isArtifactPanelMounted(store, 'a', 'a', 2)).toBe(true);
    expect(isArtifactPanelMounted(store, 'b', 'a', 2)).toBe(true);
    expect(isArtifactPanelMounted(store, 'b', 'a', 1)).toBe(false);
  });

  it('keeps every conversation record, mounted or not', () => {
    // Four conversations, four different applications: each record must survive
    // untouched, so returning to one restores its own application and never a
    // neighbour's.
    let store = { topics: {}, order: [] };
    for (const [topic, appId] of [['c1', 'app1'], ['c2', 'app2'], ['c3', 'app3'], ['c4', 'app4']]) {
      store = rememberArtifactPanel(store, topic, {
        ...openEntry(),
        app: { id: appId, url: `https://artifact.catsco.cc/${appId}/`, title: appId, agent_uid: 9 },
      });
    }

    expect(mountedArtifactTopics(store, 'c3')).toContain('c3');
    expect(readArtifactPanel(store, 'c1').app.id).toBe('app1');
    expect(readArtifactPanel(store, 'c2').app.id).toBe('app2');
    expect(readArtifactPanel(store, 'c3').app.id).toBe('app3');
    expect(readArtifactPanel(store, 'c4').app.id).toBe('app4');
  });
});

describe('persistence across a reload', () => {
  it('restores what was open in a conversation', () => {
    const storage = memoryStorage();
    let store = rememberArtifactPanel({ topics: {}, order: [] }, 'p2p_7_9', openEntry());
    saveArtifactPanelStore(storage, 7, store);

    const reloaded = loadArtifactPanelStore(storage, 7);
    expect(readArtifactPanel(reloaded, 'p2p_7_9')).toMatchObject({ open: true, agentUid: 9, tab: 'gateway' });
    expect(readArtifactPanel(reloaded, 'p2p_7_9').app).toEqual({
      id: 'board', url: APP.url, title: '课题调研', agent_uid: 9,
    });
  });

  it('keeps each account separate', () => {
    const storage = memoryStorage();
    saveArtifactPanelStore(storage, 7, rememberArtifactPanel({ topics: {}, order: [] }, 'a', openEntry()));

    expect(loadArtifactPanelStore(storage, 8).order).toEqual([]);
    expect(loadArtifactPanelStore(storage, 7).order).toEqual(['a']);
  });

  it('clears the stored key once nothing is left', () => {
    const storage = memoryStorage();
    let store = rememberArtifactPanel({ topics: {}, order: [] }, 'a', openEntry());
    saveArtifactPanelStore(storage, 7, store);
    store = forgetArtifactPanel(store, 'a');
    saveArtifactPanelStore(storage, 7, store);

    expect(Object.keys(storage.dump())).toEqual([]);
  });

  it('survives a corrupted stored value', () => {
    const storage = memoryStorage({ 'catsco:artifact-panel:v1:7': '{not json' });
    expect(loadArtifactPanelStore(storage, 7).order).toEqual([]);
  });

  it('survives a storage that throws', () => {
    const thrown = {
      getItem: () => { throw new Error('denied'); },
      setItem: () => { throw new Error('denied'); },
      removeItem: () => { throw new Error('denied'); },
    };
    expect(loadArtifactPanelStore(thrown, 7).order).toEqual([]);
    expect(() => saveArtifactPanelStore(thrown, 7, rememberArtifactPanel({ topics: {}, order: [] }, 'a', openEntry()))).not.toThrow();
  });

  it('does not write when there is nothing to keep', () => {
    const storage = memoryStorage();
    const setItem = vi.spyOn(storage, 'setItem');
    saveArtifactPanelStore(storage, 7, { topics: {}, order: [] });
    expect(setItem).not.toHaveBeenCalled();
  });
});
