import postcss from 'postcss';
import { liquidSecondaryTheme, secondarySurfaces } from './liquid-secondary-theme.mjs';

const transform = css => postcss([liquidSecondaryTheme()]).process(css, {
  from: '/webapp/src/css/catsco-ui-system.css',
});

it('limits liquid materials to the workspace and reuses light tokens inside secondary surfaces', async () => {
  const result = await transform(`
    :root { --cc-panel: #ffffff; --cc-text: #1f1f1f; --v3-panel: var(--cc-panel); }
    html[data-theme="liquid"] { --cc-panel: rgba(255,255,255,.86); }
    html[data-theme="liquid"] .oc-modal { backdrop-filter: blur(12px); }
    .oc-modal { background: var(--cc-panel); }
  `);
  const scopes = result.root.nodes.filter(node => node.type === 'atrule' && node.name === 'scope');
  expect(scopes).toHaveLength(2);
  expect(scopes[0].first.selector).toBe('html:where(:scope)[data-theme="liquid"]');
  expect(scopes[0].params).toContain(':scope[data-theme="liquid"]:not([data-liquid-variant="green"])');
  expect(secondarySurfaces).toEqual(expect.arrayContaining([
    '.oc-modal-overlay', '[role="dialog"]', '[role="menu"]', '.cc-skillhub-page', '.cc-apps-page',
  ]));
  const surfaceTokens = result.root.last;
  const values = Object.fromEntries(surfaceTokens.nodes.map(decl => [decl.prop, decl.value]));
  expect(values).toMatchObject({
    '--cc-panel': '#ffffff', '--cc-text': '#1f1f1f', '--v3-panel': 'var(--cc-panel)',
    '--cc-accent': '#5662d9', color: 'var(--cc-text)',
  });
  expect(surfaceTokens.selector).not.toContain('.v3-sidebar');
  expect(surfaceTokens.selector).not.toContain('.v3-composer-box');
});

it('preserves rules that already share their appearance between light and violet', async () => {
  const css = `html:not([data-theme="dark"]):not([data-theme="liquid"]) .cc-apps-page,
    html[data-theme="liquid"]:not([data-liquid-variant="green"]) .cc-apps-page { --card: #fcfcfc; }`;
  const result = await transform(css);
  expect(result.css).not.toContain('@scope');
  expect(result.css).toContain('--card: #fcfcfc');
});
