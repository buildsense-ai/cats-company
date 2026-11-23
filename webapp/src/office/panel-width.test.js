import { beforeEach, describe, expect, it } from 'vitest';
import {
  clampOfficePanelWidth, officePanelWidthFromPointer, OFFICE_PANEL_WIDTH_DEFAULT,
  OFFICE_PANEL_WIDTH_MAX, OFFICE_PANEL_WIDTH_MIN, OFFICE_PANEL_WIDTH_STORAGE_KEY,
  readOfficePanelWidth, saveOfficePanelWidth, shouldShowOfficePanelResizer,
} from './ui/office-panel-width.js';

describe('office panel width', () => {
  beforeEach(() => localStorage.clear());

  it('clamps persisted and pointer-derived widths to 220..560', () => {
    expect(clampOfficePanelWidth(100)).toBe(OFFICE_PANEL_WIDTH_MIN);
    expect(clampOfficePanelWidth(900)).toBe(OFFICE_PANEL_WIDTH_MAX);
    expect(clampOfficePanelWidth(333.6)).toBe(334);
    expect(clampOfficePanelWidth('not-a-number')).toBe(OFFICE_PANEL_WIDTH_DEFAULT);
    expect(officePanelWidthFromPointer(300, 500, 400)).toBe(400);
    expect(officePanelWidthFromPointer(300, 500, 1000)).toBe(OFFICE_PANEL_WIDTH_MIN);
    expect(officePanelWidthFromPointer(300, 500, -1000)).toBe(OFFICE_PANEL_WIDTH_MAX);
  });

  it('reads the default when storage is empty or corrupt', () => {
    expect(readOfficePanelWidth()).toBe(300);
    localStorage.setItem(OFFICE_PANEL_WIDTH_STORAGE_KEY, 'garbage');
    expect(readOfficePanelWidth()).toBe(300);
    localStorage.setItem(OFFICE_PANEL_WIDTH_STORAGE_KEY, '441');
    expect(readOfficePanelWidth()).toBe(441);
  });

  it('persists the clamped width so refresh restores the same panel size', () => {
    expect(saveOfficePanelWidth(999)).toBe(560);
    expect(localStorage.getItem(OFFICE_PANEL_WIDTH_STORAGE_KEY)).toBe('560');
    expect(readOfficePanelWidth()).toBe(560);
  });

  it('hides the resizer below the 900px single-column breakpoint', () => {
    expect(shouldShowOfficePanelResizer(899)).toBe(false);
    expect(shouldShowOfficePanelResizer(900)).toBe(true);
  });
});
