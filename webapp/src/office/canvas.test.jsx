// Phase-1 acceptance for the office canvas.
//
// The claim being tested is the whole design: a click on the rendered canvas resolves
// to exactly one XML node in exactly one part, with no search or inference. These tests
// fail if that link is ever reconstructed by guessing.

import React from 'react';
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react-dom/test-utils';
import { createRoot } from 'react-dom/client';
import { buildPptx, titleSlideXml, tableSlideXml } from './test-fixtures/build-pptx.js';
import { buildSampleDocx } from './test-fixtures/build-docx.js';
import SlideCanvas from './pptx/render.jsx';
import DocxCanvas from './docx/render.jsx';
import ElementList from './ui/ElementList.jsx';
import EditableCanvas from './ui/EditableCanvas.jsx';
import { applyDraftToSlide } from './ui/draft-render.js';
import { applyWordDraft } from './ui/word-draft-render.js';
import { parsePptx } from './pptx/parse.js';
import { parseDocx } from './docx/parse.js';
import { scanXml } from './ooxml/xml.js';

function nodeAtPath(root, path) {
  let current = root;
  for (const index of path) {
    if (!current) return null;
    current = current.children[index];
  }
  return current;
}

let container;
let root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

async function mountCanvas(model) {
  const slide = model.slides[0];
  const onSelect = vi.fn();
  await act(async () => {
    root.render(
      <SlideCanvas
        slide={slide}
        slideSize={model.slideSize}
        themeColors={model.themeColors}
        selectedId={null}
        hoveredId={null}
        onSelect={onSelect}
        onHover={() => {}}
        imageUrls={{}}
      />,
    );
  });
  return { slide, onSelect };
}

describe('pptx canvas', () => {
  it('renders uncommitted text and style drafts without changing the source model', async () => {
    const model = await parsePptx(await buildPptx());
    const slide = model.slides[0];
    const body = slide.shapes[1];
    const originalText = body.textBody.paragraphs[0].text;
    const draft = {
      paragraphs: [{ ...body.textBody.paragraphs[0], text: 'Draft text' }],
      styles: { 0: { bold: true, size: 23, color: '#123456', align: 'ctr' } },
    };

    const rendered = applyDraftToSlide(slide, body.elId, draft);
    const renderedBody = rendered.shapes[1];
    const paragraph = renderedBody.textBody.paragraphs[0];
    expect(paragraph).toMatchObject({ text: 'Draft text', align: 'ctr' });
    expect(paragraph.runs[0]).toMatchObject({
      text: 'Draft text', bold: true, size: 23, color: '#123456',
    });
    expect(slide.shapes[1].textBody.paragraphs[0].text).toBe(originalText);
  });

  it('renders one node per shape and tags each with its element id', async () => {
    const model = await parsePptx(await buildPptx());
    const { slide } = await mountCanvas(model);

    const nodes = container.querySelectorAll('[data-office-el]');
    expect(nodes).toHaveLength(slide.shapes.length);
    expect([...nodes].map(node => node.getAttribute('data-office-el')))
      .toEqual(slide.shapes.map(shape => shape.elId));
  });

  it('reports the clicked element id so the panel can follow it', async () => {
    const model = await parsePptx(await buildPptx());
    const { slide, onSelect } = await mountCanvas(model);

    const body = slide.shapes[1];
    const node = container.querySelector(`[data-office-el="${body.elId}"]`);
    expect(node).not.toBeNull();

    await act(async () => {
      node.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    });

    // The event is forwarded as a second argument so the container can read shiftKey for
    // multi-select (grouping needs two or more shapes). React hands over a synthetic
    // wrapper, so the assertion checks the modifier flag rather than the Event type.
    expect(onSelect).toHaveBeenCalled();
    expect(onSelect.mock.calls[0][0]).toBe(body.elId);
    expect(onSelect.mock.calls[0][1]).toHaveProperty('shiftKey');
  });

  it('resolves the recorded anchor path to the very node the shape came from', async () => {
    const model = await parsePptx(await buildPptx());
    const slide = model.slides[0];
    const partXml = await model.pkg.text(slide.part);
    const tree = scanXml(partXml);

    for (const shape of slide.shapes) {
      const node = nodeAtPath(tree, shape.anchor.path);
      expect(node, `anchor for ${shape.elId} must resolve`).not.toBeNull();
      // The node found through the anchor must be the same shape kind the renderer
      // drew, otherwise the id and the node have drifted apart.
      expect(['p:sp', 'p:pic', 'p:graphicFrame', 'p:grpSp', 'p:cxnSp'])
        .toContain(node.name);
    }
  });

  it('renders table text and marks inherited shapes', async () => {
    const model = await parsePptx(await buildPptx({ slides: [tableSlideXml({ rows: 2, cols: 2 })] }));
    await mountCanvas(model);
    const table = container.querySelector('[data-office-kind="table"]');
    expect(table).not.toBeNull();
    expect(table.textContent).toContain('值');

    const inheritedModel = await parsePptx(await buildPptx({ slides: [titleSlideXml()] }));
    await act(async () => root.unmount());
    root = createRoot(container);
    await mountCanvas(inheritedModel);
    const title = container.querySelector('[data-office-kind="text"][data-office-inherited="1"]');
    expect(title).not.toBeNull();
  });

  it('draws grouped children in the group local space and applies their drafts there', async () => {
    const child = {
      elId: 'slides-slide1#0/0',
      kind: 'text',
      label: 'child',
      box: { x: 1000, y: 500, cx: 2000, cy: 500 },
      textBody: { paragraphs: [{ runs: [{ text: 'child' }] }] },
    };
    const group = {
      elId: 'slides-slide1#0',
      kind: 'group',
      label: 'group',
      box: { x: 1000, y: 500, cx: 2000, cy: 1000 },
      childOffset: { x: 1000, y: 500 },
      childExtent: { x: 2000, y: 1000 },
      children: [child],
    };
    const slide = { index: 0, shapes: [group] };
    const slideSize = { cx: 4000, cy: 2000 };

    await act(async () => {
      root.render(
        <EditableCanvas slide={slide} slideSize={slideSize} selectedId={null}
          hoveredId={null} imageUrls={{}} editedBoxes={{}} onSelect={() => {}}
          onHover={() => {}} onBoxPreview={() => {}} onBoxCommit={() => {}} />,
      );
    });
    let node = container.querySelector('[data-office-el="slides-slide1#0/0"]');
    expect(node.style.left).toBe('0%');
    expect(node.style.top).toBe('0%');
    expect(node.style.width).toBe('100%');
    expect(node.style.height).toBe('50%');

    // A committed child drag is stored relative to the group. It must be visible before save.
    await act(async () => {
      root.render(
        <EditableCanvas slide={slide} slideSize={slideSize} selectedId={child.elId}
          hoveredId={null} imageUrls={{}}
          editedBoxes={{ [child.elId]: { x: 1500, y: 500, cx: 2000, cy: 500 } }}
          onSelect={() => {}} onHover={() => {}} onBoxPreview={() => {}} onBoxCommit={() => {}} />,
      );
    });
    node = container.querySelector('[data-office-el="slides-slide1#0/0"]');
    expect(node.style.left).toBe('25%');
    expect(node.style.top).toBe('0%');
    expect(node.style.width).toBe('100%');
  });
});

describe('element list', () => {
  it('keeps one collapsed row per shape and expands groups', async () => {
    const model = await parsePptx(await buildPptx());
    const slide = model.slides[0];
    const onSelect = vi.fn();

    await act(async () => {
      root.render(
        <ElementList shapes={slide.shapes} selectedId={null} onSelect={onSelect}
          onHover={() => {}} slideIndex={0} slideCount={1} />,
      );
    });

    const rows = container.querySelectorAll('[data-office-el-row]');
    expect(rows).toHaveLength(slide.shapes.length);

    // A row must carry the element id so a click can be routed without a lookup table.
    // The click is also passed through, because shift-click means "add to selection".
    await act(async () => {
      rows[0].dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(onSelect).toHaveBeenCalled();
    expect(onSelect.mock.calls[0][0]).toBe(slide.shapes[0].elId);
  });

  it('routes a shift-click to the multi-select handler instead of replacing the selection', async () => {
    const model = await parsePptx(await buildPptx());
    const slide = model.slides[0];
    const onSelect = vi.fn();
    const onSelectMany = vi.fn();

    await act(async () => {
      root.render(
        <ElementList shapes={slide.shapes} selectedId={null} onSelect={onSelect}
          onSelectMany={onSelectMany} onHover={() => {}} slideIndex={0} slideCount={1} />,
      );
    });

    const rows = container.querySelectorAll('[data-office-el-row]');
    await act(async () => {
      rows[1].dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }));
    });

    // Grouping needs two or more shapes, and shift is how the user expresses that. If this
    // went to onSelect the previous pick would be replaced and grouping could never fire.
    expect(onSelectMany).toHaveBeenCalledWith(slide.shapes[1].elId);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it('badges an inherited element so the user knows it came from the layout', async () => {
    const model = await parsePptx(await buildPptx());
    const slide = model.slides[0];
    await act(async () => {
      root.render(
        <ElementList shapes={slide.shapes} selectedId={null} onSelect={() => {}}
          onHover={() => {}} slideIndex={0} slideCount={1} />,
      );
    });
    const badge = container.querySelector('.office-el-badge');
    expect(badge).not.toBeNull();
    expect(badge.textContent).toBe('继承');
  });
});

describe('docx canvas', () => {
  it('renders an uncommitted paragraph draft without changing the source model', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const block = model.blocks[1];
    const originalText = block.text;
    const rendered = applyWordDraft(model, block.id, {
      text: 'Draft paragraph',
      style: { bold: true, size: 23, color: '#123456', underline: true },
      props: { align: 'center', indLeftCm: 1.5, indFirstCm: 0.5 },
    });
    const paragraph = rendered.blocks[1];

    expect(paragraph).toMatchObject({
      text: 'Draft paragraph', align: 'center', indLeftTwips: 851, indFirstTwips: 284,
    });
    expect(paragraph.runs[0]).toMatchObject({
      text: 'Draft paragraph', bold: true, size: 23, color: '#123456', underline: true,
    });
    expect(model.blocks[1].text).toBe(originalText);

    await act(async () => {
      root.render(
        <DocxCanvas model={rendered} selectedId={block.id} hoveredId={null}
          onSelect={() => {}} onHover={() => {}} />,
      );
    });
    expect(container.textContent).toContain('Draft paragraph');
  });

  it('renders a table-cell draft in only the selected cell', async () => {
    const model = await parseDocx(await buildSampleDocx());
    const table = model.blocks.find(block => block.kind === 'table');
    const cell = table.cells[0];
    const sibling = table.cells[1];
    const rendered = applyWordDraft(model, table.id, {
      cell: { row: cell.row, col: cell.col, text: 'CELL-DRAFT' },
    });
    const changed = rendered.blocks.find(block => block.id === table.id);

    expect(changed.cells[0].text).toBe('CELL-DRAFT');
    expect(changed.cells[1].text).toBe(sibling.text);
    expect(table.cells[0].text).not.toBe('CELL-DRAFT');
  });
});
