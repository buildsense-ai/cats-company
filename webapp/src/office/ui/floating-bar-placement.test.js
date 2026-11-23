import { describe, expect, it } from 'vitest';
import {
  clampDragOffset, placeBarBeside, shouldDockBar,
} from './floating-bar-placement.js';

const container = { width: 900, height: 600 };
const size = { width: 330, height: 148 };

describe('bar sits beside the block', () => {
  it('goes to the right of a block with room there', () => {
    const result = placeBarBeside({
      anchor: { left: 100, top: 120, width: 200, height: 60 }, container, size,
    });
    expect(result.side).toBe('right');
    expect(result.left).toBe(310);
    expect(result.top).toBe(120);
  });

  it('flips to the left of a block near the right edge', () => {
    const result = placeBarBeside({
      anchor: { left: 620, top: 120, width: 180, height: 60 }, container, size,
    });
    expect(result.side).toBe('left');
    expect(result.left).toBe(280);
  });

  it('parks against the right edge when neither side fits', () => {
    // A Word paragraph spans the page, so both margins are narrower than the bar. Parking it
    // under the block would cover the next paragraphs and make them unclickable.
    const result = placeBarBeside({
      anchor: { left: 220, top: 120, width: 420, height: 60 }, container, size,
    });
    expect(result.side).toBe('edge');
    expect(result.left).toBe(900 - 330 - 8);
    // It must sit on the block's row, not below it, so the paragraphs underneath keep working.
    expect(result.top).toBe(120);
  });

  it('never leaves the stage on either axis', () => {
    const low = placeBarBeside({
      anchor: { left: 820, top: 560, width: 120, height: 60 }, container, size,
    });
    expect(low.top).toBeLessThanOrEqual(600 - 148 - 8);
    expect(low.left).toBeLessThanOrEqual(900 - 330 - 8);

    const huge = placeBarBeside({
      anchor: { left: 0, top: 0, width: 40, height: 20 },
      container,
      size: { width: 2000, height: 40 },
    });
    expect(huge.left).toBeGreaterThanOrEqual(8);
  });

  it('zeroes out before the stage has been measured instead of guessing', () => {
    const result = placeBarBeside({
      anchor: { left: 10, top: 10, width: 10, height: 10 },
      container: { width: 0, height: 0 },
      size,
    });
    expect(result).toEqual({ left: 0, top: 0, side: 'below' });
  });
});

describe('docking', () => {
  it('docks only near the right edge', () => {
    expect(shouldDockBar({ pointerX: 880, containerWidth: 900 })).toBe(true);
    expect(shouldDockBar({ pointerX: 840, containerWidth: 900 })).toBe(false);
    expect(shouldDockBar({ pointerX: 900, containerWidth: 0 })).toBe(false);
  });
});

describe('drag stays inside the container', () => {
  const position = { left: 300, top: 120 };
  const container = { width: 900, height: 600 };
  const barSize = { width: 330, height: 160 };

  it('lets the bar move freely while it remains visible', () => {
    const result = clampDragOffset({
      offset: { x: -40, y: 30 }, position, size: barSize, container,
    });
    expect(result).toEqual({ x: -40, y: 30 });
  });

  it('stops a drag that would push the bar past the right edge', () => {
    // 600px to the right would put the bar at x=900, entirely outside a 900px container.
    const result = clampDragOffset({
      offset: { x: 600, y: 0 }, position, size: barSize, container,
    });
    expect(position.left + result.x + barSize.width).toBeLessThanOrEqual(container.width - 8);
  });

  it('stops a drag that would push the bar past the left edge', () => {
    const result = clampDragOffset({
      offset: { x: -900, y: 0 }, position, size: barSize, container,
    });
    expect(position.left + result.x).toBeGreaterThanOrEqual(8);
  });

  it('stops a drag past the bottom edge', () => {
    const result = clampDragOffset({
      offset: { x: 0, y: 900 }, position, size: barSize, container,
    });
    expect(position.top + result.y + barSize.height).toBeLessThanOrEqual(container.height - 8);
  });

  it('passes the offset through before the container has been measured', () => {
    const result = clampDragOffset({
      offset: { x: 25, y: -10 },
      position,
      size: barSize,
      container: { width: 0, height: 0 },
    });
    expect(result).toEqual({ x: 25, y: -10 });
  });

  it('survives missing numbers instead of producing NaN', () => {
    const result = clampDragOffset({});
    expect(Number.isNaN(result.x)).toBe(false);
    expect(Number.isNaN(result.y)).toBe(false);
  });
});
