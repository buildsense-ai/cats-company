import { describe, expect, it } from 'vitest';
import { paragraphsFromPlainText, plainTextOfParagraphs } from './ui/inline-text.js';

const PARAGRAPHS = [
  { text: 'Title', align: 'ctr', runs: [{ text: 'Title', size: 3300, bold: true }] },
  { text: 'Sub', runs: [{ text: 'Sub', size: 1800, color: '#C86400' }] },
];

describe('inline paragraph text', () => {
  it('joins paragraphs with newlines for editing', () => {
    expect(plainTextOfParagraphs(PARAGRAPHS)).toBe('Title\nSub');
  });

  it('falls back to the runs when a paragraph has no text field', () => {
    const paragraphs = [{ runs: [{ text: 'ab' }, { break: true }, { text: 'cd' }] }];
    expect(plainTextOfParagraphs(paragraphs)).toBe('ab\ncd');
  });

  it('keeps the formatting of the paragraph it replaces', () => {
    const next = paragraphsFromPlainText(PARAGRAPHS, 'Renamed\nSub');
    expect(next[0].runs[0]).toMatchObject({ text: 'Renamed', size: 3300, bold: true });
    expect(next[0].align).toBe('ctr');
  });

  it('leaves an unchanged paragraph object untouched', () => {
    const next = paragraphsFromPlainText(PARAGRAPHS, 'Title\nChanged');
    expect(next[0]).toBe(PARAGRAPHS[0]);
    expect(next[1].runs[0]).toMatchObject({ text: 'Changed', size: 1800, color: '#C86400' });
  });

  it('clones the last paragraph formatting for added lines', () => {
    const next = paragraphsFromPlainText(PARAGRAPHS, 'Title\nSub\nExtra');
    expect(next).toHaveLength(3);
    expect(next[2].runs[0]).toMatchObject({ text: 'Extra', size: 1800 });
  });

  it('produces an empty run list for a cleared line', () => {
    const next = paragraphsFromPlainText(PARAGRAPHS, 'Title\n');
    expect(next[1].runs).toEqual([]);
    expect(next[1].text).toBe('');
  });

  it('accepts Windows line endings from a pasted value', () => {
    const next = paragraphsFromPlainText(PARAGRAPHS, 'A\r\nB');
    expect(next.map(paragraph => paragraph.text)).toEqual(['A', 'B']);
  });
});