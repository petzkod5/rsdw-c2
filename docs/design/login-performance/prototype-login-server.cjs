// Prototype of the proposed login delivery (static, login only). Serves ./proto with production-style headers.
// This is a measurement aid, not the final UI: same copy and layout intent, optimized delivery.
const http = require('http');
const fs = require('fs');
const path = require('path');
const zlib = require('zlib');
const root = path.join(__dirname, 'proto');
const b64 = (f) => fs.readFileSync(path.join(__dirname, 'img-out', f)).toString('base64');
const lqipD = 'data:image/webp;base64,' + b64('lqip-desktop-24.webp');
const lqipM = 'data:image/webp;base64,' + b64('lqip-mobile-24.webp');

const html = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#0b0e11"><title>RSDW C2</title>
<link rel="preload" as="image" type="image/avif" media="(min-width:761px)" imagesrcset="/d-1280.avif 1280w, /d-1920.avif 1920w, /d-2560.avif 2560w" imagesizes="100vw" fetchpriority="high">
<link rel="preload" as="image" type="image/avif" media="(max-width:760px)" imagesrcset="/m-750.avif 750w, /m-1170.avif 1170w" imagesizes="100vw" fetchpriority="high">
<style>
html,body{margin:0;background:#0b0e11;color:#e8edf2;font:16px/1.4 system-ui,-apple-system,"Segoe UI",sans-serif}
.scene{position:relative;min-height:100svh;display:grid;grid-template-rows:auto 1fr auto;justify-items:center;padding:6svh 24px 10svh;text-align:center;isolation:isolate;overflow:clip}
.lqip{position:absolute;inset:-5%;z-index:-3;background:url(${lqipD}) center/cover;filter:blur(18px) saturate(.9)}
.hero{position:absolute;inset:0;z-index:-2;width:100%;height:100%;object-fit:cover;opacity:0;transition:opacity .35s ease-out}
.hero.ready{opacity:1}
.scrim{position:absolute;inset:0;z-index:-1;background:linear-gradient(#0b0e11cc,#0b0e1166 40%,#0b0e11e6)}
h1{margin:0;font-size:clamp(2rem,3vw,3rem);font-weight:650;letter-spacing:-.02em}
p.sub{margin:.25rem 0 0;color:#aeb8c2}
button{align-self:end;min-width:200px;min-height:56px;border:0;border-radius:6px;background:#f2a33a;color:#1a1204;font:600 1.125rem system-ui;cursor:pointer}
button:focus-visible{outline:3px solid #fff;outline-offset:3px}
@media(max-width:760px){.lqip{background-image:url(${lqipM})}}
@media(prefers-reduced-motion:reduce){.hero{transition:none}}
</style></head>
<body><main class="scene">
<div class="lqip" aria-hidden="true"></div>
<picture>
<source media="(max-width:760px)" type="image/avif" srcset="/m-750.avif 750w, /m-1170.avif 1170w" sizes="100vw">
<source media="(max-width:760px)" type="image/webp" srcset="/m-750.webp 750w" sizes="100vw">
<source type="image/avif" srcset="/d-1280.avif 1280w, /d-1920.avif 1920w, /d-2560.avif 2560w" sizes="100vw">
<source type="image/webp" srcset="/d-1920.webp 1920w" sizes="100vw">
<img class="hero" src="/d-1920.webp" alt="" width="1920" height="1080" fetchpriority="high" decoding="async" onload="this.classList.add('ready')">
</picture>
<div class="scrim" aria-hidden="true"></div>
<header><h1>Dragonwilds Control Center</h1><p class="sub">Petzko C2 Systems</p></header>
<span></span>
<button type="button" data-testid="open-login">Log in</button>
</main></body></html>`;

const types = { '.avif': 'image/avif', '.webp': 'image/webp', '.html': 'text/html; charset=utf-8' };
http.createServer((req, res) => {
  const url = new URL(req.url, 'http://x');
  let body, type, cache;
  if (url.pathname === '/') { body = Buffer.from(html); type = types['.html']; cache = 'no-cache'; }
  else {
    const file = path.join(root, path.basename(url.pathname));
    if (!fs.existsSync(file)) { res.writeHead(404); return res.end(); }
    body = fs.readFileSync(file); type = types[path.extname(file)] || 'application/octet-stream'; cache = 'public, max-age=31536000, immutable';
  }
  const headers = { 'Content-Type': type, 'Cache-Control': cache };
  if (type.startsWith('text/') && /gzip/.test(req.headers['accept-encoding'] || '')) { body = zlib.gzipSync(body); headers['Content-Encoding'] = 'gzip'; }
  headers['Content-Length'] = body.length;
  res.writeHead(200, headers); res.end(body);
}).listen(18081, '127.0.0.1', () => console.log('proto on 18081'));
