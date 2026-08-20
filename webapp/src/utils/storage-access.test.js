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
    const sessionDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'sessionStorage');
    Object.defineProperty(globalThis, 'localStorage', {
      configurable: true,
      get() {
        throw new Error('storage is blocked');
      },
    });
    Object.defineProperty(globalThis, 'sessionStorage', {
      configurable: true,
      get() {
        throw new Error('session storage is blocked');
      },
    });

    try {
      expect(readStorageValue('key')).toBeNull();
      expect(writeStorageValue('key', 'value')).toBe(false);
      expect(removeStorageValue('key')).toBe(false);
      expect(readStorageValue('key', 'sessionStorage')).toBeNull();
      expect(writeStorageValue('key', 'value', 'sessionStorage')).toBe(false);
      expect(removeStorageValue('key', 'sessionStorage')).toBe(false);
    } finally {
      Object.defineProperty(globalThis, 'localStorage', descriptor);
      Object.defineProperty(globalThis, 'sessionStorage', sessionDescriptor);
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
