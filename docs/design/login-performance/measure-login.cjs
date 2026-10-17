// Login-page load measurement against a running rsdw-c2 (run it in token mode so the login scene shows).
// Usage: node measure-login.cjs <baseUrl> <label> [profiles,comma] [devices,comma] [outDir]
// Env: WARM=1 repeat visit, CPU=4 main-thread slowdown, AUTHED=1 signed-in path, TOKEN, BLOCKIMG=1
// Needs: npm i playwright@1.62.1 sharp   (uses the installed Microsoft Edge channel; change `channel` for Chromium)
const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const BASE = process.argv[2] || 'http://127.0.0.1:18080';
const LABEL = process.argv[3] || 'baseline';
const ONLY_PROFILES = (process.argv[4] || '').split(',').filter(Boolean);
const ONLY_DEVICES = (process.argv[5] || '').split(',').filter(Boolean);
const OUT = process.argv[6] || path.join(__dirname, 'out');
const CPU = Number(process.env.CPU || 1);
const AUTHED = process.env.AUTHED === '1';
const BLOCKIMG = process.env.BLOCKIMG === '1';
const TOKEN = process.env.TOKEN || 'local-measure-token';
fs.mkdirSync(OUT, { recursive: true });

const mbps = (n) => (n * 1e6) / 8;
const PROFILES = {
  loopback: null,
  'broadband-100Mbps-20ms': { latency: 20, downloadThroughput: mbps(100), uploadThroughput: mbps(20) },
  'broadband-25Mbps-40ms': { latency: 40, downloadThroughput: mbps(25), uploadThroughput: mbps(5) },
  'lte-9Mbps-150ms': { latency: 150, downloadThroughput: mbps(9), uploadThroughput: mbps(3) },
};
const DEVICES = {
  desktop: { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 },
  mobile: { viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true },
};

const initScript = () => {
  const m = (window.__m = { lcp: [], longtasks: [], fcp: null, btnShown: null, btnPainted: null });
  try {
    new PerformanceObserver((l) => { for (const e of l.getEntries()) if (e.name === 'first-contentful-paint') m.fcp = e.startTime; }).observe({ type: 'paint', buffered: true });
    new PerformanceObserver((l) => { for (const e of l.getEntries()) m.lcp.push({ t: Math.round(e.startTime), size: e.size, url: (e.url || '').replace(location.origin, ''), el: e.element ? (e.element.className || e.element.tagName) : '' }); }).observe({ type: 'largest-contentful-paint', buffered: true });
    new PerformanceObserver((l) => { for (const e of l.getEntries()) m.longtasks.push({ s: Math.round(e.startTime), d: Math.round(e.duration) }); }).observe({ type: 'longtask', buffered: true });
  } catch (e) { m.err = String(e); }
  const check = () => {
    if (m.btnShown) return;
    const b = document.querySelector('[data-testid="open-login"]');
    if (b && b.getBoundingClientRect().width > 0) {
      m.btnShown = performance.now();
      requestAnimationFrame(() => requestAnimationFrame(() => { m.btnPainted = performance.now(); }));
    }
  };
  new MutationObserver(check).observe(document, { childList: true, subtree: true, attributes: true });
};

async function run(profileName, deviceName, warm) {
  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  try {
    const context = await browser.newContext({ ...DEVICES[deviceName], locale: 'en-US' });
    const visit = async (tag) => {
      const page = await context.newPage();
      await page.addInitScript(initScript);
      const cdp = await context.newCDPSession(page);
      await cdp.send('Network.enable');
      if (CPU > 1) await cdp.send('Emulation.setCPUThrottlingRate', { rate: CPU });
      if (AUTHED) await page.addInitScript((t) => { try { sessionStorage.setItem('rsdw-admin-token', t); } catch {} }, TOKEN);
      const p = PROFILES[profileName];
      if (p) await cdp.send('Network.emulateNetworkConditions', { offline: false, ...p });
      const requests = [];
      page.on('requestfinished', async (req) => {
        try {
          const res = await req.response();
          const sizes = await req.sizes();
          requests.push({ url: req.url().replace(BASE, ''), status: res.status(), fromCache: res.fromServiceWorker() ? 'sw' : undefined, bodyBytes: sizes.responseBodySize, headerBytes: sizes.responseHeadersSize });
        } catch {}
      });
      if (BLOCKIMG) await page.route('**/assets/*.png', (r) => r.abort());
      const frames = [];
      cdp.on('Page.screencastFrame', (f) => {
        frames.push({ ts: f.metadata.timestamp * 1000, data: f.data });
        cdp.send('Page.screencastFrameAck', { sessionId: f.sessionId }).catch(() => {});
      });
      await cdp.send('Page.startScreencast', { format: 'jpeg', quality: 40, maxWidth: 320, maxHeight: 640, everyNthFrame: 1 });
      const t0 = Date.now();
      await page.goto(BASE + '/', { waitUntil: 'commit' });
      // wait for button painted, then wait for the png (or 90s)
      await page.waitForFunction(() => window.__m && window.__m.btnPainted, null, { timeout: 90000, polling: 20 }).catch(() => {});
      const btnAt = Date.now() - t0;
      let dashAt = null;
      if (AUTHED) { await page.waitForSelector('.console-row', { timeout: 90000 }).catch(() => {}); dashAt = Date.now() - t0; }
      if (tag === 'cold') {
        await page.screenshot({ path: path.join(OUT, `${LABEL}-${profileName}-${deviceName}-${tag}-at-button.png`) }).catch(() => {});
      }
      await page.waitForFunction(() => performance.getEntriesByType('resource').some((r) => /\.(png|avif|webp|jpe?g)$/.test(new URL(r.name).pathname) && r.responseEnd > 0), null, { timeout: 90000, polling: 50 }).catch(() => {});
      await page.waitForTimeout(1500); // allow decode + paint + final LCP candidate
      if (tag === 'cold') {
        await page.screenshot({ path: path.join(OUT, `${LABEL}-${profileName}-${deviceName}-${tag}-settled.png`) }).catch(() => {});
      }
      const data = await page.evaluate(() => {
        const nav = performance.getEntriesByType('navigation')[0];
        const res = performance.getEntriesByType('resource').map((r) => ({ name: new URL(r.name).pathname + new URL(r.name).search.slice(0, 12), start: Math.round(r.startTime), responseStart: Math.round(r.responseStart), end: Math.round(r.responseEnd), transfer: r.transferSize, enc: r.encodedBodySize, dec: r.decodedBodySize, init: r.initiatorType }));
        return { m: window.__m, nav: { ttfb: Math.round(nav.responseStart), domInteractive: Math.round(nav.domInteractive), dcl: Math.round(nav.domContentLoadedEventEnd), load: Math.round(nav.loadEventEnd) }, res, bodyClass: document.body.className, title: document.querySelector('.login-scene h1')?.textContent };
      });
      await cdp.send('Page.stopScreencast').catch(() => {});
      await page.close();
      // frame analysis: fraction of pixels that differ from the flat page colour #171f25 (23,31,37)
      const sharp = require('sharp');
      const analysis = [];
      for (const f of frames) {
        const { data: px, info } = await sharp(Buffer.from(f.data, 'base64')).removeAlpha().raw().toBuffer({ resolveWithObject: true });
        let diff = 0, white = 0; const n = info.width * info.height;
        for (let i = 0; i < px.length; i += 3) {
          const r = px[i], g = px[i + 1], b = px[i + 2];
          if (r > 235 && g > 235 && b > 235) { white++; continue; }
          const d = Math.abs(r - 23) + Math.abs(g - 31) + Math.abs(b - 37);
          if (d > 45) diff++;
        }
        const tiny = await sharp(Buffer.from(f.data, 'base64')).removeAlpha().resize(32, 20, { fit: 'fill' }).raw().toBuffer();
        analysis.push({ t: Math.round(f.ts - t0), frac: +(diff / n).toFixed(3), white: +(white / n).toFixed(3), tiny });
      }
      const maxFrac = Math.max(0, ...analysis.map((a) => a.frac));
      const last = analysis.at(-1)?.tiny;
      const mad = (a, b) => { let d = 0; for (let i = 0; i < a.length; i++) d += Math.abs(a[i] - b[i]); return d / a.length; };
      let vc = null;
      if (last) { for (let i = analysis.length - 1; i >= 0; i--) { if (mad(analysis[i].tiny, last) > 4) { vc = analysis[Math.min(i + 1, analysis.length - 1)].t; break; } if (i === 0) vc = analysis[0].t; } }
      const firstFrame = analysis.find((a) => a.white < 0.5)?.t ?? null;
      const bgFirst = analysis.find((a) => a.white < 0.5 && a.frac > 0.12)?.t ?? null;
      const bgFull = analysis.find((a) => a.white < 0.5 && a.frac >= 0.9 * maxFrac && maxFrac > 0.12)?.t ?? null;
      const lcp = data.m.lcp.at(-1);
      const png = data.res.find((r) => /\.(png|avif|webp|jpe?g)/.test(r.name));
      const total = data.res.reduce((s, r) => s + (r.transfer || 0), 0);
      return {
        tag, profile: profileName, device: deviceName,
        fcp: Math.round(data.m.fcp || 0), btnShown: Math.round(data.m.btnShown || 0), btnPainted: Math.round(data.m.btnPainted || 0),
        lcp: lcp ? lcp.t : null, lcpEl: lcp ? `${lcp.el}${lcp.url ? ' ' + lcp.url : ''}` : null,
        imgStart: png ? png.start : null, imgEnd: png ? png.end : null, imgTransfer: png ? png.transfer : null, imgEnc: png ? png.enc : null,
        firstFrame, bgFirst, bgFull, vc, maxFrac, frameCount: analysis.length, dashAt, cpu: CPU, authed: AUTHED,
        totalTransfer: total, nav: data.nav, longTasks: data.m.longtasks, bodyClass: data.bodyClass, wallBtn: btnAt,
        res: data.res.map((r) => `${r.name} ${r.start}->${r.end}ms transfer=${r.transfer} enc=${r.enc}`),
      };
    };
    const results = [];
    results.push(await visit('cold'));
    if (warm) results.push(await visit('warm'));
    return results;
  } finally {
    await browser.close();
  }
}

(async () => {
  const all = [];
  for (const profile of Object.keys(PROFILES)) {
    if (ONLY_PROFILES.length && !ONLY_PROFILES.includes(profile)) continue;
    for (const device of Object.keys(DEVICES)) {
      if (ONLY_DEVICES.length && !ONLY_DEVICES.includes(device)) continue;
      const warm = process.env.WARM === '1' && (profile === 'loopback' || profile === 'broadband-100Mbps-20ms');
      const r = await run(profile, device, warm);
      for (const x of r) {
        all.push(x);
        console.log(JSON.stringify({ ...x, res: undefined, longTasks: x.longTasks.length ? x.longTasks : undefined }));
      }
    }
  }
  fs.writeFileSync(path.join(OUT, `${LABEL}-results.json`), JSON.stringify(all, null, 2));
})().catch((e) => { console.error(e); process.exit(1); });
