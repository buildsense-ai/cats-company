import React, { useMemo } from 'react';

const VERSION = 4;
const SIZE = 17 + VERSION * 4;
const QUIET_ZONE = 4;
const VIEWBOX_SIZE = SIZE + QUIET_ZONE * 2;
const DATA_CODEWORDS = 80;
const EC_CODEWORDS = 20;
const MAX_BYTES = 78;

export default function QRCode({ value, size = 205 }) {
  const matrix = useMemo(() => {
    try {
      return createQRCodeMatrix(value || '');
    } catch (error) {
      return null;
    }
  }, [value]);

  if (!matrix) {
    return (
      <div style={{ width: size, height: size, display: 'flex', alignItems: 'center', justifyContent: 'center', textAlign: 'center', padding: 16, background: '#fff', color: '#111', borderRadius: 8, fontSize: 12 }}>
        Link too long for QR
      </div>
    );
  }

  const path = matrix
    .map((row, r) => row.map((dark, c) => (dark ? `M${c + QUIET_ZONE},${r + QUIET_ZONE}h1v1h-1z` : '')).join(''))
    .join('');

  return (
    <svg width={size} height={size} viewBox={`0 0 ${VIEWBOX_SIZE} ${VIEWBOX_SIZE}`} role="img" aria-label="QR code" shapeRendering="crispEdges" style={{ background: '#fff', borderRadius: 8 }}>
      <rect width={VIEWBOX_SIZE} height={VIEWBOX_SIZE} fill="#fff" />
      <path d={path} fill="#111" shapeRendering="crispEdges" />
    </svg>
  );
}

export function canEncodeQR(value) {
  return new TextEncoder().encode(value || '').length <= MAX_BYTES;
}

function createQRCodeMatrix(value) {
  const data = encodeData(value);
  const ec = reedSolomonRemainder(data, EC_CODEWORDS);
  const bits = toBits([...data, ...ec]);
  const modules = Array.from({ length: SIZE }, () => Array(SIZE).fill(false));
  const reserved = Array.from({ length: SIZE }, () => Array(SIZE).fill(false));

  const setFunction = (row, col, dark) => {
    if (row < 0 || row >= SIZE || col < 0 || col >= SIZE) return;
    modules[row][col] = dark;
    reserved[row][col] = true;
  };

  addFinder(modules, reserved, 0, 0);
  addFinder(modules, reserved, 0, SIZE - 7);
  addFinder(modules, reserved, SIZE - 7, 0);
  addAlignment(setFunction, 26, 26);
  addTiming(setFunction);
  reserveFormat(reserved);
  setFunction(4 * VERSION + 9, 8, true);

  placeData(modules, reserved, bits);
  writeFormat(modules, reserved, 0);
  modules[4 * VERSION + 9][8] = true;
  return modules;
}

function encodeData(value) {
  const bytes = Array.from(new TextEncoder().encode(value));
  if (bytes.length > MAX_BYTES) {
    throw new Error('QR value too long');
  }
  const bits = [];
  appendBits(bits, 0b0100, 4);
  appendBits(bits, bytes.length, 8);
  for (const byte of bytes) appendBits(bits, byte, 8);
  const capacity = DATA_CODEWORDS * 8;
  appendBits(bits, 0, Math.min(4, capacity - bits.length));
  while (bits.length % 8 !== 0) bits.push(0);

  const data = [];
  for (let i = 0; i < bits.length; i += 8) {
    let valueByte = 0;
    for (let j = 0; j < 8; j++) valueByte = (valueByte << 1) | bits[i + j];
    data.push(valueByte);
  }
  for (let pad = 0xec; data.length < DATA_CODEWORDS; pad ^= 0xfd) {
    data.push(pad);
  }
  return data;
}

function appendBits(bits, value, length) {
  for (let i = length - 1; i >= 0; i--) bits.push((value >>> i) & 1);
}

function toBits(codewords) {
  const bits = [];
  for (const codeword of codewords) appendBits(bits, codeword, 8);
  return bits;
}

const EXP = new Array(512);
const LOG = new Array(256);
let gfValue = 1;
for (let i = 0; i < 255; i++) {
  EXP[i] = gfValue;
  LOG[gfValue] = i;
  gfValue <<= 1;
  if (gfValue & 0x100) gfValue ^= 0x11d;
}
for (let i = 255; i < 512; i++) EXP[i] = EXP[i - 255];

function gfMultiply(a, b) {
  if (a === 0 || b === 0) return 0;
  return EXP[LOG[a] + LOG[b]];
}

function reedSolomonGenerator(degree) {
  const poly = new Array(degree).fill(0);
  poly[degree - 1] = 1;
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < poly.length; j++) {
      poly[j] = gfMultiply(poly[j], root);
      if (j + 1 < poly.length) poly[j] ^= poly[j + 1];
    }
    root = gfMultiply(root, 2);
  }
  return poly;
}

function reedSolomonRemainder(data, degree) {
  const generator = reedSolomonGenerator(degree);
  const result = new Array(degree).fill(0);
  for (const byte of data) {
    const factor = byte ^ result.shift();
    result.push(0);
    for (let i = 0; i < degree; i++) {
      result[i] ^= gfMultiply(generator[i], factor);
    }
  }
  return result;
}

function addFinder(modules, reserved, row, col) {
  for (let dy = -1; dy <= 7; dy++) {
    for (let dx = -1; dx <= 7; dx++) {
      const r = row + dy;
      const c = col + dx;
      if (r < 0 || r >= SIZE || c < 0 || c >= SIZE) continue;
      const dark = dy >= 0 && dy <= 6 && dx >= 0 && dx <= 6
        && (dy === 0 || dy === 6 || dx === 0 || dx === 6 || (dy >= 2 && dy <= 4 && dx >= 2 && dx <= 4));
      modules[r][c] = dark;
      reserved[r][c] = true;
    }
  }
}

function addAlignment(setFunction, row, col) {
  for (let dy = -2; dy <= 2; dy++) {
    for (let dx = -2; dx <= 2; dx++) {
      const ring = Math.max(Math.abs(dx), Math.abs(dy));
      setFunction(row + dy, col + dx, ring !== 1);
    }
  }
}

function addTiming(setFunction) {
  for (let i = 8; i < SIZE - 8; i++) {
    const dark = i % 2 === 0;
    setFunction(6, i, dark);
    setFunction(i, 6, dark);
  }
}

function reserveFormat(reserved) {
  for (let i = 0; i <= 8; i++) {
    if (i !== 6) {
      reserved[8][i] = true;
      reserved[i][8] = true;
    }
  }
  reserved[7][8] = true;
  reserved[8][7] = true;
  reserved[8][8] = true;
  for (let i = 0; i < 7; i++) reserved[SIZE - 1 - i][8] = true;
  for (let i = 0; i < 8; i++) reserved[8][SIZE - 1 - i] = true;
}

function placeData(modules, reserved, bits) {
  let bitIndex = 0;
  let upward = true;
  for (let right = SIZE - 1; right >= 1; right -= 2) {
    if (right === 6) right--;
    for (let vert = 0; vert < SIZE; vert++) {
      const row = upward ? SIZE - 1 - vert : vert;
      for (let j = 0; j < 2; j++) {
        const col = right - j;
        if (reserved[row][col]) continue;
        const bit = bitIndex < bits.length ? bits[bitIndex++] === 1 : false;
        modules[row][col] = bit !== mask(0, row, col);
      }
    }
    upward = !upward;
  }
}

function mask(maskId, row, col) {
  if (maskId !== 0) return false;
  return (row + col) % 2 === 0;
}

function writeFormat(modules, reserved, maskId) {
  const format = formatBits(maskId);
  const set = (row, col, dark) => {
    modules[row][col] = dark;
    reserved[row][col] = true;
  };

  for (let i = 0; i < 15; i++) {
    const dark = ((format >>> i) & 1) === 1;
    if (i < 6) set(i, 8, dark);
    else if (i === 6) set(7, 8, dark);
    else if (i === 7) set(8, 8, dark);
    else if (i === 8) set(8, 7, dark);
    else set(8, 14 - i, dark);

    if (i < 8) set(8, SIZE - 1 - i, dark);
    else set(SIZE - 15 + i, 8, dark);
  }
  set(SIZE - 8, 8, true);
}

function formatBits(maskId) {
  const data = (1 << 3) | maskId;
  let bits = data << 10;
  for (let i = 14; i >= 10; i--) {
    if (((bits >>> i) & 1) !== 0) bits ^= 0x537 << (i - 10);
  }
  return ((data << 10) | bits) ^ 0x5412;
}
