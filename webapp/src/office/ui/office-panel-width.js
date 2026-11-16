import { readStorageValue, writeStorageValue } from '../../utils/storage-access';

export const OFFICE_PANEL_WIDTH_STORAGE_KEY = 'office.panelWidth';
export const OFFICE_PANEL_WIDTH_DEFAULT = 300;
export const OFFICE_PANEL_WIDTH_MIN = 220;
export const OFFICE_PANEL_WIDTH_MAX = 560;
export const OFFICE_PANEL_RESIZER_MIN_VIEWPORT = 900;

export function clampOfficePanelWidth(value) {
  if (value === null || value === undefined
    || (typeof value === 'string' && value.trim() === '')) {
    return OFFICE_PANEL_WIDTH_DEFAULT;
  }
  const width = Number(value);
  if (!Number.isFinite(width)) return OFFICE_PANEL_WIDTH_DEFAULT;
  return Math.round(Math.min(
    OFFICE_PANEL_WIDTH_MAX,
    Math.max(OFFICE_PANEL_WIDTH_MIN, width),
  ));
}

export function readOfficePanelWidth(storage = 'localStorage') {
  return clampOfficePanelWidth(readStorageValue(OFFICE_PANEL_WIDTH_STORAGE_KEY, storage));
}

export function saveOfficePanelWidth(value, storage = 'localStorage') {
  const width = clampOfficePanelWidth(value);
  writeStorageValue(OFFICE_PANEL_WIDTH_STORAGE_KEY, width, storage);
  return width;
}

export function officePanelWidthFromPointer(startWidth, startX, currentX) {
  return clampOfficePanelWidth(Number(startWidth) + Number(startX) - Number(currentX));
}

export function shouldShowOfficePanelResizer(viewportWidth) {
  return Number(viewportWidth) >= OFFICE_PANEL_RESIZER_MIN_VIEWPORT;
}
