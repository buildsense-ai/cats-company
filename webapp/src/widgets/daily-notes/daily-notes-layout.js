// A deterministic region layout for the Daily Notes graph. Each note belongs
// to one region, the source session it mostly came from; regions are packed
// side by side with clear space between them, so clusters read at a glance.
// Inside a region notes grow like a sunflower head: the newest at the core,
// the oldest at the rim, evenly spaced without a visible pattern.
//
// Sources with fewer than MIN_REGION notes share one "other" region rather
// than scattering small circles over the canvas.
//
// The layout is built in slices (advance) that pause every PAUSE_WORK units of
// work, so even a very large graph never holds the main thread for long.

const SPREAD = 8.6;
const GOLDEN_ANGLE = Math.PI * (3 - Math.sqrt(5));
const PADDING = 4;
const GAP = 30;
const MIN_REGION = 3;
// Distance between neighbouring notes; the graph switches to whole-cluster
// discs when this falls below a few pixels on screen.
export const NOTE_SPACING = SPREAD * Math.sqrt(Math.PI);
// Cluster colours the renderer provides; neighbours never share one.
export const HUE_COUNT = 7;
// Grid cell for finding the region under a point.
const INDEX_CELL = 160;
// Units of work (notes, region pairs) between pauses; well under 5 ms.
const PAUSE_WORK = 5000;

// Places circle c tangent to circles a and b (counter-clockwise from a to b).
// From d3-hierarchy's packSiblings (ISC licence), like the front-chain below.
function place(b, a, c) {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const d2 = dx * dx + dy * dy;
  if (!d2) { c.x = a.x + c.r; c.y = a.y; return; }
  let a2 = a.r + c.r; a2 *= a2;
  let b2 = b.r + c.r; b2 *= b2;
  if (a2 > b2) {
    const x = (d2 + b2 - a2) / (2 * d2);
    const y = Math.sqrt(Math.max(0, b2 / d2 - x * x));
    c.x = b.x - x * dx - y * dy;
    c.y = b.y - x * dy + y * dx;
  } else {
    const x = (d2 + a2 - b2) / (2 * d2);
    const y = Math.sqrt(Math.max(0, a2 / d2 - x * x));
    c.x = a.x + x * dx - y * dy;
    c.y = a.y + x * dy + y * dx;
  }
}

function intersects(a, b) {
  const dr = a.r + b.r - 1e-6;
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  return dr > 0 && dr * dr > dx * dx + dy * dy;
}

function score(node) {
  const a = node.circle;
  const b = node.next.circle;
  const ab = a.r + b.r;
  const dx = (a.x * b.r + b.x * a.r) / ab;
  const dy = (a.y * b.r + b.y * a.r) / ab;
  return dx * dx + dy * dy;
}

// Packs circles ({ r }) around the origin without overlap, largest first.
function packCircles(circles) {
  const n = circles.length;
  if (!n) return;
  let a = circles[0];
  a.x = 0; a.y = 0;
  if (n === 1) return;
  let b = circles[1];
  a.x = -b.r; b.x = a.r; b.y = 0;
  if (n === 2) return;
  let c = circles[2];
  place(b, a, c);
  a = { circle: a }; b = { circle: b }; c = { circle: c };
  a.next = c.previous = b;
  b.next = a.previous = c;
  c.next = b.previous = a;
  pack: for (let i = 3; i < n; i += 1) {
    place(a.circle, b.circle, circles[i]);
    c = { circle: circles[i] };
    let j = b.next;
    let k = a.previous;
    let sj = b.circle.r;
    let sk = a.circle.r;
    do {
      if (sj <= sk) {
        if (intersects(j.circle, c.circle)) {
          b = j; a.next = b; b.previous = a; i -= 1;
          continue pack;
        }
        sj += j.circle.r; j = j.next;
      } else {
        if (intersects(k.circle, c.circle)) {
          a = k; a.next = b; b.previous = a; i -= 1;
          continue pack;
        }
        sk += k.circle.r; k = k.previous;
      }
    } while (j !== k.next);
    c.previous = a; c.next = b; a.next = b.previous = b = c;
    let best = score(a);
    while ((c = c.next) !== b) {
      const value = score(c);
      if (value < best) { a = c; best = value; }
    }
    b = a.next;
  }
}

// Vogel's sunflower: about 15 px between neighbours at any count.
function sunflower(count) {
  const points = [];
  for (let i = 0; i < count; i += 1) {
    const radius = SPREAD * Math.sqrt(i + 0.5);
    points.push({ x: Math.cos(i * GOLDEN_ANGLE) * radius, y: Math.sin(i * GOLDEN_ANGLE) * radius, radius });
  }
  return points;
}

// Orders regions so that each one follows the region it is most linked to;
// the packing places consecutive regions side by side, so related clusters
// end up as neighbours. The largest region starts; the "other" region ends.
function* orderByLinks(regions, weights) {
  const named = regions.filter((region) => !region.other);
  const score = new Float64Array(regions.length);
  const placed = new Uint8Array(regions.length);
  const order = [];
  for (let n = 0; n < named.length; n += 1) {
    let best = -1;
    for (let i = 0; i < named.length; i += 1) {
      if (!placed[i] && (best < 0 || score[i] > score[best])) best = i;
    }
    if (n % Math.max(1, Math.floor(PAUSE_WORK / named.length)) === 0) yield;
    placed[best] = 1;
    order.push(named[best]);
    for (const [other, weight] of weights.get(best) || []) score[other] += weight;
  }
  return regions.length > named.length ? [...order, regions[regions.length - 1]] : order;
}

// Gives each region the least-used colour that no packed neighbour has, so
// all colours stay in play; the largest region takes colour 0 (the accent).
// Neighbours come from the region grid.
function assignHues(regions, grid) {
  const reach = GAP + 40;
  const uses = new Array(HUE_COUNT).fill(0);
  for (const region of regions) region.hue = -2;
  for (const [i, region] of regions.entries()) {
    const used = new Map();
    const seen = new Set();
    for (let cx = Math.floor((region.x - region.r - reach) / INDEX_CELL); cx <= Math.floor((region.x + region.r + reach) / INDEX_CELL); cx += 1) {
      for (let cy = Math.floor((region.y - region.r - reach) / INDEX_CELL); cy <= Math.floor((region.y + region.r + reach) / INDEX_CELL); cy += 1) {
        for (const j of grid.get(`${cx},${cy}`) || []) {
          const other = regions[j];
          if (j === i || seen.has(j) || other.hue < 0) continue;
          seen.add(j);
          if (Math.hypot(other.x - region.x, other.y - region.y) < other.r + region.r + reach) {
            used.set(other.hue, (used.get(other.hue) || 0) + 1);
          }
        }
      }
    }
    let hue = -1;
    for (let h = 0; h < HUE_COUNT; h += 1) {
      if (!used.has(h) && (hue < 0 || uses[h] < uses[hue])) hue = h;
    }
    if (hue < 0) hue = [...used].sort((a, b) => a[1] - b[1] || a[0] - b[0])[0][0];
    region.hue = region.other ? -1 : hue;
    if (!region.other) uses[hue] += 1;
  }
}

// The region under a layout point, or -1. Each region is listed in every
// grid cell its bounding box touches, so a lookup reads one cell.
export function regionAt(layout, x, y) {
  const cell = layout.regionGrid.get(`${Math.floor(x / INDEX_CELL)},${Math.floor(y / INDEX_CELL)}`);
  for (const i of cell || []) {
    const region = layout.regions[i];
    if (Math.hypot(region.x - x, region.y - y) <= region.r) return i;
  }
  return -1;
}

export function createDailyNotesLayout(model) {
  const layout = { settled: false };
  const run = build(model, layout);
  // Runs until the deadline (a performance.now() time), at least one slice
  // per call. Returns true once the layout is complete.
  layout.advance = (deadline) => {
    if (layout.settled) return true;
    let finished;
    do finished = run.next().done; while (!finished && performance.now() < deadline);
    layout.settled = finished;
    return finished;
  };
  return layout;
}

function* build(model, layout) {
  let work = 0;
  const pause = (units = 1) => (work += units) >= PAUSE_WORK;
  // A note's region is its largest source, so a note that cites a busy group
  // chat and a one-off session joins the group chat's cluster.
  const groups = new Map();
  const other = [];
  for (const { entries } of model.days) {
    for (const entry of entries) {
      if (pause()) { work = 0; yield; }
      let home = '';
      for (const key of entry.sources) {
        const size = model.sources.get(key)?.entries.size || 0;
        const best = model.sources.get(home)?.entries.size || 0;
        if (!home || size > best || (size === best && key < home)) home = key;
      }
      if (!home) { other.push(entry); continue; }
      if (!groups.has(home)) groups.set(home, []);
      groups.get(home).push(entry);
    }
  }
  let regions = [];
  for (const [key, entries] of groups) {
    if (entries.length < MIN_REGION) { other.push(...entries); continue; }
    regions.push({ key, label: model.sources.get(key).label, entries });
  }
  regions.sort((a, b) => b.entries.length - a.entries.length || a.key.localeCompare(b.key));
  if (other.length) {
    // Keep the shared region in time order too.
    const rank = new Map(model.days.map(({ day }, i) => [day, i]));
    other.sort((a, b) => rank.get(a.day) - rank.get(b.day));
    regions.push({ key: '', label: '其他', entries: other, other: true });
  }

  // Links between regions: note relations and citations of another region's
  // session. They order the packing and become the overview's region links.
  const homeOf = new Map();
  const regionOfKey = new Map();
  for (const [i, region] of regions.entries()) {
    if (pause(region.entries.length)) { work = 0; yield; }
    for (const entry of region.entries) homeOf.set(entry.id, i);
    if (region.key) regionOfKey.set(region.key, i);
  }
  const pairs = [];
  for (const { from, to, kind } of model.relations) {
    if (pause()) { work = 0; yield; }
    if (homeOf.has(from) && homeOf.has(to) && homeOf.get(from) !== homeOf.get(to)) pairs.push([homeOf.get(from), homeOf.get(to), kind]);
  }
  for (const entry of model.entries.values()) {
    if (pause()) { work = 0; yield; }
    for (const key of entry.sources) {
      if (regionOfKey.has(key) && regionOfKey.get(key) !== homeOf.get(entry.id)) pairs.push([homeOf.get(entry.id), regionOfKey.get(key), 'cites']);
    }
  }
  const weights = new Map();
  const addWeight = (a, b) => {
    if (!weights.has(a)) weights.set(a, new Map());
    weights.get(a).set(b, (weights.get(a).get(b) || 0) + 1);
  };
  for (const [a, b] of pairs) { addWeight(a, b); addWeight(b, a); }
  const ordered = yield* orderByLinks(regions, weights);
  const newIndex = new Map(ordered.map((region, i) => [regions.indexOf(region), i]));
  regions = ordered;

  const degree = new Map();
  for (const { from, to } of model.relations) {
    if (pause()) { work = 0; yield; }
    degree.set(from, (degree.get(from) || 0) + 1);
    degree.set(to, (degree.get(to) || 0) + 1);
  }

  const nodes = [];
  const index = new Map();
  const regionIndex = new Map();
  for (const [i, region] of regions.entries()) {
    const points = sunflower(region.entries.length);
    region.members = [];
    region.count = region.entries.length;
    region.offsets = points;
    for (const entry of region.entries) {
      if (pause()) { work = 0; yield; }
      index.set(entry.id, nodes.length);
      region.members.push(nodes.length);
      nodes.push({
        id: entry.id,
        type: 'entry',
        region: i,
        degree: degree.get(entry.id) || 0,
        r: 4 + Math.min(2.5, Math.sqrt(degree.get(entry.id) || 0) * 1.1),
      });
    }
    region.r = points[points.length - 1].radius + 6.5 + PADDING;
    if (region.key) regionIndex.set(region.key, i);
  }
  // Pack with half the gap around each region, then place the notes.
  const circles = regions.map((region) => ({ r: region.r + GAP / 2 }));
  packCircles(circles);
  const regionGrid = new Map();
  yield;
  for (const [i, region] of regions.entries()) {
    if (pause(region.members.length)) { work = 0; yield; }
    region.x = circles[i].x;
    region.y = circles[i].y;
    region.members.forEach((nodeIndex, n) => {
      nodes[nodeIndex].x = region.x + region.offsets[n].x;
      nodes[nodeIndex].y = region.y + region.offsets[n].y;
    });
    delete region.entries;
    delete region.offsets;
    for (let cx = Math.floor((region.x - region.r) / INDEX_CELL); cx <= Math.floor((region.x + region.r) / INDEX_CELL); cx += 1) {
      for (let cy = Math.floor((region.y - region.r) / INDEX_CELL); cy <= Math.floor((region.y + region.r) / INDEX_CELL); cy += 1) {
        const cell = `${cx},${cy}`;
        if (!regionGrid.has(cell)) regionGrid.set(cell, []);
        regionGrid.get(cell).push(i);
      }
    }
  }
  yield;
  assignHues(regions, regionGrid);
  yield;

  const links = [];
  for (const relation of model.relations) {
    if (pause()) { work = 0; yield; }
    const source = index.get(relation.from);
    const target = index.get(relation.to);
    if (source !== undefined && target !== undefined) links.push({ source, target, kind: relation.kind });
  }
  // Citations of a session other than the note's own region are the bridges
  // between clusters; citations of the home region are shown by membership.
  const bridges = [];
  yield;
  for (const [nodeIndex, node] of nodes.entries()) {
    if (pause()) { work = 0; yield; }
    for (const key of model.entries.get(node.id).sources) {
      const region = regionIndex.get(key);
      if (region !== undefined && region !== node.region) bridges.push({ source: nodeIndex, region });
    }
  }
  // At the overview every link between two regions draws as one line.
  const regionLinks = new Map();
  for (const [a0, b0, kind] of pairs) {
    if (pause()) { work = 0; yield; }
    const a = Math.min(newIndex.get(a0), newIndex.get(b0));
    const b = Math.max(newIndex.get(a0), newIndex.get(b0));
    const key = a * regions.length + b;
    if (!regionLinks.has(key)) regionLinks.set(key, { a, b, count: 0, corrects: 0 });
    regionLinks.get(key).count += 1;
    if (kind === 'corrects') regionLinks.get(key).corrects += 1;
  }

  let minX = 0; let maxX = 0; let minY = 0; let maxY = 0;
  if (regions.length) {
    minX = Infinity; maxX = -Infinity; minY = Infinity; maxY = -Infinity;
    for (const { x, y, r } of regions) {
      minX = Math.min(minX, x - r); maxX = Math.max(maxX, x + r);
      minY = Math.min(minY, y - r); maxY = Math.max(maxY, y + r);
    }
  }

  Object.assign(layout, {
    nodes,
    index,
    regions,
    regionIndex,
    regionGrid,
    links,
    bridges,
    regionLinks: [...regionLinks.values()],
    bounds: { minX, maxX, minY, maxY },
  });
}
