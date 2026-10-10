// One task draft owns one creation result. Normal send and annotation setup
// take the same lease so they cannot transition the draft simultaneously.
export function createDraftTaskCoordinator(getScope) {
  let activeLease = null;
  let creation = null;
  return {
    acquire() {
      if (activeLease) return null;
      const scope = getScope();
      const lease = {
        isCurrent: () => getScope() === scope,
        release: () => { if (activeLease === lease) activeLease = null; },
      };
      activeLease = lease;
      return lease;
    },
    ensure(create) {
      const scope = getScope();
      if (creation?.scope === scope) return creation.promise;
      const entry = { scope, promise: null };
      entry.promise = Promise.resolve().then(create).catch(error => {
        if (creation === entry) creation = null;
        throw error;
      });
      creation = entry;
      return entry.promise;
    },
    forget() { creation = null; },
  };
}
