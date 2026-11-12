// A deterministic force layout for the Daily Notes graph. Notes that link to
// each other, or cite the same session, settle into one cluster; the session
// sources become the hubs between them.
//
// The simulation runs in slices (advance) that pause inside a step, so laying
// out a large graph never holds the main thread for long. Slicing does not
// change the result.

const CELL = 140;
const FRESH_STEPS = 320;
const SEEDED_STEPS = 120;
// A step costs about one pass over each node's near neighbours, so graphs
// above BUDGET_NODES get proportionally fewer steps, but never under MIN_STEPS.
const BUDGET_NODES = 1500;
const MIN_STEPS = 40;
// Neighbouring grid cells, each unordered pair once.
const FORWARD = [[1, -1], [1, 0], [1, 1], [0, 1]];
// Pair checks between pauses; a few hundred microseconds of work.
const PAUSE_PAIRS = 20000;

function hash(value) {
  let h = 2166136261;
  for (let i = 0; i < value.length; i += 1) {
    h ^= value.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return ((h >>> 0) % 10000) / 10000;
}

const cellKey = (cx, cy) => (cx + 50000) * 100000 + (cy + 50000);

function repel(a, b, alpha) {
  let dx = b.x - a.x;
  let dy = b.y - a.y;
  let d2 = dx * dx + dy * dy;
  if (d2 > CELL * CELL) return;
  if (d2 < 1) { dx = hash(`${a.id}${b.id}`) - 0.5; dy = 0.5; d2 = 1; }
  const force = (900 * (0.3 + alpha)) / d2;
  const d = Math.sqrt(d2);
  const fx = (dx / d) * force;
  const fy = (dy / d) * force;
  a.vx -= fx; a.vy -= fy; b.vx += fx; b.vy += fy;
}

export function createDailyNotesLayout(model, previous = new Map()) {
  const nodes = [];
  const index = new Map();
  const add = (node) => {
    const seed = previous.get(node.id);
    // Unseeded nodes start on a sunflower spiral, so the first frame is spread out.
    const angle = nodes.length * 2.39996 + hash(node.id);
    const radius = 14 * Math.sqrt(nodes.length + 1);
    node.x = seed ? seed.x : Math.cos(angle) * radius;
    node.y = seed ? seed.y : Math.sin(angle) * radius;
    node.vx = 0;
    node.vy = 0;
    node.degree = 0;
    index.set(node.id, nodes.length);
    nodes.push(node);
  };
  for (const entry of model.entries.values()) add({ id: entry.id, type: 'entry' });
  for (const source of model.sources.values()) {
    if (source.entries.size) add({ id: source.key, type: 'source' });
  }
  const links = [];
  const link = (from, to, kind, distance, strength) => {
    const source = index.get(from);
    const target = index.get(to);
    if (source === undefined || target === undefined) return;
    nodes[source].degree += 1;
    nodes[target].degree += 1;
    links.push({ source, target, kind, distance, strength });
  };
  for (const relation of model.relations) link(relation.from, relation.to, relation.kind, 60, 0.08);
  for (const entry of model.entries.values()) {
    for (const key of entry.sources) link(entry.id, key, 'cites', 46, 0.05);
  }
  for (const node of nodes) {
    node.r = node.type === 'entry'
      ? 4.5 + Math.min(7, Math.sqrt(node.degree) * 1.6)
      : 3.5 + Math.min(9, Math.sqrt(node.degree) * 1.8);
  }

  // Seeded positions only need settling; a fresh layout needs the full run.
  const base = previous.size ? SEEDED_STEPS : FRESH_STEPS;
  const steps = nodes.length
    ? Math.min(base, Math.max(MIN_STEPS, Math.round((base * BUDGET_NODES) / nodes.length)))
    : 0;

  function* simulate() {
    for (let done = 0; done < steps; done += 1) yield* step(1 - done / steps);
  }

  function* step(alpha) {
    const grid = new Map();
    for (const node of nodes) {
      const cx = Math.floor(node.x / CELL);
      const cy = Math.floor(node.y / CELL);
      const key = cellKey(cx, cy);
      let cell = grid.get(key);
      if (!cell) { cell = { cx, cy, nodes: [] }; grid.set(key, cell); }
      cell.nodes.push(node);
    }
    // Repulsion only between near neighbours keeps each step linear; gravity
    // below keeps separate clusters from drifting apart.
    let pairs = 0;
    for (const { cx, cy, nodes: here } of grid.values()) {
      for (let i = 0; i < here.length; i += 1) {
        for (let j = i + 1; j < here.length; j += 1) repel(here[i], here[j], alpha);
      }
      pairs += (here.length * (here.length - 1)) / 2;
      for (const [ox, oy] of FORWARD) {
        const there = grid.get(cellKey(cx + ox, cy + oy));
        if (!there) continue;
        for (const a of here) {
          for (const b of there.nodes) repel(a, b, alpha);
        }
        pairs += here.length * there.nodes.length;
      }
      // Forces only accumulate until the moves below, so pausing between
      // cells leaves the step's result unchanged.
      if (pairs >= PAUSE_PAIRS) { pairs = 0; yield; }
    }
    for (const { source, target, distance, strength } of links) {
      const a = nodes[source];
      const b = nodes[target];
      const dx = b.x - a.x;
      const dy = b.y - a.y;
      const d = Math.sqrt(dx * dx + dy * dy) || 1;
      const pull = (d - distance) * strength;
      a.vx += (dx / d) * pull; a.vy += (dy / d) * pull;
      b.vx -= (dx / d) * pull; b.vy -= (dy / d) * pull;
    }
    for (const node of nodes) {
      node.vx -= node.x * 0.004;
      node.vy -= node.y * 0.004;
      node.vx *= 0.55;
      node.vy *= 0.55;
      node.x += Math.max(-24, Math.min(24, node.vx));
      node.y += Math.max(-24, Math.min(24, node.vy));
    }
    yield;
  }

  const run = simulate();
  const layout = {
    nodes,
    index,
    links,
    steps,
    settled: steps === 0,
    bounds: { minX: 0, maxX: 0, minY: 0, maxY: 0 },
    // Runs until the deadline (a performance.now() time), at least one slice
    // per call. Returns true once the layout has settled.
    advance(deadline) {
      if (layout.settled) return true;
      let finished;
      do finished = run.next().done; while (!finished && performance.now() < deadline);
      if (finished) {
        layout.settled = true;
        let minX = Infinity; let maxX = -Infinity; let minY = Infinity; let maxY = -Infinity;
        for (const node of nodes) {
          minX = Math.min(minX, node.x - node.r); maxX = Math.max(maxX, node.x + node.r);
          minY = Math.min(minY, node.y - node.r); maxY = Math.max(maxY, node.y + node.r);
        }
        layout.bounds = { minX, maxX, minY, maxY };
      }
      return layout.settled;
    },
  };
  return layout;
}
