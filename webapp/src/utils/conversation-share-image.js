const DEFAULT_WIDTH = 720;
const DEFAULT_SCALE = 1.5;
// Keep long shares within a scanner-friendly page height as well as browser
// canvas limits. Their content remains complete across the resulting pages.
const MAX_OUTPUT_HEIGHT = 7680;
const MAX_OUTPUT_PIXELS = 18_000_000;
// This fixed QR matrix encodes https://app.catsco.cc. The four-module quiet zone
// is part of the matrix so the share-image background provides the surrounding contrast.
const APP_ENTRY_QR_MODULES = [
  '000000000000000000000000000000000',
  '000000000000000000000000000000000',
  '000000000000000000000000000000000',
  '000000000000000000000000000000000',
  '000011111110100000100011111110000',
  '000010000010100100001010000010000',
  '000010111010100011101010111010000',
  '000010111010011011101010111010000',
  '000010111010101111101010111010000',
  '000010000010001111100010000010000',
  '000011111110101010101011111110000',
  '000000000000011101110000000000000',
  '000010011111111100110100101110000',
  '000001111000101010110101111100000',
  '000001010111010001110111110010000',
  '000011011000000010100110011110000',
  '000000001111011010010011000010000',
  '000011101000100001111100100100000',
  '000011001111011110010010111110000',
  '000010100100101100001011011010000',
  '000010100011101000111111101100000',
  '000000000000101111001000101100000',
  '000011111110111100001010100010000',
  '000010000010100010111000100110000',
  '000010111010111011111111100110000',
  '000010111010100000110010000110000',
  '000010111010011010110100111110000',
  '000010000010001000100001101110000',
  '000011111110111001101100010010000',
  '000000000000000000000000000000000',
  '000000000000000000000000000000000',
  '000000000000000000000000000000000',
  '000000000000000000000000000000000',
];
// Keep the full 33×33 matrix, including its four-module quiet zone. At the
// minimum 1.5× export density, four logical pixels per module still yields
// six physical pixels per module for reliable scanning without overpowering
// the footer.
const APP_ENTRY_QR_MODULE_SIZE = 4;
const APP_ENTRY_QR_SIZE = APP_ENTRY_QR_MODULES.length * APP_ENTRY_QR_MODULE_SIZE;

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
    .filter((block) => ['text', 'image', 'file'].includes(block?.type))
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

function drawAppEntryQRCode(ctx, x, y, palette) {
  ctx.fillStyle = palette.text;
  APP_ENTRY_QR_MODULES.forEach((row, rowIndex) => {
    Array.from(row).forEach((module, columnIndex) => {
      if (module !== '1') return;
      ctx.fillRect(
        x + columnIndex * APP_ENTRY_QR_MODULE_SIZE,
        y + rowIndex * APP_ENTRY_QR_MODULE_SIZE,
        APP_ENTRY_QR_MODULE_SIZE,
        APP_ENTRY_QR_MODULE_SIZE,
      );
    });
  });
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
  // The QR and CatsCo label form one compact, right-aligned footer group. Its
  // full quiet zone starts below the divider, leaving both elements clear.
  const footerHeight = 184;
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
  // Keep a QR-sized export at the normal 1.5x density. Allowing pages to
  // shrink further makes a valid QR too small for scanners to find inside a
  // very tall PNG.
  const minimumPageOutputScale = Math.min(requestedScale, DEFAULT_SCALE);
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
    MAX_OUTPUT_HEIGHT / minimumPageOutputScale,
    MAX_OUTPUT_PIXELS / (width * minimumPageOutputScale * minimumPageOutputScale),
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
      && outputScaleForHeight(heightForLayouts(nextPageLayouts)) < minimumPageOutputScale
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
    if (!Number.isFinite(outputScale) || outputScale < minimumPageOutputScale) {
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

    const footerTop = height - padding - footerHeight;
    pageCtx.strokeStyle = palette.border;
    pageCtx.lineWidth = 1;
    pageCtx.beginPath();
    pageCtx.moveTo(padding, footerTop + 16);
    pageCtx.lineTo(width - padding, footerTop + 16);
    pageCtx.stroke();
    const qrY = footerTop + 32;
    const qrLabelCenterY = qrY + APP_ENTRY_QR_SIZE / 2;
    const qrX = width - padding - APP_ENTRY_QR_SIZE;
    const qrLabelRight = qrX - 18;
    const footerInfoMaxWidth = Math.max(1, qrX - padding - 72);
    const showFooterDetails = footerInfoMaxWidth >= 220;
    if (showFooterDetails) {
      pageCtx.fillStyle = palette.text;
      pageCtx.font = '600 22px "Inter Variable", "Noto Sans SC", sans-serif';
      pageCtx.fillText(
        fitText(pageCtx, '对话已整理为可分享图片', footerInfoMaxWidth),
        padding,
        qrLabelCenterY - 5,
      );
      pageCtx.fillStyle = palette.muted;
      pageCtx.font = '400 15px "Inter Variable", "Noto Sans SC", sans-serif';
      pageCtx.fillText(
        fitText(
          pageCtx,
          `${normalizedItems.length} 条消息 · 保留发送者、时间与附件标签`,
          footerInfoMaxWidth,
        ),
        padding,
        qrLabelCenterY + 20,
      );
    } else {
      pageCtx.fillStyle = palette.muted;
      pageCtx.font = '400 16px "Inter Variable", "Noto Sans SC", sans-serif';
      pageCtx.fillText(
        fitText(pageCtx, '由 CatsCo 生成 · 保留对话上下文', footerInfoMaxWidth),
        padding,
        height - padding - 20,
      );
    }
    pageCtx.textAlign = 'right';
    pageCtx.fillStyle = palette.accentText;
    pageCtx.font = '600 24px "Inter Variable", "Noto Sans SC", sans-serif';
    pageCtx.fillText('CatsCo', qrLabelRight, qrLabelCenterY - 5);
    pageCtx.fillStyle = palette.muted;
    pageCtx.font = '400 14px "Inter Variable", "Noto Sans SC", sans-serif';
    pageCtx.fillText('app.catsco.cc', qrLabelRight, qrLabelCenterY + 20);
    pageCtx.textAlign = 'left';

    drawAppEntryQRCode(pageCtx, qrX, qrY, palette);

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

function utf8Bytes(value) {
  if (typeof TextEncoder === 'function') return new TextEncoder().encode(value);
  const encoded = encodeURIComponent(value);
  const bytes = [];
  for (let index = 0; index < encoded.length; index += 1) {
    if (encoded[index] === '%') {
      bytes.push(Number.parseInt(encoded.slice(index + 1, index + 3), 16));
      index += 2;
    } else {
      bytes.push(encoded.charCodeAt(index));
    }
  }
  return Uint8Array.from(bytes);
}

function dataURLBytes(dataUrl) {
  if (typeof dataUrl !== 'string' || !dataUrl.startsWith('data:')) return null;
  const separatorIndex = dataUrl.indexOf(',');
  if (separatorIndex < 0) return null;

  const metadata = dataUrl.slice(5, separatorIndex);
  const payload = dataUrl.slice(separatorIndex + 1);
  const mimeType = metadata.split(';')[0] || 'application/octet-stream';
  try {
    if (metadata.includes(';base64')) {
      if (typeof atob !== 'function') return null;
      const binary = atob(payload);
      const bytes = new Uint8Array(binary.length);
      for (let index = 0; index < binary.length; index += 1) {
        bytes[index] = binary.charCodeAt(index);
      }
      return { bytes, mimeType };
    }
    return { bytes: utf8Bytes(decodeURIComponent(payload)), mimeType };
  } catch {
    return null;
  }
}

function blobFromDataURL(dataUrl) {
  const parsed = dataURLBytes(dataUrl);
  return parsed ? blobFromBytes(parsed.bytes, parsed.mimeType) : null;
}

function blobFromBytes(bytes, mimeType) {
  if (!bytes || typeof Blob !== 'function') return null;
  try {
    return new Blob([bytes], { type: mimeType });
  } catch {
    return null;
  }
}

function imageFileFromBlob(blob, filename) {
  if (typeof File !== 'function') return null;
  return new File([blob], filename, { type: blob.type || 'image/png' });
}

const CRC32_TABLE = Array.from({ length: 256 }, (_, index) => {
  let value = index;
  for (let bit = 0; bit < 8; bit += 1) {
    value = (value & 1) ? ((value >>> 1) ^ 0xedb88320) : (value >>> 1);
  }
  return value >>> 0;
});

function crc32(bytes) {
  let value = 0xffffffff;
  for (const byte of bytes) {
    value = (value >>> 8) ^ CRC32_TABLE[(value ^ byte) & 0xff];
  }
  return (value ^ 0xffffffff) >>> 0;
}

function zipFilenameBytes(filename) {
  if (typeof TextEncoder === 'function') return new TextEncoder().encode(filename);
  return Uint8Array.from(Array.from(filename), (character) => character.charCodeAt(0) & 0xff);
}

function createZipHeader({ central, nameLength, crc, size, offset = 0 }) {
  const header = new Uint8Array(central ? 46 : 30);
  const view = new DataView(header.buffer);
  view.setUint32(0, central ? 0x02014b50 : 0x04034b50, true);
  if (central) {
    view.setUint16(4, 20, true);
    view.setUint16(6, 20, true);
    view.setUint16(8, 0, true);
    view.setUint16(10, 0, true);
    view.setUint16(12, 0, true);
    view.setUint16(14, 0, true);
    view.setUint32(16, crc, true);
    view.setUint32(20, size, true);
    view.setUint32(24, size, true);
    view.setUint16(28, nameLength, true);
    view.setUint16(30, 0, true);
    view.setUint16(32, 0, true);
    view.setUint16(34, 0, true);
    view.setUint16(36, 0, true);
    view.setUint32(38, 0, true);
    view.setUint32(42, offset, true);
  } else {
    view.setUint16(4, 20, true);
    view.setUint16(6, 0, true);
    view.setUint16(8, 0, true);
    view.setUint16(10, 0, true);
    view.setUint16(12, 0, true);
    view.setUint32(14, crc, true);
    view.setUint32(18, size, true);
    view.setUint32(22, size, true);
    view.setUint16(26, nameLength, true);
    view.setUint16(28, 0, true);
  }
  return header;
}

function createZipBlob(entries) {
  // PNGs are already compressed; store-only ZIP entries keep this synchronous
  // so the one download click remains inside the user's gesture.
  if (
    typeof Blob !== 'function'
    || !Array.isArray(entries)
    || entries.length === 0
    || entries.length > 0xffff
  ) return null;
  try {
    const localChunks = [];
    const centralChunks = [];
    let localOffset = 0;
    for (const entry of entries) {
      if (!entry?.bytes) return null;
      const nameBytes = zipFilenameBytes(entry.archiveName || entry.name);
      const bytes = entry.bytes;
      if (nameBytes.length > 0xffff || bytes.length > 0xffffffff || localOffset > 0xffffffff) return null;
      const checksum = crc32(bytes);
      const localHeader = createZipHeader({
        central: false,
        nameLength: nameBytes.length,
        crc: checksum,
        size: bytes.length,
      });
      const centralHeader = createZipHeader({
        central: true,
        nameLength: nameBytes.length,
        crc: checksum,
        size: bytes.length,
        offset: localOffset,
      });
      localChunks.push(localHeader, nameBytes, bytes);
      centralChunks.push(centralHeader, nameBytes);
      localOffset += localHeader.length + nameBytes.length + bytes.length;
      if (localOffset > 0xffffffff) return null;
    }

    const centralSize = centralChunks.reduce((sum, chunk) => sum + chunk.length, 0);
    if (centralSize > 0xffffffff) return null;
    const endOfCentralDirectory = new Uint8Array(22);
    const endView = new DataView(endOfCentralDirectory.buffer);
    endView.setUint32(0, 0x06054b50, true);
    endView.setUint16(8, entries.length, true);
    endView.setUint16(10, entries.length, true);
    endView.setUint32(12, centralSize, true);
    endView.setUint32(16, localOffset, true);

    return new Blob([...localChunks, ...centralChunks, endOfCentralDirectory], {
      type: 'application/zip',
    });
  } catch {
    return null;
  }
}

function safeDownloadPrefix(value) {
  const prefix = String(value || 'catsco-conversation-share')
    .replace(/[^a-zA-Z0-9._-]+/g, '-')
    .replace(/^[.-]+|[.-]+$/g, '');
  return prefix || 'catsco-conversation-share';
}

function downloadFilenamePrefix(value) {
  const prefix = String(value || 'catsco-conversation-share')
    .replace(/[\\/:*?"<>|\u0000-\u001f]+/g, '-')
    .replace(/^[-.]+|[-.]+$/g, '')
    .trim();
  return prefix || 'catsco-conversation-share';
}

function isMobileBrowser() {
  if (typeof navigator === 'undefined') return false;
  if (navigator.userAgentData?.mobile === true) return true;
  const userAgent = String(navigator.userAgent || '');
  return /Android|webOS|iPhone|iPad|iPod|BlackBerry|IEMobile|Opera Mini/i.test(userAgent)
    || (/Macintosh/i.test(userAgent) && Number(navigator.maxTouchPoints) > 1);
}

function startNativeImageShare(files) {
  if (
    !isMobileBrowser()
    || typeof navigator.share !== 'function'
    || typeof navigator.canShare !== 'function'
    || files.length === 0
  ) return null;

  const shareData = {
    files,
    title: 'CatsCo 对话分享图',
  };
  try {
    if (!navigator.canShare(shareData)) return null;
    return Promise.resolve(navigator.share(shareData))
      .then(() => true)
      // Closing the system sheet is an intentional cancellation, not a failed
      // save action that should surface as an error in the conversation.
      .catch((error) => (error?.name === 'AbortError' ? true : null));
  } catch (error) {
    return error?.name === 'AbortError' ? true : null;
  }
}

function startDirectImageDownload(blob, filename) {
  if (
    typeof document === 'undefined'
    || typeof URL === 'undefined'
    || typeof URL.createObjectURL !== 'function'
  ) return false;

  const objectURL = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = objectURL;
  link.download = filename;
  link.rel = 'noopener';
  link.style.display = 'none';
  document.body.appendChild(link);
  try {
    link.click();
  } catch {
    link.remove();
    URL.revokeObjectURL?.(objectURL);
    return false;
  }
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL?.(objectURL), 60_000);
  return true;
}

function openImageForManualSave(blob) {
  if (
    typeof window === 'undefined'
    || typeof URL === 'undefined'
    || typeof URL.createObjectURL !== 'function'
  ) return false;

  let objectURL;
  try {
    objectURL = URL.createObjectURL(blob);
  } catch {
    return false;
  }

  let imageWindow;
  try {
    imageWindow = window.open(objectURL, '_blank');
  } catch {
    URL.revokeObjectURL?.(objectURL);
    return false;
  }
  if (!imageWindow) {
    URL.revokeObjectURL?.(objectURL);
    return false;
  }
  try {
    imageWindow.opener = null;
  } catch {
    // Some browsers expose a cross-origin WindowProxy here. The image is
    // still open and can be saved with the browser's native controls.
  }
  window.setTimeout(() => URL.revokeObjectURL?.(objectURL), 300_000);
  return true;
}

export function openConversationShareImageForManualSave(dataUrl) {
  const blob = blobFromDataURL(dataUrl);
  return blob ? openImageForManualSave(blob) : false;
}

export async function downloadConversationShareImage(dataUrl, filename = 'catsco-conversation-share.png') {
  const blob = blobFromDataURL(dataUrl);
  if (!blob) return false;

  const imageFile = imageFileFromBlob(blob, filename);
  const nativeShare = imageFile ? startNativeImageShare([imageFile]) : null;
  if (nativeShare) {
    // A Web Share rejection can settle after transient user activation expires.
    // Return control so the caller can offer an explicit manual-save click.
    return (await nativeShare) === true;
  }

  // iOS and some embedded mobile browsers ignore synthetic download clicks.
  // When native file sharing is unavailable, show the image in a real tab so
  // the user can still save it with the platform's long-press/save controls.
  if (isMobileBrowser()) return openImageForManualSave(blob);
  return startDirectImageDownload(blob, filename);
}

export async function downloadConversationShareImages(dataUrls, filenamePrefix = 'catsco-conversation-share') {
  const urls = Array.isArray(dataUrls) ? dataUrls.filter(Boolean) : [];
  if (urls.length === 0) return false;
  if (urls.length === 1) return downloadConversationShareImage(urls[0], `${filenamePrefix}.png`);

  const mobileBrowser = isMobileBrowser();
  const originalPrefix = String(filenamePrefix || 'catsco-conversation-share');
  const downloadPrefix = downloadFilenamePrefix(filenamePrefix);
  const safePrefix = safeDownloadPrefix(filenamePrefix);
  const entries = urls.map((dataUrl, index) => {
    const parsed = dataURLBytes(dataUrl);
    const suffix = `-${String(index + 1).padStart(2, '0')}`;
    return parsed
      ? {
        bytes: parsed.bytes,
        mimeType: parsed.mimeType,
        name: `${originalPrefix}${suffix}.png`,
        archiveName: `${safePrefix}${suffix}.png`,
      }
      : null;
  });
  if (entries.some((entry) => !entry)) return false;

  if (mobileBrowser) {
    const files = entries.map((entry) => {
      const blob = blobFromBytes(entry.bytes, entry.mimeType);
      return blob ? imageFileFromBlob(blob, entry.name) : null;
    });
    const nativeShare = files.every(Boolean) ? startNativeImageShare(files) : null;
    if (nativeShare && (await nativeShare) === true) return true;
  }
  // Browsers usually block a burst of mobile downloads. The UI retains a
  // one-page action and explains this fallback when a multi-file share isn't
  // supported.
  if (mobileBrowser) return false;

  const zipBlob = createZipBlob(entries);
  return zipBlob ? startDirectImageDownload(zipBlob, `${downloadPrefix}.zip`) : false;
}
