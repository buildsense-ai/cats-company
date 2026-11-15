// Undo/redo for the office editor.
//
// The stack stores whole edit states rather than inverse operations. An office edit is a
// small object (a box, a paragraph list, a style patch), so snapshotting is cheaper than
// writing and testing an inverse for every operation type, and it cannot drift out of
// sync with the writer the way hand-written inverses do.
//
// Capped at a fixed depth: a session that drags a shape for a minute should not hold
// thousands of snapshots in memory.

export const UNDO_LIMIT = 60;

export function createHistory(initial) {
  return { past: [], present: initial, future: [] };
}

/**
 * Push a new state.
 *
 * A push clears the redo branch. That is the standard behaviour and also the honest one:
 * once the user edits after undoing, the undone states are no longer reachable.
 */
export function pushHistory(history, next) {
  const past = [...history.past, history.present];
  return {
    past: past.length > UNDO_LIMIT ? past.slice(past.length - UNDO_LIMIT) : past,
    present: next,
    future: [],
  };
}

export function canUndo(history) {
  return Boolean(history && history.past.length > 0);
}

export function canRedo(history) {
  return Boolean(history && history.future.length > 0);
}

export function undoHistory(history) {
  if (!canUndo(history)) return history;
  const past = history.past.slice(0, -1);
  const present = history.past[history.past.length - 1];
  return { past, present, future: [history.present, ...history.future] };
}

export function redoHistory(history) {
  if (!canRedo(history)) return history;
  const [present, ...future] = history.future;
  return { past: [...history.past, history.present], present, future };
}

/** Replace the present without recording history, for changes that are not user edits. */
export function resetHistory(history, next) {
  return { ...history, present: next };
}
