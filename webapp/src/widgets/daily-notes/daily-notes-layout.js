// A deterministic force layout for the Daily Notes graph. Notes that link to
// each other, or cite the same session, settle into one cluster; the session
// sources become the hubs between them.

const CELL = 140;

function hash(value) {
  let h = 2166136261;
  for (let i = 0; i < value.length; i += 1) {
    h ^= value.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return ((h >>> 0) % 10000) / 10000;
}

export function layoutDailyNotes(model, previous = new Map()) {
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
  const steps = previous.size ? 120 : 320;
  for (let step = 0; step < steps; step += 1) {
    const alpha = 1 - step / steps;
    const grid = new Map();
    for (let i = 0; i < nodes.length; i += 1) {
      const key = `${Math.floor(nodes[i].x / CELL)},${Math.floor(nodes[i].y / CELL)}`;
      if (!grid.has(key)) grid.set(key, []);
      grid.get(key).push(i);
    }
    // Repulsion only between near neighbours keeps each step linear; gravity
    // below keeps separate clusters from drifting apart.
    for (let i = 0; i < nodes.length; i += 1) {
      const a = nodes[i];
      const cx = Math.floor(a.x / CELL);
      const cy = Math.floor(a.y / CELL);
      for (let gx = cx - 1; gx <= cx + 1; gx += 1) {
        for (let gy = cy - 1; gy <= cy + 1; gy += 1) {
          for (const j of grid.get(`${gx},${gy}`) || []) {
            if (j <= i) continue;
            const b = nodes[j];
            let dx = b.x - a.x;
            let dy = b.y - a.y;
            let d2 = dx * dx + dy * dy;
            if (d2 > CELL * CELL) continue;
            if (d2 < 1) { dx = hash(`${a.id}${b.id}`) - 0.5; dy = 0.5; d2 = 1; }
            const force = (900 * (0.3 + alpha)) / d2;
            const d = Math.sqrt(d2);
            const fx = (dx / d) * force;
            const fy = (dy / d) * force;
            a.vx -= fx; a.vy -= fy; b.vx += fx; b.vy += fy;
          }
        }
      }
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
  }

  let minX = Infinity; let maxX = -Infinity; let minY = Infinity; let maxY = -Infinity;
  for (const node of nodes) {
    minX = Math.min(minX, node.x - node.r); maxX = Math.max(maxX, node.x + node.r);
    minY = Math.min(minY, node.y - node.r); maxY = Math.max(maxY, node.y + node.r);
  }
  return {
    nodes,
    index,
    links,
    bounds: nodes.length ? { minX, maxX, minY, maxY } : { minX: 0, maxX: 0, minY: 0, maxY: 0 },
  };
}
