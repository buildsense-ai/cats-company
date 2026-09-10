import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

const FORMAT_VERSION = 1;

function decodeKey(value) {
  const raw = String(value || '').trim();
  let key;
  if (/^[0-9a-f]{64}$/i.test(raw)) key = Buffer.from(raw, 'hex');
  else {
    try { key = Buffer.from(raw, 'base64'); } catch { key = Buffer.alloc(0); }
  }
  if (key.length !== 32) throw new Error('SHIMO_WORKER_SESSION_KEY must decode to exactly 32 bytes');
  return key;
}

function fileName(binding) {
  return `${crypto.createHash('sha256').update(binding).digest('hex')}.session`;
}

export class EncryptedSessionStore {
  constructor({ directory, key }) {
    this.directory = path.resolve(directory);
    this.key = Buffer.isBuffer(key) ? Buffer.from(key) : decodeKey(key);
    if (this.key.length !== 32) throw new Error('session encryption key must contain exactly 32 bytes');
    fs.mkdirSync(this.directory, { recursive: true, mode: 0o700 });
  }

  pathFor(binding) {
    assertBinding(binding);
    return path.join(this.directory, fileName(binding));
  }

  save(binding, record) {
    assertBinding(binding);
    const plaintext = Buffer.from(JSON.stringify({
      formatVersion: FORMAT_VERSION,
      storageState: record.storageState,
      accountHint: String(record.accountHint || ''),
      verifiedAt: String(record.verifiedAt || new Date().toISOString()),
    }), 'utf8');
    const nonce = crypto.randomBytes(12);
    const cipher = crypto.createCipheriv('aes-256-gcm', this.key, nonce);
    cipher.setAAD(Buffer.from(binding, 'utf8'));
    const ciphertext = Buffer.concat([cipher.update(plaintext), cipher.final()]);
    const envelope = Buffer.concat([Buffer.from([FORMAT_VERSION]), nonce, cipher.getAuthTag(), ciphertext]);
    const destination = this.pathFor(binding);
    const temporary = `${destination}.${process.pid}.${crypto.randomBytes(6).toString('hex')}.tmp`;
    fs.writeFileSync(temporary, envelope, { mode: 0o600, flag: 'wx' });
    fs.renameSync(temporary, destination);
  }

  load(binding) {
    const source = this.pathFor(binding);
    let envelope;
    try { envelope = fs.readFileSync(source); } catch (error) {
      if (error?.code === 'ENOENT') return null;
      throw error;
    }
    if (envelope.length < 30 || envelope[0] !== FORMAT_VERSION) throw new Error('unsupported encrypted session format');
    const nonce = envelope.subarray(1, 13);
    const tag = envelope.subarray(13, 29);
    const ciphertext = envelope.subarray(29);
    const decipher = crypto.createDecipheriv('aes-256-gcm', this.key, nonce);
    decipher.setAAD(Buffer.from(binding, 'utf8'));
    decipher.setAuthTag(tag);
    const plaintext = Buffer.concat([decipher.update(ciphertext), decipher.final()]);
    const record = JSON.parse(plaintext.toString('utf8'));
    if (record.formatVersion !== FORMAT_VERSION || !record.storageState || typeof record.storageState !== 'object') {
      throw new Error('invalid decrypted session record');
    }
    return record;
  }

  delete(binding) {
    try {
      fs.unlinkSync(this.pathFor(binding));
      return true;
    } catch (error) {
      if (error?.code === 'ENOENT') return false;
      throw error;
    }
  }
}

export function sessionKeyFromEnv(value) {
  return decodeKey(value);
}

export function assertBinding(value) {
  if (!/^[0-9a-f]{32,128}$/i.test(String(value || ''))) throw new Error('invalid connection binding');
}
