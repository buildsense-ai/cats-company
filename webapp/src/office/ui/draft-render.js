// Merge uncommitted text and style edits into the slide model used for rendering.
//
// Geometry already had a draft overlay in EditableCanvas. Text and style did not, so
// typing in the property panel left the canvas showing the previous version until save.
// Keeping the merge pure makes it testable without mounting the whole preview shell.

function runsText(runs) {
  return (runs || []).map(run => (run.break ? '\n' : (run.text || ''))).join('');
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

function patchShapes(shapes, selectedId, draft) {
  return (shapes || []).map((shape) => {
    let next = shape;
    if (shape.elId === selectedId) {
      const paragraphs = draft?.paragraphs || shape.textBody?.paragraphs;
      if (paragraphs) {
        next = {
          ...shape,
          textBody: {
            ...(shape.textBody || {}),
            paragraphs: paragraphsWithStyles(paragraphs, draft?.styles),
          },
        };
      }
    }
    if (shape.children?.length) {
      next = { ...next, children: patchShapes(shape.children, selectedId, draft) };
    }
    return next;
  });
}

export function applyDraftToSlide(slide, selectedId, draft) {
  if (!slide || !selectedId || (!draft?.paragraphs && !draft?.styles)) return slide;
  return { ...slide, shapes: patchShapes(slide.shapes, selectedId, draft) };
}
