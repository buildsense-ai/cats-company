import { afterEach, describe, expect, test, vi } from 'vitest';
import {
  readStorageValue,
  removeStorageValue,
  writeStorageValue,
} from './storage-access';

describe('storage access', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  test('converts storage getter failures into safe defaults', () => {
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
    Object.defineProperty(globalThis, 'localStorage', {
      configurable: true,
      get() {
        throw new Error('storage is blocked');
      },
    });

    try {
      expect(readStorageValue('key')).toBeNull();
      expect(writeStorageValue('key', 'value')).toBe(false);
      expect(removeStorageValue('key')).toBe(false);
    } finally {
      Object.defineProperty(globalThis, 'localStorage', descriptor);
    }
  });

  test('converts storage method failures into safe defaults', () => {
    const storage = {
      getItem: vi.fn(() => { throw new Error('read failed'); }),
      setItem: vi.fn(() => { throw new Error('write failed'); }),
      removeItem: vi.fn(() => { throw new Error('remove failed'); }),
    };

    expect(readStorageValue('key', storage)).toBeNull();
    expect(writeStorageValue('key', 'value', storage)).toBe(false);
    expect(removeStorageValue('key', storage)).toBe(false);
  });
});
