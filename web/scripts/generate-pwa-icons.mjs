#!/usr/bin/env node

/**
 * Rasterise the repository's Helm mark without adding an image dependency.
 * The supersampled geometry below mirrors public/helm-mark.svg (spokes,
 * ring, endpoint grips, and centre grip), producing deterministic PNGs for
 * Apple Home Screen and Web App Manifest consumers.
 */

import fs from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const webRoot = path.resolve(scriptDir, '..');
const markPath = path.join(webRoot, 'public', 'helm-mark.svg');
const outputDir = path.join(webRoot, 'public', 'icons');
const sourceSvg = fs.readFileSync(markPath, 'utf8');
const markColor = hexRgb(sourceSvg.match(/stroke="(#[0-9a-f]{6})"/i)?.[1] || '#6d5efc');
const background = [248, 250, 252];
const samplesPerAxis = 4;

const spokes = [
  [24, 24, 24, 4],
  [24, 24, 24, 44],
  [24, 24, 4, 24],
  [24, 24, 44, 24],
  [24, 24, 9.86, 9.86],
  [24, 24, 38.14, 9.86],
  [24, 24, 9.86, 38.14],
  [24, 24, 38.14, 38.14]
];
const grips = [
  [24, 4],
  [24, 44],
  [4, 24],
  [44, 24],
  [9.86, 9.86],
  [38.14, 9.86],
  [9.86, 38.14],
  [38.14, 38.14]
];

function hexRgb(value) {
  const normalized = value.replace('#', '');
  return [
    Number.parseInt(normalized.slice(0, 2), 16),
    Number.parseInt(normalized.slice(2, 4), 16),
    Number.parseInt(normalized.slice(4, 6), 16)
  ];
}

function distanceToSegment(px, py, x1, y1, x2, y2) {
  const dx = x2 - x1;
  const dy = y2 - y1;
  const lengthSquared = dx * dx + dy * dy;
  if (!lengthSquared) return Math.hypot(px - x1, py - y1);
  const t = Math.max(0, Math.min(1, ((px - x1) * dx + (py - y1) * dy) / lengthSquared));
  return Math.hypot(px - (x1 + t * dx), py - (y1 + t * dy));
}

function markContains(x, y) {
  if (spokes.some(([x1, y1, x2, y2]) => distanceToSegment(x, y, x1, y1, x2, y2) <= 1.5)) return true;
  if (Math.abs(Math.hypot(x - 24, y - 24) - 14) <= 1.5) return true;
  if (grips.some(([cx, cy]) => Math.hypot(x - cx, y - cy) <= 2.6)) return true;
  return Math.hypot(x - 24, y - 24) <= 4.8;
}

function rasterize(size) {
  const pixels = Buffer.alloc(size * size * 4);
  const markScale = size * 0.76 / 48;
  const markOffset = (size - size * 0.76) / 2;
  const sampleCount = samplesPerAxis * samplesPerAxis;

  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      let covered = 0;
      for (let sampleY = 0; sampleY < samplesPerAxis; sampleY += 1) {
        for (let sampleX = 0; sampleX < samplesPerAxis; sampleX += 1) {
          const sourceX = ((x + (sampleX + 0.5) / samplesPerAxis) - markOffset) / markScale;
          const sourceY = ((y + (sampleY + 0.5) / samplesPerAxis) - markOffset) / markScale;
          if (markContains(sourceX, sourceY)) covered += 1;
        }
      }

      const alpha = covered / sampleCount;
      const offset = (y * size + x) * 4;
      pixels[offset] = Math.round(markColor[0] * alpha + background[0] * (1 - alpha));
      pixels[offset + 1] = Math.round(markColor[1] * alpha + background[1] * (1 - alpha));
      pixels[offset + 2] = Math.round(markColor[2] * alpha + background[2] * (1 - alpha));
      pixels[offset + 3] = 255;
    }
  }
  return pixels;
}

function crc32(value) {
  let crc = 0xffffffff;
  for (const byte of value) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function pngChunk(type, data) {
  const typeBytes = Buffer.from(type, 'ascii');
  const payload = Buffer.concat([typeBytes, data]);
  const checksum = Buffer.alloc(4);
  checksum.writeUInt32BE(crc32(payload), 0);
  const length = Buffer.alloc(4);
  length.writeUInt32BE(data.length, 0);
  return Buffer.concat([length, payload, checksum]);
}

function encodePng(size, pixels) {
  const scanlines = Buffer.alloc((size * 4 + 1) * size);
  for (let y = 0; y < size; y += 1) {
    const rowOffset = y * (size * 4 + 1);
    scanlines[rowOffset] = 0;
    pixels.copy(scanlines, rowOffset + 1, y * size * 4, (y + 1) * size * 4);
  }
  const header = Buffer.alloc(13);
  header.writeUInt32BE(size, 0);
  header.writeUInt32BE(size, 4);
  header[8] = 8;
  header[9] = 6;
  header[10] = 0;
  header[11] = 0;
  header[12] = 0;
  return Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
    pngChunk('IHDR', header),
    pngChunk('IDAT', zlib.deflateSync(scanlines, { level: 9 })),
    pngChunk('IEND', Buffer.alloc(0))
  ]);
}

fs.mkdirSync(outputDir, { recursive: true });
for (const [name, size] of [
  ['icon-180.png', 180],
  ['icon-192.png', 192],
  ['icon-512.png', 512],
  ['icon-512-maskable.png', 512]
]) {
  fs.writeFileSync(path.join(outputDir, name), encodePng(size, rasterize(size)));
}

console.log(`Generated Helm PWA icons from ${path.relative(webRoot, markPath)} in ${path.relative(webRoot, outputDir)}.`);
