// Geometry, z-order and undo/redo.
//
// These cover what phase 3 adds over phase 2: dragging must not drift, resizing from a
// north/west handle must move the origin too, z-order must actually reorder nodes rather
// than set an attribute, and the history stack must not lose or invent states.

import { describe, it, expect } from 'vitest';
import { buildPptx, titleSlideXml } from './test-fixtures/build-pptx.js';
import { parsePptx } from './pptx/parse.js';
import { writePptx, applyZOrderEdit, validateSlidePart } from './pptx/write.js';
import {
  MIN_SHAPE_EMU, alignBoxes, clampBoxToSlide, distributeBoxes, emuPerPixel,
  moveBox, resizeBox, snapBox,
} from './pptx/geometry.js';
import {
  UNDO_LIMIT, canRedo, canUndo, createHistory, pushHistory, redoHistory, resetHistory, undoHistory,
} from './ui/history.js';

const SLIDE = { cx: 12192000, cy: 6858000 };

describe('coordinate conversion', () => {
  it('derives EMU per pixel from the live rect rather than a fixed constant', () => {
    // A 960 px wide canvas showing a 13.33 inch slide: 12192000 / 960 = 12700 EMU per px.
    expect(emuPerPixel(960, SLIDE.cx)).toBe(12700);
    // Half the panel width doubles the factor; that is why the factor cannot be cached.
    expect(emuPerPixel(480, SLIDE.cx)).toBe(25400);
  });

  it('returns zero instead of NaN or Infinity for a collapsed canvas', () => {
    expect(emuPerPixel(0, SLIDE.cx)).toBe(0);
    expect(emuPerPixel(960, 0)).toBe(0);
    expect(emuPerPixel(Number.NaN, SLIDE.cx)).toBe(0);
  });
});

describe('move', () => {
  it('accumulates from the gesture anchor, so repeated moves do not drift', () => {
    const anchor = { x: 1000000, y: 500000, cx: 3000000, cy: 2000000 };
    const factor = { x: 12700, y: 12700 };
    const first = moveBox(anchor, { dx: 10, dy: 10 }, factor);
    const second = moveBox(anchor, { dx: 10, dy: 10 }, factor);
    // Same delta from the same anchor must give the same box, not a doubled one.
    expect(first).toEqual(second);
    expect(first.x).toBe(1000000 + 127000);
  });

  it('rounds to whole EMU so a save cannot write fractional coordinates', () => {
    const moved = moveBox({ x: 0, y: 0, cx: 1, cy: 1 }, { dx: 1, dy: 1 }, { x: 12700.7, y: 12700.3 });
    expect(Number.isInteger(moved.x)).toBe(true);
    expect(Number.isInteger(moved.y)).toBe(true);
  });
});

describe('resize', () => {
  it('moves the origin when dragging a north-west handle', () => {
    const anchor = { x: 1000000, y: 1000000, cx: 2000000, cy: 2000000 };
    const next = resizeBox(anchor, 'nw', { dx: 10, dy: 10 }, { x: 12700, y: 12700 });
    // Dragging the top-left corner inward must move x/y and shrink the box together;
    // resizing without moving the origin leaves the opposite corner floating.
    expect(next.x).toBe(1000000 + 127000);
    expect(next.y).toBe(1000000 + 127000);
    expect(next.cx).toBe(2000000 - 127000);
    expect(next.cy).toBe(2000000 - 127000);
  });

  it('keeps the origin fixed when dragging a south-east handle', () => {
    const anchor = { x: 1000000, y: 1000000, cx: 2000000, cy: 2000000 };
    const next = resizeBox(anchor, 'se', { dx: 10, dy: 10 }, { x: 12700, y: 12700 });
    expect(next.x).toBe(1000000);
    expect(next.y).toBe(1000000);
    expect(next.cx).toBe(2000000 + 127000);
    expect(next.cy).toBe(2000000 + 127000);
  });

  it('refuses to shrink below one point', () => {
    const anchor = { x: 0, y: 0, cx: 12700, cy: 12700 };
    const next = resizeBox(anchor, 'se', { dx: -100, dy: -100 }, { x: 12700, y: 12700 });
    expect(next.cx).toBe(MIN_SHAPE_EMU);
    expect(next.cy).toBe(MIN_SHAPE_EMU);
  });

  it('does not invert the box when dragging a handle past the opposite edge', () => {
    const anchor = { x: 1000000, y: 1000000, cx: 200000, cy: 200000 };
    const next = resizeBox(anchor, 'w', { dx: 500, dy: 0 }, { x: 12700, y: 12700 });
    // The width cannot go negative; the box keeps its minimum and the origin stays put.
    expect(next.cx).toBeGreaterThanOrEqual(MIN_SHAPE_EMU);
    expect(next.x).toBe(1000000);
  });
});

describe('snapping', () => {
  it('snaps to the horizontal centre only within the threshold', () => {
    const centred = { x: Math.round((SLIDE.cx - 2000000) / 2) + 30000, y: 0, cx: 2000000, cy: 1000000 };
    const snapped = snapBox(centred, SLIDE, 60000);
    expect(snapped.x).toBe(Math.round((SLIDE.cx - 2000000) / 2));
    expect(snapped.snappedX).toBe('center');

    const far = { x: 3000000, y: 0, cx: 2000000, cy: 1000000 };
    expect(snapBox(far, SLIDE, 60000).snappedX).toBeUndefined();
  });

  it('snaps each axis independently', () => {
    const box = { x: 10000, y: Math.round((SLIDE.cy - 1000000) / 2), cx: 500000, cy: 1000000 };
    const snapped = snapBox(box, SLIDE, 30000);
    expect(snapped.snappedX).toBe('left');
    expect(snapped.snappedY).toBe('middle');
  });

  it('does nothing when the threshold is zero', () => {
    const box = { x: 1, y: 1, cx: 100, cy: 100 };
    expect(snapBox(box, SLIDE, 0)).toEqual(box);
  });
});

describe('clamping', () => {
  it('keeps a sliver of the shape on the slide so it stays grabbable', () => {
    const box = { x: -5000000, y: -5000000, cx: 1000000, cy: 1000000 };
    const clamped = clampBoxToSlide(box, SLIDE);
    // The box may hang off, but not so far that no part is clickable.
    expect(clamped.x).toBeGreaterThan(-1000000);
    expect(clamped.y).toBeGreaterThan(-1000000);
    expect(clamped.cx).toBe(1000000);
  });

  it('enforces the minimum size', () => {
    const clamped = clampBoxToSlide({ x: 0, y: 0, cx: 0, cy: 0 }, SLIDE);
    expect(clamped.cx).toBe(MIN_SHAPE_EMU);
    expect(clamped.cy).toBe(MIN_SHAPE_EMU);
  });
});

describe('align and distribute', () => {
  const boxes = [
    { elId: 'a', x: 100, y: 100, cx: 100, cy: 100 },
    { elId: 'b', x: 400, y: 200, cx: 200, cy: 100 },
    { elId: 'c', x: 900, y: 300, cx: 100, cy: 100 },
  ];

  it('aligns left to the common bounding box, not to zero', () => {
    const aligned = alignBoxes(boxes, 'left');
    expect(aligned.map(box => box.x)).toEqual([100, 100, 100]);
  });

  it('aligns right by moving each box to the shared right edge', () => {
    const aligned = alignBoxes(boxes, 'right');
    // Right edge is max(x + cx) = 1000, so each box lands so its own right edge hits 1000.
    expect(aligned.map(box => box.x + box.cx)).toEqual([1000, 1000, 1000]);
  });

  it('distributes with even gaps rather than an even stride', () => {
    const spread = [
      { elId: 'a', x: 0, y: 0, cx: 100, cy: 10 },
      { elId: 'b', x: 300, y: 0, cx: 200, cy: 10 },
      { elId: 'c', x: 1000, y: 0, cx: 100, cy: 10 },
    ];
    const out = distributeBoxes(spread, 'x');
    const gap1 = out[1].x - (out[0].x + out[0].cx);
    const gap2 = out[2].x - (out[1].x + out[1].cx);
    expect(Math.abs(gap1 - gap2)).toBeLessThanOrEqual(1);
    // The outermost boxes stay put; only the middle one moves.
    expect(out[0].x).toBe(0);
    expect(out[2].x).toBe(1000);
  });

  it('leaves fewer than three boxes alone when distributing', () => {
    const two = boxes.slice(0, 2);
    expect(distributeBoxes(two, 'x')).toBe(two);
  });
});

describe('z-order', () => {
  async function twoShapeSlide() {
    const model = await parsePptx(await buildPptx({
      slides: [titleSlideXml({ title: '前面的标题' })],
    }));
    const slide = model.slides[0];
    const partXml = await model.pkg.text(slide.part);
    return { model, slide, partXml };
  }

  it('moves a shape to the front by reordering nodes, not by writing an attribute', async () => {
    const { slide, partXml } = await twoShapeSlide();
    const [title, body] = slide.shapes;
    const next = applyZOrderEdit(partXml, title, 'front');

    // The body's text must now come before the title's in document order, because later
    // nodes paint on top. No z-index attribute exists in OOXML.
    expect(next.indexOf('第一点')).toBeLessThan(next.indexOf('前面的标题'));
    expect(next).not.toContain('zIndex');
    expect(validateSlidePart(next)).toEqual([]);
    expect(body.elId).not.toBe(title.elId);
  });

  it('moves a shape to the back', async () => {
    const { slide, partXml } = await twoShapeSlide();
    const [, body] = slide.shapes;
    const next = applyZOrderEdit(partXml, body, 'back');
    expect(next.indexOf('第一点')).toBeLessThan(next.indexOf('前面的标题'));
  });

  it('moves a shape exactly one layer forward or backward', async () => {
    const { slide, partXml } = await twoShapeSlide();
    const [title, body] = slide.shapes;
    const up = applyZOrderEdit(partXml, title, 'up');
    expect(up.indexOf('第一点')).toBeLessThan(up.indexOf('前面的标题'));

    const down = applyZOrderEdit(up, body, 'down');
    expect(down.indexOf('前面的标题')).toBeLessThan(down.indexOf('第一点'));
  });

  it('is a no-op when already at the requested end', async () => {
    const { slide, partXml } = await twoShapeSlide();
    const [, body] = slide.shapes;
    const next = applyZOrderEdit(partXml, body, 'front');
    expect(next).toBe(partXml);
  });

  it('keeps the non-shape children in place', async () => {
    const { slide, partXml } = await twoShapeSlide();
    const [title] = slide.shapes;
    const next = applyZOrderEdit(partXml, title, 'front');
    // nvGrpSpPr and grpSpPr are group properties, not shape slots. They must stay ahead
    // of every shape, or Office treats the tree as malformed even though the shapes
    // themselves are intact. The fixture writes grpSpPr self-closing, so that is the form
    // to assert on rather than an open tag.
    expect(next).toContain('<p:grpSpPr/>');
    expect(next.indexOf('<p:nvGrpSpPr>')).toBeLessThan(next.indexOf('<p:grpSpPr/>'));
    expect(next.indexOf('<p:nvGrpSpPr>')).toBeLessThan(next.indexOf('前面的标题'));
  });

  it('writes z-order through writePptx and re-parses in the new order', async () => {
    const { model, slide } = await twoShapeSlide();
    const [title] = slide.shapes;
    const result = await writePptx(model, [
      { slideIndex: 0, elId: title.elId, op: 'zorder', payload: { direction: 'front' } },
    ]);
    expect(result.changed).toBe(true);
    const reparsed = await parsePptx(result.bytes);
    const texts = reparsed.slides[0].shapes.map(shape => shape.text);
    expect(texts[0]).toContain('第一点');
    expect(texts[1]).toContain('前面的标题');
  });
});

describe('undo history', () => {
  it('walks back and forward through states', () => {
    let history = createHistory({ n: 0 });
    history = pushHistory(history, { n: 1 });
    history = pushHistory(history, { n: 2 });

    expect(history.present.n).toBe(2);
    history = undoHistory(history);
    expect(history.present.n).toBe(1);
    history = undoHistory(history);
    expect(history.present.n).toBe(0);
    expect(canUndo(history)).toBe(false);

    history = redoHistory(history);
    expect(history.present.n).toBe(1);
    expect(canRedo(history)).toBe(true);
  });

  it('drops the redo branch once a new edit lands', () => {
    let history = createHistory({ n: 0 });
    history = pushHistory(history, { n: 1 });
    history = undoHistory(history);
    expect(canRedo(history)).toBe(true);

    history = pushHistory(history, { n: 9 });
    // The undone state is no longer reachable, which is the honest behaviour: the user
    // edited after undoing, so that branch is gone.
    expect(canRedo(history)).toBe(false);
    expect(history.present.n).toBe(9);
  });

  it('caps depth so a long drag session cannot grow without bound', () => {
    let history = createHistory(0);
    for (let i = 1; i <= UNDO_LIMIT + 20; i += 1) history = pushHistory(history, i);
    expect(history.past.length).toBe(UNDO_LIMIT);
    expect(history.present).toBe(UNDO_LIMIT + 20);
    // The oldest states were dropped, so undoing UNDO_LIMIT times reaches the cap floor,
    // never below it.
    for (let i = 0; i < UNDO_LIMIT; i += 1) history = undoHistory(history);
    expect(canUndo(history)).toBe(false);
    expect(history.present).toBe(20);
  });

  it('replaces the present without adding history for non-user changes', () => {
    let history = createHistory({ n: 0 });
    history = resetHistory(history, { n: 5 });
    expect(history.present.n).toBe(5);
    expect(history.past).toHaveLength(0);
    expect(canUndo(history)).toBe(false);
  });

  it('ignores undo and redo when the stack is empty', () => {
    const history = createHistory({ n: 0 });
    expect(undoHistory(history)).toBe(history);
    expect(redoHistory(history)).toBe(history);
  });
});
