// Merge uncommitted PPT text and style edits into the slide model used for rendering.
//
// Drafts are keyed by `slideIndex:elId`, not by the current selection. A page or element
// switch therefore never moves one element's text onto another, and the canvas can render
// every pending edit on the visible page in one pass.

function runsText(runs) {
  return (runs || []).map(run => (run.break ? '\n' : (run.text || ''))).join('');
}

export function officeDraftKey(slideIndex, elId) {
  return `${Number(slideIndex)}:${elId}`;
}

export function parseOfficeDraftKey(key) {
  const raw = String(key || '');
  const split = raw.indexOf(':');
  if (split <= 0 || split === raw.length - 1) return null;
  const slideIndex = Number(raw.slice(0, split));
  if (!Number.isInteger(slideIndex) || slideIndex < 0) return null;
  return { slideIndex, elId: raw.slice(split + 1) };
}

function renderParagraph(paragraph, patch) {
  const merged = patch ? { ...paragraph, ...patch } : { ...paragraph };
  const text = String(merged.text ?? '');
  const runs = merged.runs || [];
  if (runsText(runs) === text) {
    if (!patch) return merged;
    // A style change is written to every run, matching applyStyleEdits in the writer.
    merged.runs = runs.map(run => (run.break ? run : { ...run, ...patch }));
    return merged;
  }

  // Once text changes, the writer re-emits the paragraph using the first run as its
  // formatting template. Mirror that here so the canvas does not show stale run text.
  const template = runs.find(run => !run.break) || {};
  const styled = { ...template, ...patch };
  const lines = text.split('\n');
  merged.runs = [];
  lines.forEach((line, index) => {
    if (index > 0) merged.runs.push({ break: true });
    merged.runs.push({ ...styled, text: line });
  });
  return merged;
}

function paragraphsWithStyles(paragraphs, styles) {
  return (paragraphs || []).map((paragraph, index) => {
    return renderParagraph(paragraph, styles?.[index]);
  });
}

function patchShapes(shapes, slideIndex, draft) {
  return (shapes || []).map((shape) => {
    const key = officeDraftKey(slideIndex, shape.elId);
    const paragraphsDraft = draft?.texts?.[key];
    const styleDraft = draft?.styles?.[key];
    let next = shape;
    if (paragraphsDraft || styleDraft) {
      const paragraphs = paragraphsDraft || shape.textBody?.paragraphs;
      if (paragraphs) {
        next = {
          ...shape,
          textBody: {
            ...(shape.textBody || {}),
            paragraphs: paragraphsWithStyles(paragraphs, styleDraft),
          },
        };
      }
    }
    if (shape.children?.length) {
      next = { ...next, children: patchShapes(shape.children, slideIndex, draft) };
    }
    return next;
  });
}

export function applyDraftToSlide(slide, slideIndex, draft) {
  if (!slide || !Number.isInteger(Number(slideIndex))) return slide;
  const hasText = Object.keys(draft?.texts || {}).length > 0;
  const hasStyles = Object.keys(draft?.styles || {}).length > 0;
  if (!hasText && !hasStyles) return slide;
  return { ...slide, shapes: patchShapes(slide.shapes, Number(slideIndex), draft) };
}
