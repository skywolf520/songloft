#!/usr/bin/env bash
# 从 favicon.svg 生成项目所需的所有 PNG/ICO 图标
# 依赖: node (>= 18), 会自动临时安装 sharp
# 用法: ./scripts/generate-icons.sh

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

echo "Installing sharp in temp dir..."
cd "$WORKDIR"
npm init -y --silent >/dev/null 2>&1
npm install sharp --silent 2>&1 | tail -1

# 把脚本写到临时目录（这样 import sharp 能按 node_modules 就近解析）
cat > "$WORKDIR/gen.mjs" <<SCRIPT
import { readFileSync, writeFileSync, mkdirSync } from 'fs';
import { resolve } from 'path';
import sharp from 'sharp';

const root = '${ROOT}';
const playerDir = resolve(root, 'songloft-player');
const buildDir = resolve(root, 'songloft-player-build/web-embedded');

const svgBuffer = readFileSync(resolve(playerDir, 'web/favicon.svg'));

async function renderPng(size) {
  return sharp(svgBuffer, { density: Math.round(72 * size / 512) * 4 })
    .resize(size, size)
    .png()
    .toBuffer();
}

async function renderIco(sizes) {
  const buffers = await Promise.all(sizes.map(s => renderPng(s)));
  const numImages = buffers.length;
  const headerSize = 6 + numImages * 16;
  let offset = headerSize;
  const header = Buffer.alloc(headerSize);
  header.writeUInt16LE(0, 0);
  header.writeUInt16LE(1, 2);
  header.writeUInt16LE(numImages, 4);
  for (let i = 0; i < numImages; i++) {
    const sz = sizes[i] >= 256 ? 0 : sizes[i];
    const off = 6 + i * 16;
    header.writeUInt8(sz, off);
    header.writeUInt8(sz, off + 1);
    header.writeUInt8(0, off + 2);
    header.writeUInt8(0, off + 3);
    header.writeUInt16LE(1, off + 4);
    header.writeUInt16LE(32, off + 6);
    header.writeUInt32LE(buffers[i].length, off + 8);
    header.writeUInt32LE(offset, off + 12);
    offset += buffers[i].length;
  }
  return Buffer.concat([header, ...buffers]);
}

function ensureDir(path) {
  mkdirSync(path, { recursive: true });
}

const webTasks = [
  { size: 64,  out: resolve(playerDir, 'web/favicon.png') },
  { size: 192, out: resolve(playerDir, 'web/icons/Icon-192.png') },
  { size: 512, out: resolve(playerDir, 'web/icons/Icon-512.png') },
  { size: 192, out: resolve(playerDir, 'web/icons/Icon-maskable-192.png') },
  { size: 512, out: resolve(playerDir, 'web/icons/Icon-maskable-512.png') },
  { size: 64,  out: resolve(buildDir, 'favicon.png') },
  { size: 192, out: resolve(buildDir, 'icons/Icon-192.png') },
  { size: 512, out: resolve(buildDir, 'icons/Icon-512.png') },
  { size: 192, out: resolve(buildDir, 'icons/Icon-maskable-192.png') },
  { size: 512, out: resolve(buildDir, 'icons/Icon-maskable-512.png') },
];

const appIcon = { size: 1024, out: resolve(playerDir, 'assets/icons/app_icon.png') };

const macSizes = [16, 32, 64, 128, 256, 512, 1024];
const macDir = resolve(playerDir, 'macos/Runner/Assets.xcassets/AppIcon.appiconset');

const iosMapping = [
  { size: 20,   name: 'Icon-App-20x20@1x.png' },
  { size: 40,   name: 'Icon-App-20x20@2x.png' },
  { size: 60,   name: 'Icon-App-20x20@3x.png' },
  { size: 29,   name: 'Icon-App-29x29@1x.png' },
  { size: 58,   name: 'Icon-App-29x29@2x.png' },
  { size: 87,   name: 'Icon-App-29x29@3x.png' },
  { size: 40,   name: 'Icon-App-40x40@1x.png' },
  { size: 80,   name: 'Icon-App-40x40@2x.png' },
  { size: 120,  name: 'Icon-App-40x40@3x.png' },
  { size: 120,  name: 'Icon-App-60x60@2x.png' },
  { size: 180,  name: 'Icon-App-60x60@3x.png' },
  { size: 76,   name: 'Icon-App-76x76@1x.png' },
  { size: 152,  name: 'Icon-App-76x76@2x.png' },
  { size: 167,  name: 'Icon-App-83.5x83.5@2x.png' },
  { size: 1024, name: 'Icon-App-1024x1024@1x.png' },
];
const iosDir = resolve(playerDir, 'ios/Runner/Assets.xcassets/AppIcon.appiconset');

console.log('Generating icons from favicon.svg...\\n');

for (const { size, out } of [...webTasks, appIcon]) {
  ensureDir(out.substring(0, out.lastIndexOf('/')));
  writeFileSync(out, await renderPng(size));
  console.log('  ✓ ' + size + 'x' + size + ' → ' + out.replace(root + '/', ''));
}

ensureDir(macDir);
for (const size of macSizes) {
  const out = resolve(macDir, 'app_icon_' + size + '.png');
  writeFileSync(out, await renderPng(size));
  console.log('  ✓ ' + size + 'x' + size + ' → ' + out.replace(root + '/', ''));
}

ensureDir(iosDir);
for (const { size, name } of iosMapping) {
  const out = resolve(iosDir, name);
  writeFileSync(out, await renderPng(size));
  console.log('  ✓ ' + size + 'x' + size + ' → songloft-player/ios/.../AppIcon.appiconset/' + name);
}

const icoOut = resolve(playerDir, 'windows/runner/resources/app_icon.ico');
ensureDir(icoOut.substring(0, icoOut.lastIndexOf('/')));
writeFileSync(icoOut, await renderIco([16, 32, 48, 64, 128, 256]));
console.log('  ✓ ICO (16-256) → songloft-player/windows/runner/resources/app_icon.ico');

console.log('\\nDone!');
SCRIPT

node "$WORKDIR/gen.mjs"
