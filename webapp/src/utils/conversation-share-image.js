const DEFAULT_WIDTH = 720;
const DEFAULT_SCALE = 1.5;
const MIN_OUTPUT_SCALE = 0.75;
// Keep long 50-message shares usable without allocating an unbounded canvas.
const MAX_OUTPUT_HEIGHT = 9600;
const MAX_OUTPUT_PIXELS = 18_000_000;

export const CONVERSATION_SHARE_IMAGE_WIDTH = DEFAULT_WIDTH;

const PALETTES = {
  light: {
    canvas: '#f5f8f6',
    panel: '#fbfdfc',
    border: '#d9e8e1',
    text: '#1d2926',
    muted: '#64736e',
    accent: '#3ab292',
    accentText: '#267a65',
    selfBubble: '#dff4eb',
    peerBubble: '#fbfdfc',
  },
  dark: {
    canvas: '#14201d',
    panel: '#1b2a26',
    border: '#30453e',
    text: '#e7f1ed',
    muted: '#9bb1a9',
    accent: '#52d2ac',
    accentText: '#8ae6c9',
    selfBubble: '#234f42',
    peerBubble: '#1b2a26',
  },
  liquid: {
    canvas: '#f2f4fc',
    panel: '#fbfbff',
    border: '#d8ddf5',
    text: '#1d2741',
    muted: '#64708c',
    accent: '#5662d9',
    accentText: '#4652c5',
    selfBubble: '#e6eaff',
    peerBubble: '#fbfbff',
  },
  liquidGreen: {
    canvas: '#151718',
    panel: '#1a1c1d',
    border: '#33463f',
    text: '#f1f8f5',
    muted: '#b8cbc3',
    accent: '#3ab292',
    accentText: '#8ce8c9',
    selfBubble: '#21463a',
    peerBubble: '#1a1c1d',
  },
};

function safeString(value) {
  return typeof value === 'string' ? value : '';
}

function contentBlockText(block) {
  if (!block || typeof block !== 'object') return '';
  if (block.type === 'text') return safeString(block.text || block.content);
  const payload = block.payload || block;
  if (block.type === 'image') return `[图片] ${safeString(payload.name) || '图片附件'}`;
  if (block.type === 'file') return `[文件] ${safeString(payload.name) || '文件附件'}`;
  if (block.type === 'audio' || block.type === 'voice') return `[音频] ${safeString(payload.name) || '音频附件'}`;
  if (block.type === 'video') return `[视频] ${safeString(payload.name) || '视频附件'}`;
  return '';
}

function structuredContentParts(content) {
  let value = content;
  const sourceWasString = typeof value === 'string';
  if (sourceWasString) {
    try {
      value = JSON.parse(value);
    } catch {
      return [content];
    }
  }
  if (typeof value === 'string') return [value];
  if (!value || typeof value !== 'object') return [];

  const blocks = Array.isArray(value.content_blocks)
    ? value.content_blocks
    : (Array.isArray(value.contentBlocks) ? value.contentBlocks : []);
  const blockParts = blocks.map(contentBlockText).filter(Boolean);
  if (blockParts.length > 0) return blockParts;

  const attachmentLabel = contentBlockText(value);
  if (attachmentLabel) return [attachmentLabel];

  const text = safeString(value.text || value.content);
  if (text) return [text];
  return sourceWasString ? [content] : [];
}

export function conversationShareText(message) {
  const blocks = Array.isArray(message?.content_blocks) ? message.content_blocks : [];
  const blockParts = blocks
    .filter((block) => ['text', 'image', 'file', 'audio', 'voice', 'video'].includes(block?.type))
    .map(contentBlockText)
    .filter(Boolean);
  const parts = blockParts.length > 0
    ? blockParts
    : structuredContentParts(message?.content);
  return parts.join('\n').replace(/\n{3,}/g, '\n\n').trim();
}

export function conversationShareMessageKey(message, index = 0) {
  const raw = message?.id ?? message?.seq_id ?? message?.created_at;
  return String(raw || `share-message-${index}`);
}

function dateLabel(value) {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date);
}

function resolvePalette(theme) {
  if (theme === 'dark') return PALETTES.dark;
  if (theme === 'liquid-green') return PALETTES.liquidGreen;
  if (theme === 'liquid') return PALETTES.liquid;
  return PALETTES.light;
}

function roundedRect(ctx, x, y, width, height, radius, fill, stroke, lineWidth = 1) {
  const r = Math.min(radius, width / 2, height / 2);
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + width, y, x + width, y + height, r);
  ctx.arcTo(x + width, y + height, x, y + height, r);
  ctx.arcTo(x, y + height, x, y, r);
  ctx.arcTo(x, y, x + width, y, r);
  ctx.closePath();
  if (fill) {
    ctx.fillStyle = fill;
    ctx.fill();
  }
  if (stroke) {
    ctx.strokeStyle = stroke;
    ctx.lineWidth = lineWidth;
    ctx.stroke();
  }
}

function wrapText(ctx, text, maxWidth) {
  const lines = [];
  const paragraphs = String(text || '').split(/\r?\n/);
  paragraphs.forEach((paragraph) => {
    if (!paragraph) {
      lines.push('');
      return;
    }
    let line = '';
    for (const character of Array.from(paragraph)) {
      const next = line + character;
      if (line && ctx.measureText(next).width > maxWidth) {
        lines.push(line);
        line = character;
      } else {
        line = next;
      }
    }
    if (line) lines.push(line);
  });
  return lines.length > 0 ? lines : [''];
}

function fitText(ctx, text, maxWidth) {
  const value = String(text || '');
  if (!value || ctx.measureText(value).width <= maxWidth) return value;

  const ellipsis = '…';
  const characters = Array.from(value);
  let fitted = '';
  for (const character of characters) {
    const next = fitted + character;
    if (ctx.measureText(`${next}${ellipsis}`).width > maxWidth) break;
    fitted = next;
  }
  return fitted ? `${fitted}${ellipsis}` : ellipsis;
}

function loadImage(src) {
  if (typeof Image === 'undefined' || !src) return Promise.resolve(null);
  return new Promise((resolve) => {
    const image = new Image();
    image.onload = () => resolve(image);
    image.onerror = () => resolve(null);
    image.src = src;
  });
}

function normalizeItem(item, index) {
  const message = item?.message || item || {};
  const isSelf = Boolean(item?.isSelf);
  return {
    key: String(item?.key || conversationShareMessageKey(message, index)),
    message,
    isSelf,
    senderName: safeString(item?.senderName || message.from_name) || (isSelf ? '我' : 'CatsCo'),
    text: conversationShareText(message),
    time: dateLabel(message.created_at),
  };
}

function drawLogoFallback(ctx, x, y, size, palette) {
  ctx.save();
  ctx.strokeStyle = palette.accent;
  ctx.lineWidth = Math.max(2, size * 0.09);
  ctx.lineJoin = 'round';
  ctx.beginPath();
  ctx.moveTo(x + size * 0.08, y + size * 0.18);
  ctx.lineTo(x + size * 0.5, y + size * 0.68);
  ctx.lineTo(x + size * 0.92, y + size * 0.18);
  ctx.moveTo(x + size * 0.22, y + size * 0.44);
  ctx.lineTo(x + size * 0.5, y + size * 0.78);
  ctx.lineTo(x + size * 0.78, y + size * 0.44);
  ctx.stroke();
  ctx.restore();
}

export async function renderConversationShareImage({
  items = [],
  topicName = '对话',
  theme = 'light',
  logoUrl = '/catsco-brand-mark.webp',
  width = DEFAULT_WIDTH,
  scale = DEFAULT_SCALE,
} = {}) {
  if (typeof document === 'undefined' || typeof document.createElement !== 'function') {
    throw new Error('当前环境不支持图像生成');
  }
  const canvas = document.createElement('canvas');
  const ctx = canvas.getContext('2d');
  if (!ctx) throw new Error('当前浏览器不支持 Canvas');

  const palette = resolvePalette(theme);
  const normalizedItems = items.map(normalizeItem).filter((item) => item.text);
  if (normalizedItems.length === 0) throw new Error('至少选择一条有内容的消息');

  const padding = 56;
  const headerHeight = 128;
  const footerHeight = 72;
  const bubbleMaxWidth = Math.min(720, width - padding * 2);
  const bodyLineHeight = 28;
  const bubblePaddingX = 24;
  const bubblePaddingY = 18;
  const logo = await loadImage(logoUrl);

  ctx.font = '400 22px "Inter Variable", "Noto Sans SC", sans-serif';
  const layouts = normalizedItems.map((item) => {
    const lines = wrapText(ctx, item.text, bubbleMaxWidth - bubblePaddingX * 2);
    const bubbleWidth = Math.min(
      bubbleMaxWidth,
      Math.max(180, Math.max(...lines.map((line) => ctx.measureText(line || ' ').width)) + bubblePaddingX * 2),
    );
    const bubbleHeight = 60 + lines.length * bodyLineHeight + bubblePaddingY * 2;
    return { ...item, lines, bubbleWidth, bubbleHeight };
  });
  const gap = 28;
  const requestedScale = Number.isFinite(scale) && scale > 0 ? scale : DEFAULT_SCALE;
  const heightForLayouts = (pageLayouts) => (
    headerHeight
    + footerHeight
    + pageLayouts.reduce((sum, item) => sum + item.bubbleHeight + gap, 0)
    + padding * 2
  );
  const outputScaleForHeight = (height) => Math.min(
    requestedScale,
    MAX_OUTPUT_HEIGHT / height,
    Math.sqrt(MAX_OUTPUT_PIXELS / (width * height)),
  );
  const maxPageHeight = Math.min(
    MAX_OUTPUT_HEIGHT / MIN_OUTPUT_SCALE,
    MAX_OUTPUT_PIXELS / (width * MIN_OUTPUT_SCALE * MIN_OUTPUT_SCALE),
  );
  const maxBubbleHeight = Math.max(1, maxPageHeight - heightForLayouts([]) - gap);
  const maxLinesPerBubble = Math.max(
    1,
    Math.floor((maxBubbleHeight - 60 - bubblePaddingY * 2) / bodyLineHeight),
  );
  const paginatedLayouts = layouts.flatMap((item) => {
    if (item.lines.length <= maxLinesPerBubble) return [item];

    const fragments = [];
    for (let start = 0; start < item.lines.length; start += maxLinesPerBubble) {
      const lines = item.lines.slice(start, start + maxLinesPerBubble);
      fragments.push({
        ...item,
        lines,
        bubbleHeight: 60 + lines.length * bodyLineHeight + bubblePaddingY * 2,
      });
    }
    return fragments;
  });

  // Keep every page within the browser-safe canvas bounds. Long selections remain
  // complete by becoming a small sequence of share images instead of failing.
  const layoutPages = [];
  let currentPageLayouts = [];
  paginatedLayouts.forEach((item) => {
    const nextPageLayouts = [...currentPageLayouts, item];
    if (
      currentPageLayouts.length > 0
      && outputScaleForHeight(heightForLayouts(nextPageLayouts)) < MIN_OUTPUT_SCALE
    ) {
      layoutPages.push(currentPageLayouts);
      currentPageLayouts = [item];
      return;
    }
    currentPageLayouts = nextPageLayouts;
  });
  if (currentPageLayouts.length > 0) layoutPages.push(currentPageLayouts);

  const renderPage = (pageLayouts, pageIndex) => {
    const height = heightForLayouts(pageLayouts);
    const outputScale = outputScaleForHeight(height);
    if (!Number.isFinite(outputScale) || outputScale < MIN_OUTPUT_SCALE) {
      throw new Error('分享图尺寸超出浏览器安全范围，请稍后重试。');
    }
    const pageCanvas = pageIndex === 0 ? canvas : document.createElement('canvas');
    const pageCtx = pageIndex === 0 ? ctx : pageCanvas.getContext('2d');
    if (!pageCtx) throw new Error('当前浏览器不支持 Canvas');

    const outputWidth = Math.max(1, Math.round(width * outputScale));
    const outputHeight = Math.max(1, Math.round(height * outputScale));
    pageCanvas.width = outputWidth;
    pageCanvas.height = outputHeight;
    pageCanvas.style.width = `${width}px`;
    pageCanvas.style.height = `${height}px`;
    pageCtx.scale(outputScale, outputScale);

    pageCtx.fillStyle = palette.canvas;
    pageCtx.fillRect(0, 0, width, height);
    roundedRect(pageCtx, padding / 2, padding / 2, width - padding, height - padding, 28, palette.panel, palette.border, 1);

    const contentX = padding;
    const logoWidth = 64;
    const logoHeight = 24;
    if (logo) {
      pageCtx.save();
      if (theme === 'liquid') {
        pageCtx.filter = 'hue-rotate(68deg) saturate(1.05) brightness(0.78)';
      }
      pageCtx.drawImage(logo, contentX, padding + 18, logoWidth, logoHeight);
      pageCtx.restore();
    } else {
      drawLogoFallback(pageCtx, contentX, padding + 10, 40, palette);
    }
    pageCtx.fillStyle = palette.text;
    pageCtx.font = '600 30px "Inter Variable", "Noto Sans SC", sans-serif';
    pageCtx.fillText('CatsCo', contentX + 76, padding + 42);
    pageCtx.fillStyle = palette.muted;
    pageCtx.font = '400 18px "Inter Variable", "Noto Sans SC", sans-serif';
    pageCtx.fillText('对话分享', contentX + 76, padding + 70);
    pageCtx.textAlign = 'right';
    pageCtx.fillStyle = palette.text;
    pageCtx.font = '600 22px "Inter Variable", "Noto Sans SC", sans-serif';
    pageCtx.fillText(fitText(pageCtx, topicName || '对话', width * 0.42), width - padding, padding + 38);
    pageCtx.fillStyle = palette.muted;
    pageCtx.font = '400 16px "Inter Variable", "Noto Sans SC", sans-serif';
    const pageLabel = layoutPages.length > 1
      ? `${normalizedItems.length} 条消息 · ${pageIndex + 1}/${layoutPages.length}`
      : `${normalizedItems.length} 条消息`;
    pageCtx.fillText(pageLabel, width - padding, padding + 66);
    pageCtx.textAlign = 'left';

    let y = padding + headerHeight;
    pageLayouts.forEach((item) => {
      const bubbleX = item.isSelf ? width - padding - item.bubbleWidth : padding;
      roundedRect(
        pageCtx,
        bubbleX,
        y,
        item.bubbleWidth,
        item.bubbleHeight,
        18,
        item.isSelf ? palette.selfBubble : palette.peerBubble,
        item.isSelf ? palette.accent : palette.border,
        1,
      );
      pageCtx.fillStyle = item.isSelf ? palette.accentText : palette.text;
      pageCtx.font = '600 17px "Inter Variable", "Noto Sans SC", sans-serif';
      const timeWidth = pageCtx.measureText(item.time).width;
      const senderMaxWidth = Math.max(
        64,
        item.bubbleWidth - bubblePaddingX * 2 - (item.time ? timeWidth + 12 : 0),
      );
      pageCtx.fillText(fitText(pageCtx, item.senderName, senderMaxWidth), bubbleX + bubblePaddingX, y + 29);
      pageCtx.fillStyle = palette.muted;
      pageCtx.font = '400 15px "Inter Variable", "Noto Sans SC", sans-serif';
      if (item.time) pageCtx.fillText(item.time, bubbleX + item.bubbleWidth - bubblePaddingX - timeWidth, y + 29);
      pageCtx.fillStyle = palette.text;
      pageCtx.font = '400 22px "Inter Variable", "Noto Sans SC", sans-serif';
      item.lines.forEach((line, lineIndex) => {
        pageCtx.fillText(line, bubbleX + bubblePaddingX, y + 68 + lineIndex * bodyLineHeight);
      });
      y += item.bubbleHeight + gap;
    });

    pageCtx.strokeStyle = palette.border;
    pageCtx.lineWidth = 1;
    pageCtx.beginPath();
    pageCtx.moveTo(padding, height - padding - footerHeight + 16);
    pageCtx.lineTo(width - padding, height - padding - footerHeight + 16);
    pageCtx.stroke();
    pageCtx.fillStyle = palette.muted;
    pageCtx.font = '400 16px "Inter Variable", "Noto Sans SC", sans-serif';
    pageCtx.fillText('由 CatsCo 生成 · 保留对话上下文', padding, height - padding - 20);
    pageCtx.textAlign = 'right';
    pageCtx.fillStyle = palette.accentText;
    pageCtx.font = '600 17px "Inter Variable", "Noto Sans SC", sans-serif';
    pageCtx.fillText('CatsCo', width - padding, height - padding - 20);
    pageCtx.textAlign = 'left';

    return {
      dataUrl: pageCanvas.toDataURL('image/png'),
      width: pageCanvas.width,
      height: pageCanvas.height,
      page: pageIndex + 1,
      total: layoutPages.length,
    };
  };

  const pages = layoutPages.map(renderPage);
  return {
    ...pages[0],
    pages,
  };
}

export function downloadConversationShareImage(dataUrl, filename = 'catsco-conversation-share.png') {
  if (typeof document === 'undefined' || !dataUrl) return false;
  const link = document.createElement('a');
  link.href = dataUrl;
  link.download = filename;
  link.rel = 'noopener';
  link.style.display = 'none';
  document.body.appendChild(link);
  link.click();
  link.remove();
  return true;
}

export function downloadConversationShareImages(dataUrls, filenamePrefix = 'catsco-conversation-share') {
  const urls = Array.isArray(dataUrls) ? dataUrls.filter(Boolean) : [];
  if (urls.length === 0) return false;
  return urls.every((dataUrl, index) => {
    const suffix = urls.length > 1 ? `-${String(index + 1).padStart(2, '0')}` : '';
    return downloadConversationShareImage(dataUrl, `${filenamePrefix}${suffix}.png`);
  });
}
