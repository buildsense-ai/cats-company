import { existsSync, readFileSync, statSync } from 'node:fs';
import { resolve } from 'node:path';

const css = readFileSync(resolve(process.cwd(), 'src/css/catsco-ui-system.css'), 'utf8')
  .replace(/\r\n?/g, '\n');
const openchatCss = readFileSync(resolve(process.cwd(), 'src/css/openchat-theme.css'), 'utf8')
  .replace(/\r\n?/g, '\n');
const searchOverlayCss = readFileSync(resolve(process.cwd(), 'src/css/search-overlay.css'), 'utf8')
  .replace(/\r\n?/g, '\n');
const liquidGreenCss = readFileSync(resolve(process.cwd(), 'src/css/catsco-liquid-green.css'), 'utf8')
  .replace(/\r\n?/g, '\n');
const brandAssetPath = resolve(process.cwd(), 'public/catsco-brand-mark.webp');

const ruleIn = (source, selector) => source.match(
  new RegExp(`(?:^|\\r?\\n)${selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*\\{[^}]*\\}`),
)?.[0] || '';
const ruleFor = (selector) => ruleIn(css, selector);

const propertyValue = (source, selector, property) => (selector ? ruleIn(source, selector) : source).match(
  new RegExp(`${property}:\\s*([^;]+);`),
)?.[1]?.trim();

const hexToken = (rule, token) => rule.match(
  new RegExp(`${token}:\\s*(#[0-9a-fA-F]{6});`),
)?.[1];

const parseCssColor = (value) => {
  const normalized = String(value || '').trim().toLowerCase();
  if (normalized.startsWith('#')) {
    const hex = normalized.slice(1);
    const expanded = hex.length === 3
      ? hex.split('').map((channel) => `${channel}${channel}`).join('')
      : hex.slice(0, 6);
    if (!/^[0-9a-f]{6}$/.test(expanded)) return null;
    return {
      r: parseInt(expanded.slice(0, 2), 16),
      g: parseInt(expanded.slice(2, 4), 16),
      b: parseInt(expanded.slice(4, 6), 16),
      a: hex.length === 8 ? parseInt(hex.slice(6, 8), 16) / 255 : 1,
    };
  }
  const match = normalized.match(/^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)(?:\s*,\s*([\d.]+))?\s*\)$/);
  if (!match) return null;
  return {
    r: Number(match[1]),
    g: Number(match[2]),
    b: Number(match[3]),
    a: match[4] === undefined ? 1 : Number(match[4]),
  };
};

const compositeColor = (foreground, background) => {
  const alpha = Math.min(1, Math.max(0, foreground.a));
  const remaining = 1 - alpha;
  return {
    r: (foreground.r * alpha) + (background.r * remaining),
    g: (foreground.g * alpha) + (background.g * remaining),
    b: (foreground.b * alpha) + (background.b * remaining),
    a: 1,
  };
};

const relativeLuminance = (color) => {
  const channels = [color.r, color.g, color.b].map((channel) => channel / 255);
  const [red, green, blue] = channels.map((channel) => (
    channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4
  ));
  return (0.2126 * red) + (0.7152 * green) + (0.0722 * blue);
};

const contrastRatio = (foreground, background) => {
  const foregroundColor = typeof foreground === 'string' ? parseCssColor(foreground) : foreground;
  const backgroundColor = typeof background === 'string' ? parseCssColor(background) : background;
  if (!foregroundColor || !backgroundColor) return 0;
  const [lighter, darker] = [relativeLuminance(foregroundColor), relativeLuminance(backgroundColor)].sort((a, b) => b - a);
  return (lighter + 0.05) / (darker + 0.05);
};

const cssColors = (value) => String(value || '').match(
  /#[0-9a-f]{3,8}|rgba?\([^)]*\)/gi,
) || [];

const readLosslessWebpDimensions = (buffer) => {
  if (
    buffer.subarray(0, 4).toString('ascii') !== 'RIFF'
    || buffer.subarray(8, 12).toString('ascii') !== 'WEBP'
    || buffer.subarray(12, 16).toString('ascii') !== 'VP8L'
    || buffer[20] !== 0x2f
  ) {
    throw new Error('Expected a lossless WebP brand asset');
  }
  const sizeBits = buffer.readUInt32LE(21);
  return {
    width: (sizeBits & 0x3fff) + 1,
    height: ((sizeBits >>> 14) & 0x3fff) + 1,
  };
};

describe('CatsCo shell styling', () => {
  it('keeps global search selection and focus surfaces chromatically neutral', () => {
    const tabsRule = ruleIn(searchOverlayCss, '.cc-global-search-tabs');
    const activeScopeRule = ruleIn(searchOverlayCss, '.cc-global-search-tabs button.active');
    const placeholderRule = ruleIn(searchOverlayCss, '.cc-global-search-field input::placeholder');

    expect(tabsRule).toContain('gap: 4px;');
    expect(activeScopeRule).toContain('border-color: color-mix(in srgb, #a3a3a3 28%, transparent);');
    expect(activeScopeRule).toContain('background: color-mix(in srgb, #808080 22%, transparent);');
    expect(activeScopeRule).toContain('color: var(--cc-text);');
    expect(placeholderRule).toContain('color: var(--cc-placeholder);');
  });

  it('keeps neutral borders quiet while focus remains a distinct semantic state', () => {
    expect(ruleFor(':root')).toContain('--cc-border: #ececef;');
    expect(ruleFor(':root')).toContain('--cc-border-strong: #dedee2;');
    expect(ruleFor(':root')).toContain('--cc-control-surface: #f2f2f4;');
    expect(ruleFor(':root')).toContain('--cc-input-surface: var(--cc-code);');
    expect(ruleFor(':root')).toContain('--cc-helper-text: color-mix(in srgb, var(--cc-text) 58%, transparent);');
    expect(ruleFor(':root')).toContain('--cc-placeholder: color-mix(in srgb, var(--cc-text) 46%, transparent);');
    expect(ruleFor(':root')).toContain('--cc-focus-ring: #147c65;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-border: #2d2d2f;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-border-strong: #3a3a3d;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-control-surface: #2c2c2f;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-focus-ring: #69d7b7;');
    expect(ruleFor('html[data-theme="liquid"]'))
      .toContain('--cc-border: rgba(73, 86, 168, 0.1);');
    expect(ruleFor('html[data-theme="liquid"]'))
      .toContain('--cc-border-strong: rgba(86, 98, 217, 0.22);');
    expect(ruleFor('html[data-theme="liquid"]'))
      .toContain('--cc-control-surface: rgba(224, 230, 255, 0.68);');
    expect(ruleFor('html[data-theme="liquid"]'))
      .toContain('--cc-input-surface: rgba(255, 255, 255, 0.78);');
    expect(ruleFor('html[data-theme="liquid"]')).toContain('--cc-focus-ring: #5662d9;');
  });

  it('uses the optimized formal brand asset wherever the shared mark is rendered', () => {
    const brandRule = ruleFor('.catsco-brand-mark');

    expect(existsSync(brandAssetPath)).toBe(true);
    expect(existsSync(resolve(process.cwd(), 'public/catsco-brand-asasda.png'))).toBe(false);
    expect(statSync(brandAssetPath).size).toBeLessThan(25_000);
    expect(readLosslessWebpDimensions(readFileSync(brandAssetPath))).toEqual({ width: 256, height: 96 });
    expect(brandRule).toContain('width: 48px;');
    expect(brandRule).toContain("url('/catsco-brand-mark.webp')");
    expect(brandRule).toContain('background');
    expect(brandRule).toContain('contain no-repeat');
    expect(brandRule).toContain('-webkit-mask: none;');
    expect(brandRule).toContain('mask: none;');
    expect(ruleFor('.v3-brand-title')).toContain('gap: 9px;');
    expect(ruleFor('.v3-brand-title')).toContain('font-size: 16px;');
    expect(ruleFor('.v3-brand-title')).toContain('font-weight: var(--cc-font-weight-bold);');
    expect(ruleFor('.v3-sidebar-header > .v3-brand-title')).toContain('gap: 4px;');
    expect(ruleFor('.v3-sidebar-header > .v3-brand-title')).toContain('font-size: 19px;');
    expect(ruleFor('.v3-sidebar-header > .v3-brand-title')).toContain('line-height: 20.9px;');
    expect(ruleFor('.v3-sidebar-header > .v3-brand-title'))
      .toContain('font-weight: var(--cc-font-weight-brand);');
    expect(ruleFor('.v3-sidebar-header > .v3-brand-title .catsco-brand-mark'))
      .toContain('width: 45.6px;');
    expect(ruleFor('.v3-sidebar-header > .v3-brand-title .catsco-brand-mark'))
      .toContain('height: 20.9px;');
    expect(ruleFor(':root')).toContain('--cc-brand-text-start: #29bc95;');
    expect(ruleFor(':root')).toContain('--cc-brand-text-end: #29bc95;');
    expect(ruleFor(':root')).toContain('--cc-accent: #29bc95;');
    expect(ruleFor(':root')).toContain('--oc-tab-active: #29bc95;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-brand-text-start: #29bc95;');
    expect(ruleFor('html[data-theme="liquid"]')).toContain('--cc-brand-text-end: #7548cf;');
    expect(ruleFor('.v3-sidebar-header .catsco-brand-name')).toContain(
      'var(--cc-brand-text-start) 0%',
    );
    expect(ruleFor('.v3-sidebar-header .catsco-brand-name')).toContain('letter-spacing: 0.01em;');
    expect(ruleFor('.v3-sidebar-header .catsco-brand-name'))
      .toContain('text-shadow: none;');
    expect(css).toMatch(
      /@media \(forced-colors: active\)[\s\S]*?\.v3-sidebar-header \.catsco-brand-name\s*\{[^}]*-webkit-text-fill-color: currentColor;/,
    );
    expect(ruleFor('.v3-sidebar.collapsed .v3-sidebar-collapse-btn .catsco-brand-mark'))
      .toContain('width: 34px;');
    const collapsedButtonRule = ruleFor(
      '.v3-sidebar.collapsed .v3-sidebar-collapse-btn,\n'
      + '.v3-sidebar.collapsed .v3-sidebar-collapse-btn:hover,\n'
      + '.v3-sidebar.collapsed .v3-sidebar-collapse-btn:active',
    );
    expect(collapsedButtonRule).toContain('background: transparent;');
    expect(collapsedButtonRule).toContain('box-shadow: none;');
    expect(ruleFor('html[data-theme="liquid"] .catsco-brand-mark'))
      .toContain('filter: hue-rotate(68deg) saturate(1.05) brightness(0.78);');
  });

  it('keeps sidebar chrome fixed while the navigation list owns overflow', () => {
    const headerRule = ruleFor('.v3-sidebar-header');
    const collapsedHeaderRule = ruleFor('.v3-sidebar.collapsed .v3-sidebar-header');
    const collapseButtonRule = ruleFor('.v3-sidebar-collapse-btn');
    const sidebarRule = ruleFor('.v3-sidebar');
    const toolsRule = ruleFor('.cc-sidebar-tools');
    const listRule = ruleFor('.v3-chat-list');
    const footerRule = ruleFor('.v3-profile-footer');

    expect(headerRule).toContain('height: 48px;');
    expect(headerRule).toContain('min-height: 48px;');
    expect(headerRule).toContain('flex: 0 0 48px;');
    expect(headerRule).toContain('padding: 0 8px 0 12px;');
    expect(headerRule).toContain('border-bottom: 0;');
    expect(collapsedHeaderRule).toContain('height: 44.8px;');
    expect(collapsedHeaderRule).toContain('flex-basis: 44.8px;');
    expect(sidebarRule).toContain('font-family: var(--cc-font-sans);');
    expect(collapseButtonRule).toContain('width: 30.4px;');
    expect(collapseButtonRule).toContain('height: 30.4px;');
    expect(ruleFor('.v3-sidebar-collapse-btn > svg')).toContain('width: 19px;');
    expect(ruleFor('.v3-sidebar-collapse-btn > svg')).toContain('height: 19px;');
    expect(toolsRule).toContain('flex: 0 0 auto;');
    expect(toolsRule).toContain('padding: 0 8px 7px;');
    expect(listRule).toContain('min-height: 0;');
    expect(listRule).toContain('flex: 1 1 auto;');
    expect(listRule).toContain('overflow-y: auto;');
    expect(footerRule).toContain('flex: 0 0 auto;');
    expect(ruleFor('.v3-chat-item')).toContain('font-weight: 400;');
  });

  it('hands vertical wheel scrolling from expanded tool output back to the chat timeline', () => {
    const scrollChainRules = [
      ruleFor('.v3-working-steps'),
      ruleFor('.v3-wpi-code-block.result pre'),
      ruleIn(openchatCss, '.v3-working-details-inline .v3-working-steps'),
      ruleIn(openchatCss, '.v3-wpi-code-block pre'),
    ];

    scrollChainRules.forEach((rule) => {
      expect(rule).toContain('overscroll-behavior-x: contain;');
      expect(rule).toContain('overscroll-behavior-y: auto;');
      expect(rule).not.toContain('overscroll-behavior: contain;');
    });
  });

  it('uses tokenized reduced-motion-safe feedback for inline video controls', () => {
    const closeButtonRule = ruleFor('.oc-rich-media-preview-close');

    expect(ruleFor(':root')).toContain('--cc-media-black: oklch(0.19 0.01 165);');
    expect(ruleFor('.v3-message .oc-rich-video-play'))
      .toContain('cubic-bezier(0.16, 1, 0.3, 1)');
    expect(closeButtonRule)
      .toContain('cubic-bezier(0.16, 1, 0.3, 1)');
    expect(closeButtonRule).toContain('width: 44px;');
    expect(closeButtonRule).toContain('height: 44px;');
    expect(ruleFor('.oc-rich-video-preview-close:focus-visible'))
      .toContain('outline: 2px solid var(--cc-focus-ring);');
    expect(css).toMatch(
      /@media \(prefers-reduced-motion: reduce\)[\s\S]*?\.v3-message \.oc-rich-video-play,[\s\S]*?transition: none;/,
    );
  });

  it('crops only ultra-wide video thumbnails to a compact, centered viewport while previews retain the source', () => {
    const videoContainerRule = ruleFor('.v3-message .oc-rich-video');
    const croppedVideoContainerRule = ruleFor('.v3-message .oc-rich-video.is-ultrawide');
    const triggerRule = ruleFor('.v3-message .oc-rich-video-trigger');
    const croppedTriggerRule = ruleFor('.v3-message .oc-rich-video.is-ultrawide .oc-rich-video-trigger');
    const thumbnailRule = ruleFor('.v3-message .oc-rich-video-thumb');
    const croppedThumbnailRule = ruleFor('.v3-message .oc-rich-video.is-ultrawide .oc-rich-video-thumb');

    expect(videoContainerRule).toContain('width: fit-content;');
    expect(croppedVideoContainerRule).toContain('width: min(240px, 100%);');
    expect(triggerRule).toContain('width: fit-content;');
    expect(croppedTriggerRule).toContain('width: 100%;');
    expect(croppedTriggerRule).toContain('aspect-ratio: 16 / 9;');
    expect(thumbnailRule).toContain('object-fit: contain;');
    expect(croppedThumbnailRule).toContain('width: 100%;');
    expect(croppedThumbnailRule).toContain('height: 100%;');
    expect(croppedThumbnailRule).toContain('object-fit: cover;');
    expect(croppedThumbnailRule).toContain('object-position: center;');
    expect(ruleFor('.oc-rich-video-preview .oc-rich-video-player')).toContain('object-fit: contain;');
  });

  it('keeps inline audio controls compact, focusable, and reduced-motion safe', () => {
    const audioRule = ruleFor('.v3-message .oc-rich-audio');
    const downloadRule = ruleFor('.v3-message .oc-rich-audio-download');

    expect(audioRule).toContain('grid-template-columns: minmax(0, 1fr) auto;');
    expect(audioRule).toContain('border: 1px solid var(--cc-border);');
    expect(downloadRule).toContain('min-height: 34px;');
    expect(downloadRule).toContain('text-decoration: none;');
    expect(ruleFor('.v3-message .oc-rich-audio-download:focus-visible'))
      .toContain('outline: 2px solid var(--cc-focus-ring);');
    expect(css).toMatch(
      /@media \(prefers-reduced-motion: reduce\)[\s\S]*?\.v3-message \.oc-rich-audio-download[\s\S]*?transition: none;/,
    );
  });

  it('keeps expanded chat images fully visible in the viewport', () => {
    const previewRule = ruleIn(openchatCss, '.oc-rich-image-preview');
    const imageRule = ruleIn(openchatCss, '.oc-rich-image-preview-media');

    expect(previewRule).toContain('padding: 16px;');
    expect(imageRule).toContain('width: auto;');
    expect(imageRule).toContain('height: auto;');
    expect(imageRule).toContain('max-width: min(90vw, calc(100vw - 32px));');
    expect(imageRule).toContain('max-height: calc(100dvh - 32px);');
    expect(imageRule).toContain('object-fit: contain;');
    expect(ruleIn(openchatCss, '.oc-rich-image-trigger')).toContain('cursor: zoom-in;');
    expect(ruleIn(openchatCss, '.oc-rich-image-trigger:focus-visible'))
      .toContain('outline: 2px solid var(--v3-primary);');
  });

  it('standardizes scrollbar tiers, states, and browser-specific rendering', () => {
    const rootRule = ruleFor(':root');
    const documentRule = ruleFor('html,\nhtml *');
    const timelineRule = ruleFor('.v3-timeline');
    const inlineRule = ruleFor('.v3-working-steps,\n.v3-wpi-code-block pre');
    const listRule = ruleFor('.v3-chat-list');
    const interactiveListRule = ruleFor('.v3-chat-list:hover,\n.v3-chat-list:focus-within');
    const scrollbarRule = ruleFor('.v3-chat-list::-webkit-scrollbar');
    const trackRule = ruleFor('.v3-chat-list::-webkit-scrollbar-track');
    const thumbRule = ruleFor('.v3-chat-list::-webkit-scrollbar-thumb');
    const interactiveThumbRule = ruleFor(
      '.v3-chat-list:hover::-webkit-scrollbar-thumb,\n.v3-chat-list:focus-within::-webkit-scrollbar-thumb,\n.v3-chat-list::-webkit-scrollbar-thumb:hover',
    );
    const activeThumbRule = ruleFor('.v3-chat-list::-webkit-scrollbar-thumb:active');
    const globalScrollbarRule = ruleIn(openchatCss, '::-webkit-scrollbar');
    const globalTrackRule = ruleIn(openchatCss, '::-webkit-scrollbar-track');
    const globalThumbRule = ruleIn(openchatCss, '::-webkit-scrollbar-thumb');
    const globalThumbHoverRule = ruleIn(openchatCss, '::-webkit-scrollbar-thumb:hover');
    const globalThumbActiveRule = ruleIn(openchatCss, '::-webkit-scrollbar-thumb:active');
    const globalButtonRule = ruleIn(openchatCss, '::-webkit-scrollbar-button');
    const globalSingleButtonRule = ruleIn(openchatCss, '::-webkit-scrollbar-button:single-button');
    const profileEditorScrollRule = ruleIn(openchatCss, '.oc-profile-editor-scroll');
    const directionalButtonRule = ruleIn(
      openchatCss,
      '::-webkit-scrollbar-button:vertical:decrement,\n'
      + '::-webkit-scrollbar-button:vertical:increment,\n'
      + '::-webkit-scrollbar-button:horizontal:decrement,\n'
      + '::-webkit-scrollbar-button:horizontal:increment,\n'
      + '::-webkit-scrollbar-button:vertical:start:decrement,\n'
      + '::-webkit-scrollbar-button:vertical:end:increment,\n'
      + '::-webkit-scrollbar-button:horizontal:start:decrement,\n'
      + '::-webkit-scrollbar-button:horizontal:end:increment',
    );
    const globalCornerRule = ruleIn(openchatCss, '::-webkit-scrollbar-corner');
    const collaborationRule = ruleFor('.cc-item-kind-agent');

    expect(ruleFor('.cc-chat-row-time')).toContain('color: var(--cc-muted);');
    expect(ruleFor('.cc-chat-row-time')).toContain('font-size: 11px;');
    expect(collaborationRule).toContain('var(--cc-muted) 76%');
    expect(collaborationRule).toContain('var(--cc-muted) 7%');
    expect(rootRule).toContain('--cc-scrollbar-page-size: 8px;');
    expect(rootRule).toContain('--cc-scrollbar-panel-size: 6px;');
    expect(rootRule).toContain('--cc-scrollbar-inline-size: 6px;');
    expect(rootRule).toContain('--cc-scrollbar-sidebar-size: 4px;');
    expect(rootRule).toContain('--cc-scrollbar-track: color-mix(in srgb, var(--cc-muted) 5%, transparent);');
    expect(rootRule).toContain('--cc-scrollbar-thumb: color-mix(in srgb, var(--cc-muted) 30%, transparent);');
    expect(rootRule).toContain('--cc-scrollbar-thumb-hover: color-mix(in srgb, var(--cc-muted) 52%, transparent);');
    expect(rootRule).toContain('--cc-scrollbar-thumb-active: color-mix(in srgb, var(--cc-muted) 70%, transparent);');
    expect(documentRule).toContain('scrollbar-color: var(--cc-scrollbar-thumb) var(--cc-scrollbar-track);');
    expect(documentRule).toContain('scrollbar-width: thin;');
    expect(css).toMatch(
      /@supports selector\(::-webkit-scrollbar\)\s*\{\s*html,\s*html \*\s*\{[^}]*scrollbar-color: auto;[^}]*scrollbar-width: auto;/,
    );
    expect(timelineRule).toContain('--cc-scrollbar-size: var(--cc-scrollbar-page-size);');
    expect(timelineRule).toContain('--cc-scrollbar-inset: 1.5px;');
    expect(inlineRule).toContain('--cc-scrollbar-size: var(--cc-scrollbar-inline-size);');
    expect(inlineRule).toContain('--cc-scrollbar-inset: 1.5px;');
    expect(listRule).toContain('--cc-scrollbar-size: var(--cc-scrollbar-sidebar-size);');
    expect(listRule).toContain('--cc-scrollbar-inset: 0.5px;');
    expect(listRule).toContain('--cc-scrollbar-track: transparent;');
    expect(listRule).toContain('scrollbar-color: var(--cc-scrollbar-thumb) transparent;');
    expect(interactiveListRule).toContain('scrollbar-color: var(--cc-scrollbar-thumb-hover) transparent;');
    expect(css).toMatch(
      /@supports selector\(::-webkit-scrollbar\)\s*\{[\s\S]*?\.v3-chat-list,\s*\.v3-chat-list:hover,\s*\.v3-chat-list:focus-within\s*\{[^}]*scrollbar-width: auto;[^}]*scrollbar-color: auto;/,
    );
    expect(scrollbarRule).toContain('width: var(--cc-scrollbar-size);');
    expect(scrollbarRule).toContain('height: var(--cc-scrollbar-size);');
    expect(trackRule).toContain('border: var(--cc-scrollbar-inset) solid transparent;');
    expect(trackRule).toContain('background: var(--cc-scrollbar-track);');
    expect(trackRule).toContain('background-clip: padding-box;');
    expect(thumbRule).toContain('min-width: 28px;');
    expect(thumbRule).toContain('min-height: 28px;');
    expect(thumbRule).toContain('border: var(--cc-scrollbar-inset) solid transparent;');
    expect(thumbRule).toContain('border-radius: 999px;');
    expect(thumbRule).toContain('background: var(--cc-scrollbar-thumb);');
    expect(interactiveThumbRule).toContain('background: var(--cc-scrollbar-thumb-hover);');
    expect(activeThumbRule).toContain('background: var(--cc-scrollbar-thumb-active);');
    expect(globalScrollbarRule).toContain('width: var(--cc-scrollbar-size, 6px);');
    expect(globalScrollbarRule).toContain('height: var(--cc-scrollbar-size, 6px);');
    expect(globalTrackRule).toContain('background: var(--cc-scrollbar-track, rgba(127, 127, 127, 0.05));');
    expect(globalTrackRule).toContain('background-clip: padding-box;');
    expect(globalThumbRule).toContain('min-width: 28px;');
    expect(globalThumbRule).toContain('min-height: 28px;');
    expect(globalThumbRule).toContain('background: var(--cc-scrollbar-thumb, rgba(127, 127, 127, 0.3));');
    expect(globalThumbHoverRule).toContain('var(--cc-scrollbar-thumb-hover, rgba(127, 127, 127, 0.52))');
    expect(globalThumbActiveRule).toContain('var(--cc-scrollbar-thumb-active, rgba(127, 127, 127, 0.7))');
    expect(globalButtonRule).toContain('-webkit-appearance: none;');
    expect(globalButtonRule).toContain('appearance: none;');
    expect(globalButtonRule).toContain('display: none !important;');
    expect(globalButtonRule).toContain('width: 0;');
    expect(globalButtonRule).toContain('height: 0;');
    expect(globalButtonRule).toContain('background: transparent;');
    expect(globalButtonRule).toContain('background-image: none;');
    expect(globalSingleButtonRule).toContain('display: none !important;');
    expect(globalSingleButtonRule).toContain('width: 0;');
    expect(globalSingleButtonRule).toContain('height: 0;');
    expect(globalSingleButtonRule).toContain('background-image: none;');
    expect(profileEditorScrollRule).toContain('margin-right: -12px;');
    expect(profileEditorScrollRule).toContain('padding-right: 12px;');
    expect(profileEditorScrollRule).toContain('scrollbar-gutter: stable;');
    expect(directionalButtonRule).toContain('display: none !important;');
    expect(directionalButtonRule).toContain('min-width: 0 !important;');
    expect(directionalButtonRule).toContain('min-height: 0 !important;');
    expect(directionalButtonRule).toContain('inline-size: 0 !important;');
    expect(directionalButtonRule).toContain('block-size: 0 !important;');
    expect(directionalButtonRule).toContain('background-image: none !important;');
    expect(globalCornerRule).toContain('background: transparent;');
  });

  it('uses the requested chat surfaces without changing sidebar or border colors', () => {
    expect(ruleFor(':root')).toContain('--cc-main-bg: #f8f8f8;');
    expect(ruleFor(':root')).toContain('--cc-main-header-bg: #f8f8f8;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-main-bg: #0f0f0f;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-main-header-bg: #0f0f0f;');
    expect(css).toMatch(/\.v3-main,\s*\.v3-message-workspace,\s*\.v3-chat-column\s*\{[^}]*background: var\(--cc-main-bg\);/);
    expect(ruleFor('.v3-local-assistant-bar')).toContain('background: var(--cc-main-header-bg);');
    expect(ruleFor('.v3-timeline')).toContain('background: var(--cc-main-bg);');
    expect(ruleFor('.v3-sidebar')).toContain('background: var(--cc-bg);');
    expect(ruleFor('.v3-local-assistant-bar')).toContain('border-bottom: 0;');
  });

  it('defines a light blue-violet liquid theme with layered glass surfaces', () => {
    const liquidRule = ruleFor('html[data-theme="liquid"]');
    const liquidGlassRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar,\n  .v3-local-assistant-bar,\n  .v3-profile-footer,\n  .v3-composer-box,\n  .v3-agent-picker-menu,\n  .v3-attachment-menu,\n  .v3-friend-action-menu,\n  .v3-profile-popover,\n  .name-dialog,\n  .oc-modal,\n  .settings-panel,\n  .collaboration-manager\n)');
    const liquidButtonRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar-collapse-btn,\n  .v3-action-btn,\n  .v3-tool,\n  .cc-section-add,\n  .v3-profile-settings,\n  .oc-btn-default,\n  .oc-modal-close,\n  .oc-profile-editor-close\n)');
    const liquidButtonHoverRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar-collapse-btn,\n  .v3-action-btn,\n  .v3-tool,\n  .cc-section-add,\n  .v3-profile-settings,\n  .oc-btn-default,\n  .oc-modal-close,\n  .oc-profile-editor-close\n):hover:not(:disabled)');
    const liquidButtonActiveRule = ruleFor('html[data-theme="liquid"] :is(\n  .v3-sidebar-collapse-btn,\n  .v3-action-btn,\n  .v3-tool,\n  .cc-section-add,\n  .v3-profile-settings,\n  .oc-btn-default,\n  .oc-modal-close,\n  .oc-profile-editor-close\n):active:not(:disabled)');
    const liquidSidebarControlRule = ruleFor(
      'html[data-theme="liquid"] :is(.cc-sidebar-primary, .cc-sidebar-search)',
    );
    const liquidSidebarControlActiveRule = ruleFor(
      'html[data-theme="liquid"] :is(.cc-sidebar-primary:hover, .cc-sidebar-search:hover, .cc-sidebar-search:focus-within)',
    );

    expect(liquidRule).toContain('--cc-accent: #5662d9;');
    expect(liquidRule).toContain('--cc-main-bg: transparent;');
    expect(liquidRule).toContain('color-scheme: light;');
    expect(liquidRule).toContain('--cc-text: #111827;');
    expect(liquidRule).toContain('--cc-text-secondary: #36425b;');
    expect(liquidRule).toContain('--cc-muted: #4f5d75;');
    expect(liquidRule).toContain('--cc-liquid-violet: #7548cf;');
    expect(liquidRule).toContain('--cc-liquid-blue: #5662d9;');
    expect(liquidRule).toContain('--oc-green-light: #95aef4;');
    expect(liquidRule).toContain('--oc-tab-active: #5662d9;');
    expect(liquidGlassRule).toContain('backdrop-filter: blur(12px) saturate(118%);');
    expect(ruleFor('html[data-theme="liquid"] .oc-modal.oc-profile-editor-modal'))
      .toContain('background: rgba(255, 255, 255, 0.94) !important;');
    expect(ruleFor('html[data-theme="liquid"] .cc-global-search-field input'))
      .toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] body'))
      .toContain('radial-gradient(circle at 12% 4%, rgba(143, 181, 255, 0.3), transparent 32%)');
    expect(ruleFor('html[data-theme="liquid"] body'))
      .toContain('linear-gradient(135deg, #f9fbff 0%, #f4f7ff 52%, #f8f4ff 100%)');
    expect(ruleFor('html[data-theme="liquid"] body')).not.toContain('liquid-dark-background.png');
    expect(ruleFor('html[data-theme="liquid"] .v3-main'))
      .toContain('radial-gradient(circle at 86% 88%, rgba(180, 145, 255, 0.18), transparent 36%)');
    expect(ruleFor('html[data-theme="liquid"] .v3-main'))
      .toContain('linear-gradient(135deg, #f9fbff 0%, #f4f7ff 54%, #f8f4ff 100%)');
    expect(ruleFor('html[data-theme="liquid"] .v3-message-workspace')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-chat-column')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-timeline')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .cc-empty-task')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-message.is-self .v3-message-bubble'))
      .toContain('background: rgba(255, 255, 255, 0.76);');
    expect(ruleFor('html[data-theme="liquid"] .v3-message.is-self .v3-message-bubble'))
      .toContain('box-shadow: none;');
    expect(ruleFor('html[data-theme="liquid"] .v3-wpi-code-block.result pre'))
      .toContain('color: var(--cc-text-secondary);');
    expect(ruleFor('html[data-theme="liquid"] .v3-wpi-tool-header .oc-wpi-tool-input'))
      .toContain('opacity: 1 !important;');
    expect(ruleFor(
      'html[data-theme="liquid"]:not([data-liquid-variant="green"]) .v3-wpi-plan-step.completed',
    )).toContain('color: var(--cc-muted);');
    expect(ruleFor(
      'html[data-theme="liquid"]:not([data-liquid-variant="green"]) :is(\n'
      + '  .cc-item-kind-agent,\n'
      + '  .cc-chat-row-time\n'
      + ')',
    )).toContain('color: var(--cc-muted);');
    expect(ruleFor('html[data-theme="liquid"] .v3-sidebar'))
      .toContain('linear-gradient(180deg, rgba(255, 255, 255, 0.92) 0%, rgba(249, 251, 255, 0.88) 58%, rgba(246, 248, 255, 0.9) 100%)');
    expect(ruleFor('html[data-theme="liquid"] .v3-sidebar'))
      .toContain('border-right-color: rgba(73, 86, 168, 0.14);');
    expect(ruleFor('html[data-theme="liquid"] .v3-profile-footer'))
      .toContain('background: rgba(249, 251, 255, 0.9);');
    expect(ruleFor('html[data-theme="liquid"] .v3-profile-footer'))
      .toContain('backdrop-filter: none;');
    expect(liquidRule).toContain('--cc-liquid-edge: rgba(73, 86, 168, 0.12);');
    expect(liquidButtonRule).toContain('border: 1px solid var(--cc-liquid-edge);');
    expect(liquidButtonRule).toContain('box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.86)');
    expect(liquidButtonRule).not.toContain('0 4px 12px rgba(79, 91, 148, 0.11)');
    expect(liquidButtonHoverRule).toContain('transform: none;');
    expect(liquidButtonActiveRule).toContain('transform: translateY(0) scale(0.98);');
    expect(liquidSidebarControlRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.92)');
    expect(liquidSidebarControlRule).not.toContain('0 4px 12px');
    expect(liquidSidebarControlActiveRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.96)');
    expect(liquidSidebarControlActiveRule).not.toContain('0 5px 13px');
    expect(ruleFor(
      'html[data-theme="liquid"] .v3-sidebar.collapsed .v3-sidebar-collapse-btn,\n'
      + 'html[data-theme="liquid"] .v3-sidebar.collapsed .v3-sidebar-collapse-btn:hover,\n'
      + 'html[data-theme="liquid"] .v3-sidebar.collapsed .v3-sidebar-collapse-btn:active',
    )).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-local-assistant-bar')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .v3-local-assistant-bar')).toContain('border-bottom: 0;');
    expect(ruleFor('html[data-theme="liquid"] .v3-local-assistant-bar')).toContain('backdrop-filter: none;');
    expect(ruleFor('html[data-theme="liquid"] .v3-local-assistant-bar')).toContain('box-shadow: none;');
    expect(ruleFor('html[data-theme="liquid"] .cc-sidebar-search input')).toContain('background: transparent;');
    expect(ruleFor('html[data-theme="liquid"] .cc-sidebar-search input')).toContain('box-shadow: none;');
    expect(css).toContain('@keyframes cc-liquid-drift-a');
    expect(css).toContain('@keyframes cc-liquid-drift-b');
    expect(css).toContain('@keyframes cc-liquid-main-flow');
  });

  it('presents theme choices and member-code unlocking as compact settings controls', () => {
    expect(ruleFor('.oc-theme-picker')).toContain('display: grid;');
    expect(ruleFor('.oc-theme-option')).toContain('grid-template-columns: 42px minmax(0, 1fr) 22px;');
    expect(ruleFor('.oc-theme-preview-liquid')).toContain('rgba(165, 128, 232, 0.68)');
    expect(ruleFor('.oc-liquid-unlock-row')).toContain('grid-template-columns: minmax(0, 1fr) auto;');
  });

  it('renders the liquid send control as a simple single-layer circle', () => {
    const sendRule = ruleFor('html[data-theme="liquid"] .v3-send');
    const sendDecorationRule = ruleFor('html[data-theme="liquid"] .v3-send::before,\nhtml[data-theme="liquid"] .v3-send::after');
    const sendHoverRule = ruleFor('html[data-theme="liquid"] .v3-send:hover:not(:disabled)');
    const sendActiveRule = ruleFor('html[data-theme="liquid"] .v3-send:active:not(:disabled)');
    const sendDisabledRule = ruleFor('html[data-theme="liquid"] .v3-send:disabled');

    expect(sendRule).toContain('border: 1px solid var(--cc-liquid-edge);');
    expect(sendRule).toContain('background: rgba(86, 98, 217, 0.16);');
    expect(sendRule).toContain('color: #4652c5;');
    expect(sendRule).not.toMatch(/0 0 0 \d+px/);
    expect(sendRule).toContain('0 2px 5px rgba(79, 91, 148, 0.12)');
    expect(sendRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.96)');
    expect(sendRule).toContain('inset 1px 0 0 rgba(86, 98, 217, 0.07)');
    expect(sendRule).toContain('inset -1px 0 0 rgba(117, 72, 207, 0.05)');
    expect(sendRule).not.toContain('radial-gradient');
    expect(sendDecorationRule).toContain('display: none;');
    expect(sendHoverRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.98)');
    expect(sendActiveRule).toContain('transform: translateY(0) scale(0.96);');
    expect(sendDisabledRule).toContain('background: rgba(255, 255, 255, 0.48);');
    expect(sendDisabledRule).toContain('color: #99a2b7;');
    expect(sendDisabledRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.82)');
    expect(sendDisabledRule).toContain('opacity: 1;');
  });

  it('renders the liquid attachment control as a simple neutral circle', () => {
    const attachmentRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus');
    const attachmentDecorationRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus::before,\nhtml[data-theme="liquid"] .v3-composer-plus::after');
    const attachmentHoverRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus:hover:not(:disabled),\nhtml[data-theme="liquid"] .v3-composer-plus[aria-expanded="true"]');
    const attachmentActiveRule = ruleFor('html[data-theme="liquid"] .v3-composer-plus:active:not(:disabled)');

    expect(attachmentRule).toContain('border: 1px solid var(--cc-liquid-edge);');
    expect(attachmentRule).toContain('background: rgba(255, 255, 255, 0.68);');
    expect(attachmentRule).toContain('color: #46506a;');
    expect(attachmentRule).toContain('box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.9)');
    expect(attachmentRule).not.toContain('0 2px 5px rgba(79, 91, 148, 0.11)');
    expect(attachmentRule).not.toContain('gradient');
    expect(attachmentDecorationRule).toContain('display: none;');
    expect(attachmentHoverRule).toContain('inset 0 1px 0 rgba(255, 255, 255, 0.98)');
    expect(attachmentActiveRule).toContain('transform: translateY(0) scale(0.96);');
  });

  it('keeps question navigator hover backgrounds neutral across themes', () => {
    const hoverRule = ruleFor('.cc-question-list-item:hover');
    const activeHoverRule = ruleFor('.cc-question-list-item.is-active:hover');

    expect(hoverRule).toContain('background: color-mix(in srgb, var(--cc-text) 8%, transparent);');
    expect(activeHoverRule).toContain('background: color-mix(in srgb, var(--cc-text) 8%, transparent);');
    expect(hoverRule).not.toContain('var(--v3-primary)');
    expect(activeHoverRule).not.toContain('var(--v3-primary)');
  });

  it('keeps the light liquid composer legible with restrained focus depth', () => {
    const composerRule = ruleFor('html[data-theme="liquid"] .v3-composer-box');
    const composerFocusRule = ruleFor('html[data-theme="liquid"] .v3-composer-box:focus-within');
    const composerInputRule = ruleFor('html[data-theme="liquid"] .v3-composer-input');

    expect(composerRule).toContain('background: rgba(255, 255, 255, 0.84);');
    expect(composerRule).toContain('0 0 0 1px rgba(73, 86, 168, 0.05)');
    expect(composerRule).toContain('0 4px 12px rgba(79, 91, 148, 0.12)');
    expect(composerRule).toContain('inset 0 8px 12px -10px rgba(255, 255, 255, 0.96)');
    expect(composerFocusRule).toContain('linear-gradient(rgba(255, 255, 255, 0.9), rgba(255, 255, 255, 0.9)) padding-box');
    expect(composerFocusRule).toContain('rgba(108, 137, 232, 0.54) 38%');
    expect(composerFocusRule).toContain('rgba(117, 72, 207, 0.56) 68%');
    expect(composerFocusRule).toContain('border-box;');
    expect(composerFocusRule).toContain('0 0 0 1px rgba(73, 86, 168, 0.08)');
    expect(composerFocusRule).toContain('inset 0 9px 13px -10px rgba(255, 255, 255, 0.96)');
    expect(composerFocusRule).toContain('transform: translateY(-1px);');
    expect(composerInputRule).toContain('background: transparent;');
    expect(composerInputRule).toContain('box-shadow: none;');
  });

  it('keeps sidebar friend requests readable at the fixed sidebar width', () => {
    const requestRule = ruleFor('.cc-contact-requests .oc-friend-request');
    const infoRule = ruleFor('.cc-contact-requests .oc-friend-request-info');
    const actionsRule = ruleFor('.cc-contact-requests .oc-friend-request-actions');
    const buttonRule = ruleFor('.cc-contact-requests .oc-friend-request-actions .oc-btn');

    expect(requestRule).toContain('grid-template-columns: 40px minmax(0, 1fr);');
    expect(requestRule).toContain('"actions actions";');
    expect(infoRule).toContain('min-width: 0;');
    expect(actionsRule).toContain('grid-template-columns: repeat(2, minmax(0, 1fr));');
    expect(buttonRule).toContain('white-space: nowrap;');
  });

  it('scopes the unified liquid control states away from the light and dark themes', () => {
    const primaryRule = ruleFor('html[data-theme="liquid"] :is(\n  .oc-btn-primary,\n  .oc-auth-btn,\n  .v3-custom-model-save,\n  .relay-access-primary-btn,\n  .v3-agent-request-action.primary\n)');
    const neutralRule = ruleFor('html[data-theme="liquid"] :is(\n  .oc-btn-default,\n  .v3-btn-secondary,\n  .cc-agent-empty-action,\n  .v3-model-status-button,\n  .v3-agent-picker-button,\n  .oc-settings-small-btn,\n  .catsco-download-action,\n  .relay-access-copy-btn,\n  .relay-access-open-btn,\n  .relay-access-key-actions button,\n  .relay-access-secret-box button\n)');
    const settingsRule = ruleFor('html[data-theme="liquid"] :is(.oc-settings-list-item, .oc-settings-list-button, .cc-new-task-agent)');
    const dangerRule = ruleFor('html[data-theme="liquid"] :is(.oc-btn-danger, button.danger, .mobile-channel-unlink-btn)');
    const focusRule = ruleFor('html[data-theme="liquid"] button:focus-visible');

    expect(primaryRule).toContain('background: rgba(86, 98, 217, 0.94) !important;');
    expect(primaryRule).toContain('inset 0 7px 10px -8px rgba(255, 255, 255, 0.46)');
    expect(neutralRule).toContain('border: 1px solid var(--cc-liquid-edge) !important;');
    expect(neutralRule).toContain('background: rgba(255, 255, 255, 0.72) !important;');
    expect(settingsRule).toContain('background: rgba(255, 255, 255, 0.72) !important;');
    expect(dangerRule).toContain('background: rgba(218, 69, 79, 0.09) !important;');
    expect(focusRule).toContain('outline: 2px solid var(--cc-focus-ring);');
    expect(primaryRule).toContain('html[data-theme="liquid"] :is(');
    expect(neutralRule).toContain('html[data-theme="liquid"] :is(');
    expect(ruleFor('html[data-theme="liquid"] :is(\n  .catsco-download-icon,\n  .relay-access-summary-icon\n)'))
      .toContain('background: rgba(86, 98, 217, 0.11);');
    expect(ruleFor('html[data-theme="liquid"] .relay-access-state:not(.inactive):not(.revoked)'))
      .toContain('background: rgba(86, 98, 217, 0.1);');
    expect(ruleFor('html[data-theme="liquid"] .relay-access-current-quota.active'))
      .toContain('background: rgba(86, 98, 217, 0.07);');
    expect(ruleFor('html[data-theme="liquid"] .relay-access-quota-bar i'))
      .toContain('linear-gradient(90deg, var(--cc-liquid-blue), var(--cc-liquid-violet))');
    expect(ruleFor('html[data-theme="liquid"] :is(\n  .v3-model-quota,\n  .v3-model-menu-item .v3-model-menu-quota\n):not(.warning):not(.danger):not(.muted)'))
      .toContain('color: var(--cc-liquid-blue);');
  });

  it('aligns registration verification controls to the shared field grid', () => {
    const rowRule = ruleFor('.oc-auth-code-row');
    const inputRule = ruleFor('.oc-auth-code-row .oc-auth-input');
    const buttonRule = ruleFor('.oc-auth-code-row .oc-auth-btn');

    expect(rowRule).toContain('align-items: stretch;');
    expect(rowRule).toContain('margin-bottom: 12px;');
    expect(inputRule).toContain('margin: 0;');
    expect(buttonRule).toContain('height: 46px;');
    expect(buttonRule).toContain('margin: 0;');
  });

  it('layers the authentication flow behind the interactive card', () => {
    const authRule = ruleFor('.oc-auth');
    const flowRule = ruleFor('.oc-auth-flow-background');
    const cardRule = ruleFor('.oc-auth-card');
    const logoRule = ruleFor('.oc-auth-logo');

    expect(authRule).toContain('isolation: isolate;');
    expect(flowRule).toContain('position: fixed;');
    expect(flowRule).toContain('z-index: 0;');
    expect(flowRule).toContain('pointer-events: none;');
    expect(cardRule).toContain('z-index: 1;');
    expect(logoRule).toContain('color: var(--cc-accent);');
    expect(logoRule).toContain('font-weight: 700;');
  });

  it('centers the sidebar settings icon inside its hover surface', () => {
    const settingsRule = ruleFor('.v3-profile-settings');
    const iconRule = ruleFor('.v3-profile-settings > svg');

    expect(settingsRule).toContain('display: grid;');
    expect(settingsRule).toContain('place-items: center;');
    expect(settingsRule).toContain('width: 32px;');
    expect(settingsRule).toContain('height: 32px;');
    expect(settingsRule).toContain('padding: 0;');
    expect(settingsRule).toContain('border: 1px solid transparent;');
    expect(settingsRule).toContain('line-height: 0;');
    expect(iconRule).toContain('display: block;');
    expect(css).toContain('.v3-profile-footer:focus-visible .v3-profile-settings');
    expect(css).toContain('background: var(--cc-pressed);');
    expect(css).toContain('color: var(--cc-text);');
  });

  it('keeps friend requests visible and gives approval a consistent green interaction', () => {
    const panelRule = ruleFor('.v3-agent-request-panel');
    const actionRule = ruleFor('.v3-agent-request-action');
    const approvalHoverRule = ruleFor('.v3-agent-request-action.primary:hover:not(:disabled),\n.v3-agent-request-action.primary:focus-visible');
    const approvalActiveRule = ruleFor('.v3-agent-request-action.primary:active:not(:disabled)');

    expect(panelRule).toContain('flex: 0 0 auto;');
    expect(actionRule).toContain('flex: 0 0 26px;');
    expect(actionRule).toContain('transition: background-color 140ms ease');
    expect(approvalHoverRule).toContain('background: var(--cc-accent-hover);');
    expect(approvalHoverRule).toContain('filter: none;');
    expect(approvalActiveRule).toContain('transform: scale(0.94);');
    expect(approvalActiveRule).toContain('color: #fff;');
  });

  it('keeps the contact request count at the right edge until the more button appears', () => {
    const badgeRule = ruleFor('.cc-section-request-badge');
    const menuRule = ruleFor('.cc-top-level-section > .cc-section-add');

    expect(badgeRule).toContain('min-width: 16px;');
    expect(badgeRule).toContain('height: 16px;');
    expect(badgeRule).toContain('padding: 0 4px;');
    expect(badgeRule).toContain('margin-left: auto;');
    expect(badgeRule).toContain('margin-right: 0;');
    expect(badgeRule).toContain('font-size: 10px;');
    expect(menuRule).toContain('position: absolute;');
    expect(menuRule).toContain('right: 2px;');
    expect(css).toContain('.cc-contacts-section:hover > .cc-section-request-badge');
    expect(css).toContain('transform: translateX(-34px);');
  });

  it('right-aligns every top-level action and keeps its chevron close to the title', () => {
    const sectionRule = ruleFor('.cc-top-level-section');
    const toggleRule = ruleFor('.cc-section-toggle');
    const activeUnreadRule = ruleFor(
      `.cc-contacts-section:hover .cc-section-unread-dot,
.cc-contacts-section:focus-within .cc-section-unread-dot`,
    );
    const actionRule = ruleFor('.cc-top-level-section > .cc-section-add');
    const unreadRule = ruleFor('.cc-section-unread-dot');

    expect(sectionRule).toContain('position: relative;');
    expect(toggleRule).toContain('gap: 3px;');
    expect(toggleRule).toContain('padding: 0;');
    expect(unreadRule).toContain('margin-left: auto;');
    expect(unreadRule).toContain('transition: opacity 120ms ease, transform 120ms ease;');
    expect(activeUnreadRule).toContain('opacity: 0;');
    expect(actionRule).toContain('position: absolute;');
    expect(actionRule).toContain('right: 2px;');
  });

  it('aligns contact and project conversation actions with regular task actions', () => {
    const actionRule = ruleFor('.v3-chat-item .cc-chat-row-actions');
    const agentActionRule = ruleFor('.cc-agent-row-trailing .v3-agent-row-actions');
    const projectActionRule = ruleFor('.cc-project-menu-trigger');
    const projectTrailingRule = ruleFor('.cc-project-row .cc-sidebar-row-trailing');
    const projectCountRule = ruleFor('.cc-project-count');
    const friendUnreadRule = ruleFor('.cc-chat-row-trailing > .cc-friend-unread-dot');
    const rowHoverMetadataRule = ruleFor(
      `.v3-chat-item:hover .cc-chat-row-time,
.v3-chat-item:focus-within .cc-chat-row-time,
.v3-chat-item:hover .cc-task-row-status,
.v3-chat-item:focus-within .cc-task-row-status,
.v3-chat-item:hover .cc-friend-unread-dot,
.v3-chat-item:focus-within .cc-friend-unread-dot`,
    );

    expect(actionRule).toContain('justify-content: flex-end;');
    expect(actionRule).toContain('right: -5px;');
    expect(agentActionRule).toContain('justify-content: flex-end;');
    expect(agentActionRule).toContain('gap: 2px;');
    expect(agentActionRule).toContain('right: -5px;');
    expect(projectActionRule).toContain('flex: 0 0 var(--cc-sidebar-action-size);');
    expect(projectTrailingRule).toContain('width: var(--cc-sidebar-action-size);');
    expect(projectCountRule).toContain('position: absolute;');
    expect(projectCountRule).toContain('right: -5px;');
    expect(projectCountRule).toContain('width: var(--cc-sidebar-action-size);');
    expect(projectCountRule).toContain('justify-content: center;');
    expect(friendUnreadRule).toContain('right: -1px;');
    expect(friendUnreadRule).toContain('transition: opacity 120ms ease, transform 120ms ease;');
    expect(rowHoverMetadataRule).toContain('opacity: 0;');
    expect(css).not.toContain('.cc-history-item .cc-chat-row-actions');
  });

  it('keeps task status visible beside touch-friendly mobile actions', () => {
    expect(css).toContain(`@media (hover: none), (pointer: coarse) {
  .v3-chat-list {
    --cc-sidebar-row-height: 44px;
    --cc-sidebar-action-size: 40px;
    --cc-sidebar-trailing-width: 84px;
  }

  .cc-chat-row-trailing .cc-chat-row-time {
    opacity: 0;
  }

  .cc-history-item .cc-chat-row-trailing:has(> .cc-task-row-status) {
    flex-basis: 108px;
    width: 108px;
    min-width: 108px;
  }

  .cc-history-item .cc-chat-row-trailing > .cc-task-row-status {
    right: calc((var(--cc-sidebar-action-size) * 2) + 4px);
    opacity: 1;
  }`);
    expect(ruleFor('.cc-history-item .cc-chat-row-trailing')).toContain(
      'width: var(--cc-sidebar-trailing-width);',
    );
  });

  it('uses filled terminal-state dots that remain distinguishable without color alone', () => {
    const dotRule = ruleFor('.cc-task-status-dot');

    expect(dotRule).toContain('width: 8px;');
    expect(dotRule).toContain('height: 8px;');
    expect(dotRule).toContain('border-radius: 999px;');
    expect(dotRule).toContain('background: currentColor;');
    expect(dotRule).toContain('position: relative;');
    expect(ruleFor('.cc-task-row-status.completed')).toContain('color: var(--cc-success-text);');
    expect(ruleFor('.cc-task-row-status.failed')).toContain('color: var(--cc-danger);');
    expect(ruleFor('.cc-task-row-status.cancelled,\n.cc-task-row-status.stale'))
      .toContain('color: var(--cc-warning-text);');
    expect(css).toContain(`.cc-task-status-dot--failed::after,
.cc-task-status-dot--cancelled::after,
.cc-task-status-dot--stale::after {`);
    expect(css).toContain(`.cc-task-status-dot--cancelled::after {
  border-style: dashed;
}`);
    expect(css).toContain(`.cc-task-status-dot--stale::after {
  border-style: dotted;
}`);
    expect(css).not.toContain('.cc-task-status-icon');
  });

  it('keeps terminal state dots at least 3:1 against every sidebar theme', () => {
    const root = ruleFor(':root');
    const dark = ruleFor('html[data-theme="dark"]');
    const liquid = ruleFor('html[data-theme="liquid"]');
    const themes = [
      { background: hexToken(root, '--cc-bg'), colors: [
        hexToken(root, '--cc-success-text'),
        hexToken(root, '--cc-danger'),
        hexToken(root, '--cc-warning-text'),
      ] },
      { background: hexToken(dark, '--cc-bg'), colors: [
        hexToken(dark, '--cc-success-text'),
        hexToken(root, '--cc-danger'),
        hexToken(dark, '--cc-warning-text'),
      ] },
      { background: hexToken(liquid, '--cc-bg'), colors: [
        hexToken(liquid, '--cc-success-text'),
        hexToken(root, '--cc-danger'),
        hexToken(liquid, '--cc-warning-text'),
      ] },
    ];

    themes.forEach(({ background, colors }) => {
      expect(background).toBeTruthy();
      colors.forEach((color) => {
        expect(color).toBeTruthy();
        expect(contrastRatio(color, background)).toBeGreaterThanOrEqual(3);
      });
    });
  });

  it('checks terminal dots against real sidebar gradients and interaction overlays', () => {
    const root = ruleFor(':root');
    const dark = ruleFor('html[data-theme="dark"]');
    const liquid = ruleFor('html[data-theme="liquid"]');
    const liquidSidebar = ruleFor('html[data-theme="liquid"] .v3-sidebar');
    const liquidSection = ruleFor(
      'html[data-theme="liquid"] .v3-chat-list > :is(.cc-contacts-section, .cc-project-section, .cc-conversation-section)',
    );
    const liquidActive = ruleFor('html[data-theme="liquid"] .v3-chat-item.active');
    const green = ruleIn(
      liquidGreenCss,
      'html[data-theme="liquid"][data-liquid-variant="green"]',
    );
    const greenSidebar = liquidGreenCss.match(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.v3-sidebar\s*\{[\s\S]*?\n\}/,
    )?.[0] || '';
    const greenSection = liquidGreenCss.match(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.v3-chat-list > :is\([^)]*\)\s*\{[\s\S]*?\n\}/,
    )?.[0] || '';
    const greenActive = liquidGreenCss.match(
      /html\[data-theme="liquid"\]\[data-liquid-variant="green"\] \.v3-chat-item\.active\s*\{[\s\S]*?\n\}/,
    )?.[0] || '';
    const statuses = [
      hexToken(root, '--cc-success-text'),
      hexToken(root, '--cc-danger'),
      hexToken(root, '--cc-warning-text'),
    ];
    const darkStatuses = [
      hexToken(dark, '--cc-success-text'),
      hexToken(root, '--cc-danger'),
      hexToken(dark, '--cc-warning-text'),
    ];
    const liquidStatuses = [
      hexToken(liquid, '--cc-success-text'),
      hexToken(root, '--cc-danger'),
      hexToken(liquid, '--cc-warning-text'),
    ];
    const greenStatuses = [
      hexToken(green, '--cc-success-text'),
      hexToken(green, '--cc-danger') || hexToken(root, '--cc-danger'),
      hexToken(green, '--cc-warning-text'),
    ];
    const lightSurfaces = [
      { name: 'light sidebar', colors: [hexToken(root, '--cc-bg')] },
      { name: 'light row hover', colors: [hexToken(root, '--cc-hover')] },
      { name: 'light row active', colors: [hexToken(root, '--cc-selected')] },
    ];
    const darkSurfaces = [
      { name: 'dark sidebar', colors: [hexToken(dark, '--cc-bg')] },
      { name: 'dark row hover', colors: [hexToken(dark, '--cc-hover')] },
      { name: 'dark row active', colors: [hexToken(dark, '--cc-selected')] },
    ];
    const liquidSurfaces = [
      { name: 'liquid sidebar token', colors: [hexToken(liquid, '--cc-bg')] },
      {
        name: 'liquid sidebar gradient',
        colors: cssColors(propertyValue(css, 'html[data-theme="liquid"] .v3-sidebar', 'background'))
          .map((color) => compositeColor(parseCssColor(color), parseCssColor(hexToken(liquid, '--cc-bg')))),
      },
      {
        name: 'liquid sidebar section',
        colors: cssColors(propertyValue(css, 'html[data-theme="liquid"] .v3-chat-list > :is(.cc-contacts-section, .cc-project-section, .cc-conversation-section)', 'background')),
      },
      {
        name: 'liquid active overlay',
        colors: [compositeColor(
          parseCssColor(cssColors(propertyValue(css, 'html[data-theme="liquid"] .v3-chat-item.active', 'background'))[0]),
          parseCssColor('rgb(249, 251, 255)'),
        )],
      },
      {
        name: 'liquid history hover',
        colors: [compositeColor(parseCssColor('rgba(86, 98, 217, 0.12)'), parseCssColor('#ffffff'))],
      },
    ];
    const greenSurfaces = [
      {
        name: 'green sidebar gradient',
        colors: cssColors(propertyValue(liquidGreenCss, 'html[data-theme="liquid"][data-liquid-variant="green"] .v3-sidebar', 'background')),
      },
      {
        name: 'green sidebar section',
        colors: cssColors(propertyValue(liquidGreenCss, 'html[data-theme="liquid"][data-liquid-variant="green"] .v3-chat-list > :is(.cc-contacts-section, .cc-project-section, .cc-conversation-section)', 'background')),
      },
      {
        name: 'green active overlay',
        colors: [compositeColor(
          parseCssColor(cssColors(propertyValue(liquidGreenCss, 'html[data-theme="liquid"][data-liquid-variant="green"] .v3-chat-item.active', 'background'))[0]),
          parseCssColor('rgb(18, 24, 22)'),
        )],
      },
    ];

    expect(liquidSidebar).toContain('background: linear-gradient(180deg');
    expect(liquidSection).toContain('background: rgb(249, 251, 255);');
    expect(liquidActive).toContain('background: rgba(233, 237, 255, 0.72);');
    expect(greenSidebar).toContain('background: linear-gradient(180deg, #151b19 0%, #111714 58%, #0f1513 100%);');
    expect(greenSection).toContain('background: rgb(18, 24, 22);');
    expect(greenActive).toContain('background: rgba(255, 255, 255, 0.09);');
    expect(statuses.every(Boolean)).toBe(true);
    expect(darkStatuses.every(Boolean)).toBe(true);
    expect(liquidStatuses.every(Boolean)).toBe(true);
    expect(greenStatuses.every(Boolean)).toBe(true);

    [
      ['light', statuses, lightSurfaces],
      ['dark', darkStatuses, darkSurfaces],
      ['liquid', liquidStatuses, liquidSurfaces],
      ['green liquid', greenStatuses, greenSurfaces],
    ].forEach(([theme, colors, surfaces]) => {
      surfaces.forEach(({ name, colors: backgrounds }) => {
        backgrounds.forEach((background) => {
          expect(background, `${theme} ${name} background`).toBeTruthy();
          colors.forEach((foreground) => {
            expect(contrastRatio(foreground, background), `${theme} ${name} ${foreground}`).toBeGreaterThanOrEqual(3);
          });
        });
      });
    });
  });

  it('places the mobile composer closer to the safe-area edge', () => {
    expect(css).toContain(`  .v3-composer {
    padding: 8px 8px calc(12px + env(safe-area-inset-bottom));
  }`);
    expect(css).toContain(`  .v3-composer.cc-empty-composer-wrap {
    position: fixed;`);
    expect(css).toContain('padding: 8px 8px calc(12px + env(safe-area-inset-bottom));');
  });

  it('makes top-level sidebar section titles distinct from expanded items', () => {
    expect(ruleFor('.cc-top-level-section')).toContain('font-weight: 600;');
    expect(ruleFor('.cc-history-section')).toContain('font-weight: 500;');
    expect(ruleFor('.v3-chat-item')).toContain('font-weight: 400;');
  });

  it('reveals top-level section actions without adding a row hover surface', () => {
    const actionRevealRule = ruleFor(
      `.v3-chat-section:hover > .cc-section-add,
.v3-chat-section:focus-within > .cc-section-add,
.cc-section-add:focus-visible`,
    );
    const nestedHoverRule = ruleFor('.v3-chat-section:not(.cc-top-level-section):hover');

    expect(actionRevealRule).toContain('opacity: 1;');
    expect(actionRevealRule).toContain('visibility: visible;');
    expect(nestedHoverRule).toContain('background: var(--cc-hover);');
    expect(css).not.toContain('.v3-chat-section:hover {\n  background: var(--cc-hover);');
  });

  it('stacks sidebar section titles while their content scrolls underneath', () => {
    const stickyRule = ruleFor(
      '.v3-chat-list > :is(.cc-contacts-section, .cc-project-section, .cc-conversation-section)',
    );
    const sectionGapRule = ruleFor('.v3-chat-list > .cc-section-after-expanded-content');
    const contactsRule = ruleFor('.v3-chat-list > .cc-contacts-section');
    const projectsRule = ruleFor('.v3-chat-list > .cc-project-section');
    const conversationsRule = ruleFor('.v3-chat-list > .cc-conversation-section');
    const compactProjectsRule = projectsRule.replace(/\s+/g, ' ');
    const compactConversationsRule = conversationsRule.replace(/\s+/g, ' ');

    expect(stickyRule).toContain('position: sticky;');
    expect(stickyRule).toContain('z-index: 12;');
    expect(stickyRule).toContain('border-radius: 0;');
    expect(stickyRule).toContain('background: var(--cc-bg);');
    expect(stickyRule).toContain(
      '0 calc(-1 * var(--cc-sidebar-row-gap)) 0 0 var(--cc-bg),',
    );
    expect(stickyRule).toContain(
      '0 var(--cc-sidebar-row-gap) 0 0 var(--cc-bg);',
    );
    expect(ruleFor('.v3-chat-list')).toContain('--cc-sidebar-group-gap: 12px;');
    expect(ruleFor('.v3-chat-list')).toContain('--cc-sidebar-list-padding-top: 8px;');
    expect(ruleFor('.v3-chat-list')).toContain('scrollbar-gutter: stable;');
    expect(sectionGapRule).toContain(
      'margin-top: calc(var(--cc-sidebar-group-gap) - var(--cc-sidebar-row-gap)) !important;',
    );
    expect(contactsRule).toContain('top: var(--cc-sidebar-row-gap);');
    expect(contactsRule).toContain('z-index: 14;');
    expect(contactsRule).toContain(
      '0 calc(-1 * var(--cc-sidebar-list-padding-top) - var(--cc-sidebar-row-gap)) 0 0 var(--cc-bg),',
    );
    expect(compactProjectsRule).toContain(
      'top: calc( var(--cc-sidebar-row-height) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap) );',
    );
    expect(projectsRule).toContain('z-index: 13;');
    expect(compactConversationsRule).toContain(
      'var(--cc-sidebar-row-height) + var(--cc-sidebar-row-height) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap) + var(--cc-sidebar-row-gap)',
    );

    const liquidStickyRule = ruleFor(
      'html[data-theme="liquid"] .v3-chat-list > :is(.cc-contacts-section, .cc-project-section, .cc-conversation-section)',
    );
    expect(liquidStickyRule).toContain('background: rgb(249, 251, 255);');
    expect(liquidStickyRule).not.toContain('rgba(249, 251, 255, 0.96)');

    const liquidContactsRule = ruleFor(
      'html[data-theme="liquid"] .v3-chat-list > .cc-contacts-section',
    );
    expect(liquidContactsRule).toContain(
      '0 calc(-1 * var(--cc-sidebar-list-padding-top) - var(--cc-sidebar-row-gap)) 0 0 rgb(249, 251, 255),',
    );
  });

  it('uses the project row geometry for every sidebar hierarchy level', () => {
    const listRule = ruleFor('.v3-chat-list');
    const sectionRule = ruleFor('.v3-chat-section');
    const itemRule = ruleFor('.v3-chat-item');
    const historyRule = ruleFor('.cc-history-item');
    const projectRowRule = ruleFor('.cc-project-row');
    const projectItemRule = ruleFor('.cc-project-row > .cc-project-item');
    const projectTaskRule = ruleFor('.cc-project-task-item');
    const nestedItemRule = ruleFor('.cc-sidebar-item-level-2');
    const contactRule = ruleFor('.cc-contact-item');
    const compactContactRule = ruleFor(
      '.cc-contact-item[data-contact-kind="friend"],\n.cc-contact-item[data-contact-kind="agent"]',
    );

    expect(listRule).toContain('--cc-sidebar-row-height: 36px;');
    expect(listRule).toContain('--cc-sidebar-row-gap: 1px;');
    expect(listRule).toContain('--cc-sidebar-row-padding-y: 6px;');
    expect(listRule).toContain('--cc-sidebar-action-size: 32px;');
    expect(listRule).toContain('--cc-sidebar-trailing-width: 68px;');
    expect(sectionRule).toContain('min-height: var(--cc-sidebar-row-height);');
    expect(sectionRule).toContain('line-height: 20px;');
    expect(itemRule).toContain('min-height: var(--cc-sidebar-row-height);');
    expect(itemRule).toContain('margin: var(--cc-sidebar-row-gap) 0;');
    expect(historyRule).toContain('min-height: var(--cc-sidebar-row-height);');
    expect(historyRule).toContain('padding-top: var(--cc-sidebar-row-padding-y);');
    expect(historyRule).toContain('padding-bottom: var(--cc-sidebar-row-padding-y);');
    expect(projectRowRule).toContain('position: relative;');
    expect(projectItemRule).toContain('margin: 0;');
    expect(projectTaskRule).toContain('width: 100%;');
    expect(nestedItemRule).toContain('padding-left: 28px;');
    expect(contactRule).toContain('min-height: var(--cc-sidebar-row-height);');
    expect(compactContactRule).toContain('min-height: var(--cc-sidebar-row-height);');
    expect(compactContactRule).toContain('padding-top: var(--cc-sidebar-row-padding-y);');
    expect(compactContactRule).toContain('padding-bottom: var(--cc-sidebar-row-padding-y);');
  });

  it('keeps the selected project-task checkmark visible against its accent fill', () => {
    expect(ruleFor('.cc-project-task-selection-indicator svg')).toContain('color: currentColor;');
    expect(ruleFor('.cc-project-task-option.is-selected .cc-project-task-selection-indicator svg')).toContain('color: #fff;');
  });

  it('keeps project selection controls inside their scroll rail', () => {
    const listRule = ruleFor('.cc-new-task-agent-list');
    const optionRule = ruleFor('.cc-new-task-agent');

    expect(listRule).toContain('min-width: 0;');
    expect(listRule).toContain('overflow-x: hidden;');
    expect(listRule).toContain('margin-right: -12px;');
    expect(listRule).toContain('padding-right: 12px;');
    expect(listRule).toContain('scrollbar-gutter: stable;');
    expect(optionRule).toContain('box-sizing: border-box;');
    expect(optionRule).toContain('min-width: 0;');
    expect(optionRule).toContain('overflow: hidden;');
  });

  it('keeps group member actions on one desktop row without overlapping members', () => {
    const memberRule = ruleFor('.oc-group-settings-modal .oc-settings-member-item');
    const actionsRule = ruleFor('.oc-group-settings-modal .oc-settings-member-actions');

    expect(memberRule).toContain('grid-template-columns: 32px minmax(0, 1fr) auto;');
    expect(memberRule).toContain('align-items: center;');
    expect(actionsRule).toContain('max-width: none;');
    expect(actionsRule).toContain('flex-wrap: nowrap;');
    expect(css).toContain('@media (max-width: 640px)');
    expect(css).toContain('grid-column: 2;');
  });

  it('presents the editable group name as a comfortable inline field', () => {
    const modalRule = ruleFor('.oc-modal.oc-group-settings-modal');
    const nameRule = ruleFor('.oc-group-name-input');
    const displayRule = ruleFor('.oc-group-name-display');
    const focusRule = ruleFor(
      '.oc-group-summary .oc-group-name-input:focus,\n.oc-group-summary .oc-group-name-input:focus-visible',
    );

    expect(modalRule).toContain('width: min(640px, calc(100vw - 32px)) !important;');
    expect(modalRule).toContain('max-width: 640px !important;');
    expect(nameRule).toContain('min-height: 34px;');
    expect(nameRule).toContain('padding: 0 8px 0 4px;');
    expect(nameRule).toContain('border: 1px solid var(--cc-border);');
    expect(nameRule).toContain('border-radius: var(--cc-radius-sm);');
    expect(displayRule).toContain('background: transparent;');
    expect(displayRule).toContain('cursor: text;');
    expect(focusRule).toContain('outline: 0;');
    expect(focusRule).toContain('box-shadow: inset 0 0 0 1px');
    expect(ruleFor('.oc-group-name-input + .oc-group-member-count')).toContain('padding-left: 4px;');
  });

  it('shows the narrow-screen sidebar shadow only while the drawer is open', () => {
    expect(css).toContain('.v3-sidebar:not(.collapsed) {\n    position: fixed;');
    expect(css).toContain('flex-basis: min(86vw, 300px);\n    box-shadow: none;');
    expect(css).toContain('.v3-sidebar:not(.collapsed).open {\n    box-shadow: 16px 0 40px rgba(0, 0, 0, 0.34);');
  });

  it('zooms compact task artwork without resizing its button and shows a fixed label', () => {
    const buttonRule = ruleFor('.cc-compact-new-chat,\n.cc-compact-tool,\n.cc-compact-conversation');
    const avatarRule = ruleFor('.cc-compact-conversation .oc-avatar');
    const hintRule = ruleFor('.cc-compact-task-hint');

    expect(buttonRule).toContain('width: 42px;');
    expect(buttonRule).toContain('height: 42px;');
    expect(avatarRule).toContain(
      'transition: transform 150ms ease, filter 150ms ease, opacity 150ms ease;',
    );
    expect(css).toContain('transform: scale(1.14);');
    expect(hintRule).toContain('position: fixed;');
    expect(hintRule).toContain('pointer-events: none;');
  });

  it('keeps collapsed sidebar tools transparent until an interaction state is active', () => {
    const newChatRule = ruleFor('.cc-compact-new-chat');
    const skillHubRule = ruleFor('.v3-sidebar.collapsed .cc-sidebar-skillhub-entry');
    const activeSkillHubRule = ruleFor('.v3-sidebar.collapsed .cc-sidebar-skillhub-entry:is(:active, .active)');
    const expandedHistoryRule = ruleFor('.cc-compact-history-trigger[aria-expanded="true"]');
    const historyPanelRule = ruleFor('.cc-compact-history-panel');
    const historyItemRule = ruleFor('.cc-compact-history-item');
    const historyLabelRule = ruleFor('.cc-compact-history-label');
    const historyTooltipRule = ruleFor('.cc-compact-history-tooltip');
    const compactHistoryHoverRule = ruleFor('.cc-compact-history-item:is(:hover, :focus-visible)');
    const expandedHistoryHoverRule = ruleFor('.v3-chat-item.cc-history-item:is(:hover, :focus-within)');
    const interactionRule = ruleFor('.cc-compact-new-chat:hover,\n.cc-compact-tool:hover,\n.cc-compact-conversation:hover,\n.cc-compact-new-chat:active,\n.cc-compact-tool:active,\n.cc-compact-conversation.active,\n.cc-compact-new-chat:focus-visible,\n.cc-compact-tool:focus-visible,\n.cc-compact-conversation:focus-visible');

    expect(newChatRule).not.toContain('background: var(--cc-panel);');
    expect(skillHubRule).toContain('width: 42px;');
    expect(skillHubRule).toContain('height: 42px;');
    expect(skillHubRule).toContain('min-height: 42px;');
    expect(skillHubRule).toContain('border-radius: 12px;');
    expect(skillHubRule).toContain('background: transparent;');
    expect(activeSkillHubRule).toContain('background: transparent;');
    expect(activeSkillHubRule).toContain('color: var(--cc-accent-text);');
    expect(expandedHistoryRule).toContain('background: var(--cc-hover);');
    expect(expandedHistoryRule).toContain('box-shadow: none;');
    expect(historyPanelRule).toContain('background: var(--cc-history-panel-bg);');
    expect(historyPanelRule).toContain('grid-template-columns: minmax(0, 1fr);');
    expect(historyPanelRule).toContain('overflow-x: hidden;');
    expect(historyPanelRule).toContain('overflow-y: auto;');
    expect(historyItemRule).toContain('min-width: 0;');
    expect(historyItemRule).toContain('overflow: hidden;');
    expect(historyLabelRule).toContain('flex: 1 1 auto;');
    expect(historyLabelRule).toContain('text-overflow: ellipsis;');
    expect(css).toContain('.cc-compact-task-status.cc-compact-history-status {');
    expect(css).toContain('.cc-compact-task-status.cc-compact-history-status:not(.running) {');
    expect(historyTooltipRule).toContain('position: fixed;');
    expect(historyTooltipRule).toContain('background: var(--cc-history-panel-bg);');
    expect(historyTooltipRule).toContain('pointer-events: none;');
    expect(css).toContain('--cc-history-panel-bg: #272729;');
    expect(css).toContain('--cc-history-hover: #37373a;');
    expect(css).toContain('--cc-history-hover-border: rgba(255, 255, 255, 0.08);');
    expect(compactHistoryHoverRule).toContain('background: var(--cc-history-hover);');
    expect(compactHistoryHoverRule).toContain('box-shadow: inset 0 0 0 1px var(--cc-history-hover-border);');
    expect(expandedHistoryHoverRule).toContain('background: var(--cc-history-hover);');
    expect(expandedHistoryHoverRule).toContain('box-shadow: inset 0 0 0 1px var(--cc-history-hover-border);');
    expect(interactionRule).toContain('background: var(--cc-hover);');
  });

  it('overlays compact task status on the avatar without changing rail geometry', () => {
    const compactButtonRule = ruleFor('.cc-compact-conversation');
    const runningAvatarRule = ruleFor(
      '.cc-compact-conversation:has(.cc-compact-task-status.running) .oc-avatar',
    );
    const dotRule = ruleFor('.cc-compact-task-status:not(.running)');
    const runningRule = ruleFor('.cc-compact-task-status.running');
    const spinnerRule = ruleFor('.cc-compact-task-status.running svg');

    expect(compactButtonRule).toContain('position: relative;');
    expect(css).toContain('.cc-compact-conversation {\n  overflow: hidden;');
    expect(runningAvatarRule).toContain('filter: brightness(0.74) saturate(0.88);');
    expect(runningAvatarRule).toContain('opacity: 0.86;');
    expect(dotRule).toContain('right: 3px;');
    expect(dotRule).toContain('bottom: 3px;');
    expect(dotRule).toContain('width: 9px;');
    expect(dotRule).toContain('box-shadow: 0 0 0 2px var(--cc-bg);');
    expect(runningRule).toContain('inset: 0;');
    expect(runningRule).toContain('justify-content: center;');
    expect(spinnerRule).toContain('animation: catsco-spin 0.9s linear infinite;');
    expect(spinnerRule).not.toContain('filter:');
    expect(ruleFor('.cc-compact-task-status.completed')).toContain('color: var(--cc-success-text);');
    expect(ruleFor('.cc-compact-task-status.failed')).toContain('color: var(--cc-danger);');
    expect(ruleFor('.cc-compact-task-status.cancelled,\n.cc-compact-task-status.stale'))
      .toContain('color: var(--cc-warning-text);');
    expect(css).toContain(`.cc-compact-task-status.failed::after,
.cc-compact-task-status.cancelled::after,
.cc-compact-task-status.stale::after {`);
    expect(css).toContain(`.cc-compact-task-status.cancelled::after {
  border-style: dashed;
}`);
    expect(css).toContain(`.cc-compact-task-status.stale::after {
  border-style: dotted;
}`);
    expect(css).toContain('.cc-task-row-status.running svg,\n  .cc-compact-task-status.running svg');
  });

  it('zooms the collapsed profile avatar without changing the footer frame', () => {
    const footerRule = ruleFor('.v3-sidebar.collapsed .v3-profile-footer');
    const avatarRule = ruleFor('.v3-sidebar.collapsed .v3-profile-avatar.oc-avatar');

    expect(footerRule).toContain('width: 100%;');
    expect(footerRule).toContain('min-height: 64px;');
    expect(avatarRule).toContain('transition: transform 150ms ease;');
    expect(css).toContain('.v3-sidebar.collapsed .v3-profile-footer:hover .v3-profile-avatar.oc-avatar');
    expect(css).toContain('transform: scale(1.14);');
  });

  it('positions the collapsed profile menu beside the rail without widening it', () => {
    const popoverRule = ruleFor('.v3-profile-popover');
    const compactRule = ruleFor('.v3-profile-popover.is-compact');

    expect(popoverRule).toContain('position: fixed;');
    expect(compactRule).toContain('left: calc(var(--cc-sidebar-collapsed) + 8px);');
    expect(compactRule).toContain('bottom: 12px;');
    expect(compactRule).toContain('width: min(220px, calc(100vw - var(--cc-sidebar-collapsed) - 20px));');
  });

  it('tightens feedback upload copy and separates profile theme text', () => {
    const uploadRule = ruleFor('.oc-feedback-upload-button');
    const messageRule = ruleFor('.oc-feedback-message-field');
    const contactRule = ruleFor('.oc-feedback-contact-field input');
    const placeholderRule = ruleFor(
      '.oc-feedback-message-field textarea::placeholder,\n.oc-feedback-contact-field input::placeholder',
    );
    const themeCopyRule = ruleFor('.oc-settings-theme-button .oc-settings-list-text');
    const themeLineRule = ruleFor('.oc-settings-theme-button .oc-settings-list-text > span');

    expect(uploadRule).toContain('gap: 5px;');
    expect(messageRule).toContain('background: var(--cc-input-surface, var(--cc-code));');
    expect(contactRule).toContain('background: var(--cc-input-surface);');
    expect(placeholderRule).toContain('color: var(--cc-placeholder);');
    expect(themeCopyRule).toContain('display: grid;');
    expect(themeCopyRule).toContain('gap: 2px;');
    expect(themeLineRule).toContain('display: block;');
    expect(themeLineRule).toContain('line-height: 1.35;');
  });

  it('places the empty-task brand mark beside the lighter greeting', () => {
    const headingRule = ruleFor('.cc-empty-task-heading');
    const markRule = ruleFor('.cc-empty-task-mark');
    const emptyTaskRule = ruleFor('.cc-empty-task');

    expect(ruleFor(':root')).toContain('--cc-empty-task-bg: #f8f8f8;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-empty-task-bg: #0f0f0f;');
    expect(emptyTaskRule).toContain('background: var(--cc-empty-task-bg);');
    expect(headingRule).toContain('display: flex;');
    expect(headingRule).toContain('align-items: flex-end;');
    expect(headingRule).toContain('justify-content: center;');
    expect(headingRule).toContain('gap: 16px;');
    expect(headingRule).toContain('margin-bottom: 24px;');
    expect(headingRule).not.toContain('transform:');
    expect(markRule).toContain('width: 112px;');
    expect(markRule).toContain('height: 48px;');
    expect(markRule).toContain('margin: 0;');
    expect(ruleFor('.cc-empty-task h1')).toContain('margin: 0;');
    expect(ruleFor('.cc-empty-task h1')).toContain('font-size: 28px;');
    expect(ruleFor('.cc-empty-task h1')).toContain('font-weight: 500;');
    expect(ruleFor('.cc-empty-task h1')).toContain('line-height: 36px;');
  });

  it('uses integer line heights for the primary desktop text rails', () => {
    expect(ruleFor('.cc-sidebar-search input')).toContain('font-size: 14px;');
    expect(ruleFor('.cc-sidebar-search input')).toContain('line-height: 20px;');
    expect(ruleFor('.v3-chat-section')).toContain('font-size: 14px;');
    expect(ruleFor('.v3-chat-section')).toContain('line-height: 20px;');
    expect(ruleFor('.v3-chat-item')).toContain('font-size: 13px;');
    expect(ruleFor('.v3-chat-item')).toContain('line-height: 19px;');
    expect(ruleFor('.v3-chat-item-identity')).toContain('font-size: 12px;');
    expect(ruleFor('.v3-chat-item-identity')).toContain('line-height: 18px;');
    expect(ruleFor('.v3-composer-input')).toContain('font-size: 15px;');
    expect(ruleFor('.v3-composer-input')).toContain('line-height: 22px;');
    expect(ruleFor('.v3-composer-hint')).toContain('line-height: 18px;');
  });

  it('keeps the two primary sidebar controls on one shared geometry', () => {
    const controlsRule = ruleFor('.cc-sidebar-primary,\n.cc-sidebar-search');
    const hoverRule = ruleFor(
      '.cc-sidebar-primary:hover,\n.cc-sidebar-search:hover,\n.cc-sidebar-search:focus-within',
    );

    expect(controlsRule).toContain('width: 100%;');
    expect(controlsRule).toContain('height: 40px;');
    expect(controlsRule).toContain('min-height: 40px;');
    expect(controlsRule).toContain('padding: 0 11px;');
    expect(hoverRule).toContain('background: var(--cc-hover);');
    expect(hoverRule).toContain('color: var(--cc-text);');
  });

  it('prevents iOS focus zoom without changing non-text form controls', () => {
    const selectorList = css.match(
      /@media \(hover: none\) and \(pointer: coarse\) \{\s*([^{}]+)\s*\{\s*font-size: 16px !important;/,
    )?.[1];
    const input = document.createElement('input');

    expect(selectorList).toBeTruthy();
    expect(css).not.toContain('@media (max-width: 768px), (hover: none) and (pointer: coarse)');
    for (const type of ['text', 'search', 'email', 'password', 'tel', 'url', 'number']) {
      input.type = type;
      expect(input.matches(selectorList)).toBe(true);
    }
    input.removeAttribute('type');
    expect(input.matches(selectorList)).toBe(true);

    for (const type of ['button', 'submit', 'reset', 'file', 'color', 'hidden', 'checkbox', 'radio', 'range']) {
      input.type = type;
      expect(input.matches(selectorList)).toBe(false);
    }

    expect(document.createElement('textarea').matches(selectorList)).toBe(true);
    expect(document.createElement('select').matches(selectorList)).toBe(true);
    expect(css).not.toContain('user-scalable=no');
    expect(css).not.toContain('maximum-scale=1');
  });

  it('aligns peer messages and typing status to the unchanged composer rail', () => {
    const noticeRule = ruleFor('.v3-composer-notices .v3-live-input-status');

    expect(ruleFor('.v3-timeline')).toContain('padding: 18px 20px 140px;');
    expect(ruleFor('.v3-timeline-inner')).toContain('max-width: 760px;');
    expect(ruleFor('.v3-message.is-peer .v3-avatar-col')).toContain('margin-right: 10px;');
    expect(ruleFor('.v3-message.is-peer .v3-message-bubble')).toContain('padding: 8px 0 14px;');
    expect(ruleFor('.v3-message.is-peer .v3-message-footer')).toContain('padding: 0;');
    expect(ruleFor('.v3-peer-typing')).toContain('width: min(760px, 100%);');
    expect(ruleFor('.v3-peer-typing')).toContain('margin: 4px auto;');
    expect(ruleFor('.v3-peer-typing')).toContain('padding: 8px 0 14px;');
    expect(ruleFor('.v3-peer-typing-label')).toContain('font-weight: 400;');
    expect(ruleFor('.v3-peer-typing-label')).toContain('animation: cc-peer-typing-pulse 1200ms ease-in-out infinite;');
    expect(ruleFor('.v3-peer-typing-label')).toContain('will-change: opacity;');
    expect(css).toContain('@keyframes cc-peer-typing-pulse');
    expect(css).toContain('opacity: 0.52;');
    expect(css).toContain('opacity: 0.88;');
    expect(css).toContain('animation: none;');
    expect(ruleFor('.v3-composer-box')).toContain('width: min(760px, 100%);');
    expect(ruleFor('.v3-composer-box.is-agent-reply-active'))
      .toContain('animation: cc-composer-agent-reply 1800ms ease-in-out infinite;');
    expect(css).toContain('@keyframes cc-composer-agent-reply');
    expect(css).toContain('border-color: color-mix(in srgb, var(--cc-accent) 56%, transparent);');
    expect(css).toContain('border-color: color-mix(in srgb, var(--cc-accent) 88%, white 12%);');
    expect(css).not.toContain('border-color: rgba(31, 211, 151, 0.95);');
    expect(css).toMatch(
      /@media \(prefers-reduced-motion: reduce\) \{[\s\S]*?\.v3-composer-box\.is-agent-reply-active \{[^}]*animation: none;/,
    );
    expect(css).toMatch(
      /@media \(prefers-reduced-motion: reduce\) \{[\s\S]*?border-color: color-mix\(in srgb, var\(--cc-accent\) 84%, transparent\);/,
    );
    expect(ruleFor('.v3-composer-notices')).toContain('width: 100%;');
    expect(noticeRule).toContain('min-height: 36px;');
    expect(noticeRule).toContain('padding: 8px 14px 8px 28px;');
    expect(noticeRule).toContain('border-bottom: 1px solid');
    expect(ruleFor('.v3-composer-context')).toContain('border-radius: 27px 27px 0 0;');
    expect(ruleFor('.oc-reply-bar')).toContain('width: 100% !important;');
    expect(ruleFor('.oc-reply-bar')).toContain('border-radius: 0 !important;');
    expect(ruleFor('.oc-reply-bar')).toContain('background: transparent !important;');
    expect(ruleFor('.oc-reply-bar')).toContain('font-size: 12px !important;');
    expect(css).toContain('width: 32px;');
    expect(css).not.toContain('margin: 0 auto -11px');
    expect(ruleFor('.v3-attachment-notice')).toContain('justify-content: center;');
    expect(ruleFor('.v3-attachment-notice > span')).toContain('text-overflow: ellipsis;');
    expect(ruleFor('.v3-composer-attachment-tray')).toContain('overflow-x: auto;');
    expect(ruleFor('.v3-composer-attachment-chip')).toContain('height: 56px;');
    expect(ruleFor('.v3-composer-attachment-chip.is-image')).toContain('width: 56px;');
    expect(ruleFor('.v3-composer-attachment-remove')).toContain('position: absolute;');
    expect(ruleFor('.v3-composer-attachment-preview')).toContain('cursor: zoom-in;');
    expect(ruleFor('.v3-composer-image-preview-backdrop')).toContain('position: fixed;');
    expect(css).not.toContain('.v3-composer-attachments {');
  });

  it('keeps hidden self-message actions reachable by keyboard focus', () => {
    const selfFooterRule = ruleFor('.v3-message.is-self .v3-message-footer');
    const revealedFooterRule = ruleFor(
      '.v3-message.is-self:hover .v3-message-footer,\n.v3-message.is-self:focus-within .v3-message-footer',
    );

    expect(selfFooterRule).toContain('opacity: 0;');
    expect(selfFooterRule).not.toContain('visibility: hidden;');
    expect(revealedFooterRule).toContain('opacity: 1;');
    expect(revealedFooterRule).toContain('pointer-events: auto;');
  });

  it('gently pulses a working agent avatar without a clipped glow', () => {
    const workingAvatarRule = ruleFor('.v3-message.is-working .v3-avatar');

    expect(workingAvatarRule).toContain('filter: brightness(0.86) saturate(0.9);');
    expect(workingAvatarRule).toContain('box-shadow: none;');
    expect(workingAvatarRule).toContain('animation: cc-agent-avatar-breathe 1800ms ease-in-out infinite;');
    expect(workingAvatarRule).toContain('will-change: opacity;');
    expect(css).not.toContain('@keyframes cc-agent-activity-halo');
    expect(css).toContain('@keyframes cc-agent-avatar-breathe');
    expect(css).toMatch(
      /@keyframes cc-agent-avatar-breathe[\s\S]*?opacity: 0\.82;[\s\S]*?opacity: 1;/,
    );
    expect(css).toMatch(
      /@media \(prefers-reduced-motion: reduce\)[\s\S]*?\.v3-message\.is-working \.v3-avatar\s*\{[^}]*animation: none;/,
    );
  });

  it('uses a lighter self-message bubble in light mode and a deeper one in dark mode', () => {
    expect(ruleFor(':root')).toContain('--cc-self-message: #efeff0;');
    expect(ruleFor('html[data-theme="dark"]')).toContain('--cc-self-message: #303032;');
    expect(ruleFor('.v3-message.is-self .v3-message-bubble'))
      .toContain('background: var(--cc-self-message);');
  });

  it('renders sent files as a compact neutral card', () => {
    const cardRule = ruleFor('.v3-message .v3-attachment-card.v3-artifact-card');
    const iconRule = ruleFor('.v3-message .v3-attachment-icon');
    const actionRule = ruleFor('.v3-message .v3-artifact-action');

    expect(cardRule).toContain('width: min(390px, 100%);');
    expect(cardRule).toContain('background: var(--cc-panel);');
    expect(cardRule).toContain('border: 1px solid var(--cc-border);');
    expect(iconRule).toContain('width: 30px;');
    expect(iconRule).toContain('height: 30px;');
    expect(actionRule).toContain('height: 28px;');
    expect(actionRule).toContain('background: transparent;');
    expect(css).toContain('.v3-message.is-self.has-file-only .v3-message-bubble');
  });

  it('separates delivered files from the completion summary without splitting the reply', () => {
    const contentRule = ruleFor('.v3-message.artifacts-first .v3-message-content');
    const deliverablesRule = ruleFor('.v3-message.artifacts-first .v3-message-deliverables');
    const summaryRule = ruleFor('.v3-message.artifacts-first .v3-message-followup-text');
    const adjacentCardRule = ruleFor(
      '.oc-working-group + .v3-message.artifacts-first .v3-attachment-card.v3-artifact-card',
    );

    expect(contentRule).toContain('display: flex;');
    expect(contentRule).toContain('flex-direction: column;');
    expect(deliverablesRule).toContain('width: 100%;');
    expect(deliverablesRule).toContain('gap: 8px;');
    expect(summaryRule).toContain('width: 100%;');
    expect(summaryRule).toContain('margin-top: 14px;');
    expect(adjacentCardRule).toContain('margin-bottom: 0;');
  });

  it('presents confirmations as a compact neutral dialog with a clear primary action', () => {
    const overlayRule = ruleFor('.cc-confirm-overlay');
    const dialogRule = ruleFor('.oc-modal.cc-confirm-dialog');
    const cancelRule = ruleFor('.cc-confirm-cancel');
    const submitRule = ruleFor('.cc-confirm-submit');
    const liquidDialogRule = ruleFor('html[data-theme="liquid"] .cc-confirm-dialog');

    expect(overlayRule).toContain('background: rgba(0, 0, 0, 0.72);');
    expect(dialogRule).toContain('width: min(420px, calc(100vw - 32px));');
    expect(dialogRule).toContain('border-radius: 16px !important;');
    expect(dialogRule).toContain('border: 1px solid var(--cc-border) !important;');
    expect(dialogRule).toContain('background: var(--cc-panel) !important;');
    expect(cancelRule).toContain('background: var(--cc-panel-raised);');
    expect(cancelRule).toContain('border: 1px solid var(--cc-border-strong);');
    expect(submitRule).toContain('background: var(--cc-accent);');
    expect(ruleFor('.cc-confirm-submit.is-danger'))
      .toContain('background: color-mix(in srgb, var(--cc-danger) 16%, var(--cc-panel));');
    expect(liquidDialogRule).toContain('border-color: rgba(112, 119, 139, 0.22) !important;');
    expect(liquidDialogRule).toContain('background: rgba(255, 255, 255, 0.96) !important;');
    expect(liquidDialogRule).toContain('saturate(100%)');
    expect(css).toContain('@keyframes cc-confirm-enter');
  });

  it('uses the same solid online color for friend and task icons while keeping agents outlined', () => {
    const friendRule = ruleFor('.cc-contact-item .cc-friend-contact-icon.online');
    const taskRule = ruleFor('.cc-task-agent-icon.online');

    [friendRule, taskRule].forEach((rule) => {
      expect(rule).toContain('color: var(--cc-online-icon);');
      expect(rule).toContain('stroke: var(--cc-online-icon);');
      expect(rule).toContain('fill: var(--cc-online-icon);');
    });
    expect(friendRule).toContain('fill-opacity: 1;');
    expect(taskRule).toContain('fill-opacity: 1;');
    expect(taskRule).toContain('opacity: 1;');
    expect(ruleFor(':root')).toContain('--cc-online-icon: #29bc95;');
    expect(ruleFor('html[data-theme="liquid"]')).toContain('--cc-online-icon: #5662d9;');
    expect(ruleFor('html[data-theme="liquid"]')).toContain('--cc-offline-icon: #98a2b7;');
    expect(ruleFor('.cc-contact-item .cc-agent-contact-icon.online')).toContain('stroke: var(--cc-online-icon);');
    expect(ruleFor('.cc-contact-item .cc-agent-contact-icon.online')).toContain('fill: none;');
    const liquidOfflineRule = ruleFor('html[data-theme="liquid"] :is(\n  .cc-friend-contact-icon.offline,\n  .cc-agent-contact-icon.offline,\n  .cc-task-agent-icon.offline\n)');
    expect(liquidOfflineRule).toContain('color: var(--cc-offline-icon);');
    expect(liquidOfflineRule).toContain('stroke: var(--cc-offline-icon);');
    expect(liquidOfflineRule).toContain('fill: none;');
    expect(liquidOfflineRule).toContain('opacity: 1;');
  });

  it('keeps the member search surface unified and clearly marks the active member type', () => {
    const searchInputRule = ruleFor('.oc-member-search input');
    const searchFocusRule = ruleFor('.oc-member-search input:focus,\n.oc-member-search input:focus-visible');
    const activeTypeRule = ruleFor('.oc-member-search .oc-segmented-control button.active');
    const groupNameFocusRule = ruleFor('.oc-create-group-dialog .oc-collaboration-input:focus-visible');

    expect(searchInputRule).toContain('background: transparent !important;');
    expect(searchInputRule).toContain('box-shadow: none !important;');
    expect(searchFocusRule).toContain('background: transparent !important;');
    expect(activeTypeRule).toContain('var(--cc-accent)');
    expect(activeTypeRule).toContain('color: #fff;');
    expect(groupNameFocusRule).toContain('outline: 0;');
    expect(groupNameFocusRule).toContain('box-shadow: none;');
  });

  it('uses a fixed, aligned friend search-mode listbox with neutral option feedback', () => {
    const triggerRule = ruleFor('.oc-friend-search-mode-trigger');
    const menuRule = ruleFor('.oc-friend-search-mode-menu');
    const optionRule = ruleFor('.oc-friend-search-mode-option');
    const optionHoverRule = ruleFor('.oc-friend-search-mode-option:is(:hover, .is-active)');

    expect(triggerRule).toContain('width: 100%;');
    expect(triggerRule).toContain('padding: 0 14px 0 10px;');
    expect(menuRule).toContain('position: fixed;');
    expect(menuRule).toContain('z-index: 2600;');
    expect(menuRule).toContain('overflow: hidden;');
    expect(menuRule).toContain('background: var(--cc-panel-raised);');
    expect(optionRule).toContain('min-height: 34px;');
    expect(optionHoverRule).toContain('background: var(--cc-hover);');
  });

  it('reserves the friend-request empty-state height while pending requests load', () => {
    expect(ruleFor('.oc-friend-requests-section .oc-collaboration-list'))
      .toContain('min-height: 66px;');
    expect(ruleFor('.oc-friend-requests-section .oc-collaboration-empty'))
      .toContain('align-content: center;');
  });

  it('sizes the assistant role menu like the surrounding form controls', () => {
    expect(ruleFor('.cc-agent-role-field')).toContain('position: relative;');
    expect(ruleFor('.cc-agent-basic-card .cc-agent-role-select .v3-custom-model-select-trigger'))
      .toContain('min-height: 40px;');
    expect(ruleFor('.cc-agent-basic-card .cc-agent-role-select .v3-custom-model-select-options'))
      .toContain('padding: 5px;');
    expect(ruleFor('.cc-agent-basic-card .cc-agent-role-select .v3-custom-model-select-option'))
      .toContain('min-height: 36px;');
  });

  it('keeps every assistant-manager tab stable and shows four assistants before the hub grid scrolls', () => {
    const managerRule = ruleFor('.oc-modal.cc-agent-manager');
    const bodyRule = ruleFor('.cc-agent-manager-body');
    const hubRule = ruleFor('.cc-agent-hub');
    const hubGridRule = ruleFor('.cc-agent-hub-grid');

    expect(managerRule).toContain('display: flex;');
    expect(managerRule).toContain('height: min(760px, calc(100vh - 32px));');
    expect(bodyRule).toContain('flex: 1 1 auto;');
    expect(bodyRule).toContain('min-height: 0;');
    expect(hubRule).toContain('grid-template-rows: auto auto auto;');
    expect(hubRule).toContain('align-content: start;');
    expect(hubRule).toContain('min-height: 0;');
    expect(hubRule).not.toContain('height: 100%;');
    expect(hubGridRule).toContain('overflow-y: auto;');
    expect(hubGridRule).toContain('max-height: min(460px, 56vh);');
    expect(ruleFor('.cc-agent-overview-stats strong'))
      .toContain('font-variant-numeric: tabular-nums;');
    expect(css).toMatch(/\.cc-agent-usage-guide\s*\{[^}]*margin-top: 8px;/);
    expect(css).toMatch(
      /\.cc-agent-usage-heading\s*\{[^}]*justify-items: center;[^}]*text-align: center;/,
    );
    expect(ruleFor('.cc-agent-usage-items small')).toContain('font-size: 12px;');
  });

  it('uses the existing narrow preview breakpoint and keeps the file preview sheet inside short viewports', () => {
    expect(css).toContain('@media (max-width: 1024px) {');
    expect(css).not.toContain('@container catsco-main (max-width: 719px)');
    expect(css).toContain('grid-template-rows: minmax(0, 1fr);');
    expect(css).toContain('height: min(68svh, 620px, calc(100svh - max(76px, env(safe-area-inset-top)) - max(10px, env(safe-area-inset-bottom))));');
    expect(css).not.toContain('min-height: 380px;');
  });

  it('removes file preview sheet dismissal transitions for reduced motion', () => {
    expect(css).toContain(`@media (prefers-reduced-motion: reduce) {
    .v3-file-preview-backdrop,
    .v3-file-preview-panel,
    .v3-file-preview-panel.is-dismissing {
      transition: none;
    }`);
  });

  it('keeps PWA download notices stacked above modal overlays', () => {
    const hostRule = ruleFor('.catsco-pwa-download-notice-host');

    expect(hostRule).toContain('display: grid;');
    expect(hostRule).toContain('z-index: 2800;');
    expect(openchatCss).toMatch(/\.oc-modal-overlay\s*\{[^}]*z-index: 1000;/);
  });

});
