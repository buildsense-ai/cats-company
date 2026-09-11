import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { EncryptedSessionStore } from '../src/session_store.mjs';

test('session state is encrypted at rest and isolated by opaque binding', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'catsco-shimo-store-'));
  const store = new EncryptedSessionStore({ directory, key: Buffer.alloc(32, 7) });
  const bindingA = 'a'.repeat(32);
  const bindingB = 'b'.repeat(32);
  const storageState = { cookies: [{ name: 'auth', value: 'super-secret-cookie', domain: '.shimo.im', path: '/' }], origins: [] };
  try {
    store.save(bindingA, { storageState, accountHint: '账号 A', verifiedAt: '2026-09-10T10:00:00Z' });
    const files = fs.readdirSync(directory);
    assert.equal(files.length, 1);
    const raw = fs.readFileSync(path.join(directory, files[0]));
    assert.equal(raw.includes(Buffer.from('super-secret-cookie')), false);
    assert.deepEqual(store.load(bindingA).storageState, storageState);
    assert.equal(store.load(bindingA).accountHint, '账号 A');
    assert.equal(store.load(bindingB), null);
    assert.equal(store.delete(bindingB), false);
    assert.equal(store.delete(bindingA), true);
    assert.equal(store.load(bindingA), null);
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

test('encrypted state cannot be opened under another binding', () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'catsco-shimo-aad-'));
  const store = new EncryptedSessionStore({ directory, key: Buffer.alloc(32, 9) });
  const bindingA = '1'.repeat(32);
  const bindingB = '2'.repeat(32);
  try {
    store.save(bindingA, { storageState: { cookies: [], origins: [] } });
    fs.copyFileSync(store.pathFor(bindingA), store.pathFor(bindingB));
    assert.throws(() => store.load(bindingB));
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});
