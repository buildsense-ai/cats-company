import { describe, expect, it } from 'vitest';
import { placeBarBeside, shouldDockBar } from './floating-bar-placement.js';

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

  it('goes below and clamps when neither side fits', () => {
    const result = placeBarBeside({
      anchor: { left: 220, top: 120, width: 420, height: 60 }, container, size,
    });
    expect(result.side).toBe('below');
    expect(result.left).toBeGreaterThanOrEqual(8);
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
