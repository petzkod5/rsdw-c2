// Re-encodes the shipped login art into the responsive set proposed in login-performance.md.
// Usage (from this directory):  node optimize-hero.cjs <repoRoot> [outDir]
// Needs `sharp` (npm i sharp). Writes <outDir>/proto/*.{avif,webp} and <outDir>/img-out/lqip-*.webp,
// which is what prototype-login-server.cjs serves. Nothing here touches the repository's web/ directory.
const sharp = require('sharp');
const fs = require('fs');
const path = require('path');

const repo = process.argv[2];
const out = path.resolve(process.argv[3] || __dirname);
if (!repo) { console.error('usage: node optimize-hero.cjs <repoRoot> [outDir]'); process.exit(2); }
const proto = path.join(out, 'proto');
const lqip = path.join(out, 'img-out');
fs.mkdirSync(proto, { recursive: true });
fs.mkdirSync(lqip, { recursive: true });

const desktop = path.join(repo, 'web/assets/dragonwilds-login-desktop-v1.png');
const mobile = path.join(repo, 'web/assets/dragonwilds-login-mobile-v1.png');
const kb = (n) => (n / 1024).toFixed(1).padStart(7) + ' KB';

async function write(file, pipeline) {
  const buf = await pipeline.toBuffer();
  fs.writeFileSync(file, buf);
  const meta = await sharp(buf).metadata();
  console.log(path.basename(file).padEnd(18), `${meta.width}x${meta.height}`.padEnd(11), kb(buf.length));
}

(async () => {
  console.log('source'.padEnd(18), 'desktop', kb(fs.statSync(desktop).size), ' mobile', kb(fs.statSync(mobile).size));
  for (const w of [1280, 1920, 2560]) await write(path.join(proto, `d-${w}.avif`), sharp(desktop).resize({ width: w }).avif({ quality: 45, effort: 4 }));
  for (const w of [750, 1170]) await write(path.join(proto, `m-${w}.avif`), sharp(mobile).resize({ width: w }).avif({ quality: 45, effort: 4 }));
  await write(path.join(proto, 'd-1920.webp'), sharp(desktop).resize({ width: 1920 }).webp({ quality: 72, effort: 4 }));
  await write(path.join(proto, 'm-750.webp'), sharp(mobile).resize({ width: 750 }).webp({ quality: 72, effort: 4 }));
  await write(path.join(lqip, 'lqip-desktop-24.webp'), sharp(desktop).resize({ width: 24 }).webp({ quality: 40 }));
  await write(path.join(lqip, 'lqip-mobile-24.webp'), sharp(mobile).resize({ width: 24 }).webp({ quality: 40 }));
})();
