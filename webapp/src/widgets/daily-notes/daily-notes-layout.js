// A deterministic force layout for the Daily Notes network. Notes are pulled
// toward their day's row, newest at the top like the list, so the network
// scrolls as a timeline; the sessions they cite settle between those days.

export const DAY_SPACING = 120;
const CELL = 90;

function hash(value) {
  let h = 2166136261;
  for (let i = 0; i < value.length; i += 1) {
    h ^= value.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return ((h >>> 0) % 10000) / 10000;
}

export function layoutDailyNotes(model, previous = new Map()) {
  const days = model.days.map(({ day }) => day).sort().reverse();
  const dayY = new Map(days.map((day, index) => [day, index * DAY_SPACING]));
  const nodes = [];
  const index = new Map();
  const add = (node) => {
    const seed = previous.get(node.id);
    node.x = seed ? seed.x : (hash(node.id) - 0.5) * 420;
    node.y = seed ? seed.y : node.ty + (hash(`${node.id}:y`) - 0.5) * 40;
    node.vx = 0;
    node.vy = 0;
    index.set(node.id, nodes.length);
    nodes.push(node);
  };
  for (const entry of model.entries.values()) {
    add({ id: entry.id, type: 'entry', ty: dayY.get(entry.day) || 0, r: 6 + Math.min(6, entry.links.length * 1.5) });
  }
  for (const source of model.sources.values()) {
    if (!source.entries.size) continue;
    let sum = 0;
    for (const id of source.entries) sum += dayY.get(model.entries.get(id)?.day) || 0;
    add({ id: source.key, type: 'source', ty: sum / source.entries.size, r: 4 + Math.min(5, Math.sqrt(source.entries.size) * 1.4) });
  }
  const links = [];
  for (const relation of model.relations) {
    links.push({ source: index.get(relation.from), target: index.get(relation.to), kind: relation.kind, distance: 120, strength: 0.05 });
  }
  for (const entry of model.entries.values()) {
    for (const key of entry.sources) {
      if (index.has(key)) links.push({ source: index.get(entry.id), target: index.get(key), kind: 'cites', distance: 70, strength: 0.03 });
    }
  }

  // Seeded positions only need settling; a fresh layout needs the full run.
  const steps = previous.size ? 120 : 280;
  for (let step = 0; step < steps; step += 1) {
    const alpha = 1 - step / steps;
    const grid = new Map();
    for (let i = 0; i < nodes.length; i += 1) {
      const key = `${Math.floor(nodes[i].x / CELL)},${Math.floor(nodes[i].y / CELL)}`;
      if (!grid.has(key)) grid.set(key, []);
      grid.get(key).push(i);
    }
    // Repulsion only between near neighbours keeps each step linear.
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
            const force = (1800 * alpha) / d2;
            const d = Math.sqrt(d2);
            const fx = (dx / d) * force;
            const fy = (dy / d) * force;
            a.vx -= fx; a.vy -= fy; b.vx += fx; b.vy += fy;
          }
        }
      }
    }
    for (const link of links) {
      const a = nodes[link.source];
      const b = nodes[link.target];
      const dx = b.x - a.x;
      const dy = b.y - a.y;
      const d = Math.sqrt(dx * dx + dy * dy) || 1;
      const pull = (d - link.distance) * link.strength * alpha;
      a.vx += (dx / d) * pull; a.vy += (dy / d) * pull;
      b.vx -= (dx / d) * pull; b.vy -= (dy / d) * pull;
    }
    for (const node of nodes) {
      node.vy += (node.ty - node.y) * (node.type === 'entry' ? 0.12 : 0.04) * alpha;
      node.vx += -node.x * 0.006 * alpha;
      node.vx *= 0.6;
      node.vy *= 0.6;
      node.x += Math.max(-30, Math.min(30, node.vx));
      node.y += Math.max(-30, Math.min(30, node.vy));
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
    days: days.map((day) => ({ day, y: dayY.get(day) })),
    bounds: nodes.length ? { minX, maxX, minY, maxY } : { minX: 0, maxX: 0, minY: 0, maxY: 0 },
  };
}
