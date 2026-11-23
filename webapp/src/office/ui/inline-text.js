// Editing text where it is drawn.
//
// The property panel already edits paragraphs, but rewriting a slide's wording in a side
// field means looking away from the place the words appear. These helpers convert between
// the paragraph model and one plain string so the canvas can host a small editor over the
// shape itself, then hand the result back in the same shape the writer expects.

function runsText(runs) {
  return (runs || []).map(run => (run?.break ? '\n' : (run?.text || ''))).join('');
}

/** The visible text of a paragraph list, one line per paragraph. */
export function plainTextOfParagraphs(paragraphs) {
  return (paragraphs || [])
    .map(paragraph => {
      const text = typeof paragraph?.text === 'string' ? paragraph.text : runsText(paragraph?.runs);
      return text.replace(/\n+$/, '');
    })
    .join('\n');
}

function firstRunStyle(paragraph) {
  const run = (paragraph?.runs || []).find(item => item && !item.break);
  if (!run) return {};
  const { text, break: isBreak, ...style } = run;
  return style;
}

/**
 * Turn an edited string back into paragraphs.
 *
 * Each line keeps the formatting of the paragraph it replaces, so bolding and font size
 * survive an edit that only changes wording. Lines past the original count clone the last
 * paragraph's formatting rather than inventing a default, and a line whose text did not
 * change is returned untouched: that keeps runs with mixed formatting intact when the user
 * edited a different line.
 */
export function paragraphsFromPlainText(paragraphs, value) {
  const original = paragraphs || [];
  const lines = String(value ?? '').replace(/\r\n?/g, '\n').split('\n');
  const fallback = original.length > 0 ? original[original.length - 1] : null;

  return lines.map((line, index) => {
    const base = original[index] || fallback || {};
    const before = typeof base.text === 'string' ? base.text : runsText(base.runs).replace(/\n+$/, '');
    if (before === line) return base;
    const style = firstRunStyle(base);
    return {
      ...base,
      text: line,
      runs: line === '' ? [] : [{ ...style, text: line }],
    };
  });
}