// Store one record per image so two tabs can save versions independently.
async function withPortraits(mode, operation) {
  const db = await new Promise((resolve, reject) => {
    const request = indexedDB.open('catsco-persona-portraits', 1);
    request.onupgradeneeded = () => {
      const portraits = request.result.createObjectStore('portraits', { keyPath: ['scope', 'id'] });
      portraits.createIndex('scope', 'scope');
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  try {
    return await new Promise((resolve, reject) => {
      const tx = db.transaction('portraits', mode);
      const request = operation(tx.objectStore('portraits'));
      tx.oncomplete = () => resolve(request.result);
      tx.onabort = () => reject(tx.error || new Error('形象保存失败'));
    });
  } finally {
    db.close();
  }
}

export async function readPortraitHistory(scope) {
  const rows = await withPortraits('readonly', store => store.index('scope').getAll(scope));
  return rows.sort((a, b) => b.created - a.created);
}

export function storePortrait(scope, portrait) {
  return withPortraits('readwrite', store => store.put({ ...portrait, scope }));
}
