import { describe, it, expect } from 'vitest';
import { normalizeHandoff, validateParameters } from './app-handoff';

const request = { request_id: 'boundary-request-1', topic_id: 'p2p_7_9', action: 'initialize', expires_at: Date.now() + 60000 };
describe('handoff exact parameter boundaries', () => {
  it('accepts 512 Unicode code points and rejects 513', () => {
    expect(normalizeHandoff({ ...request, parameters: { label: '😀'.repeat(512) } })).not.toBeNull();
    expect(normalizeHandoff({ ...request, parameters: { label: '😀'.repeat(513) } })).toBeNull();
  });
  it('accepts exactly 4096 serialized UTF-8 bytes and rejects 4097', () => {
    const base = { a: '界'.repeat(512), b: '界'.repeat(512), c: 'x'.repeat(512) };
    const remaining = 4096 - new TextEncoder().encode(JSON.stringify({ ...base, d: '' })).length;
    const accepted = { ...base, d: 'x'.repeat(remaining) };
    const rejected = { ...base, d: 'x'.repeat(remaining + 1) };
    expect(remaining).toBeGreaterThanOrEqual(0);
    expect(remaining + 1).toBeLessThanOrEqual(512);
    expect(new TextEncoder().encode(JSON.stringify(accepted)).length).toBe(4096);
    expect(new TextEncoder().encode(JSON.stringify(rejected)).length).toBe(4097);
    expect(normalizeHandoff({ ...request, parameters: accepted })).not.toBeNull();
    expect(normalizeHandoff({ ...request, parameters: rejected })).toBeNull();
  });
  it('enforces schema maxLength at the code-point boundary', () => {
    const schema = { label: { type: 'string', maxLength: 300 } };
    expect(validateParameters({ label: '😀'.repeat(300) }, schema)).toBe(true);
    expect(validateParameters({ label: '😀'.repeat(301) }, schema)).toBe(false);
  });
});
