const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {spawn} = require('node:child_process');
const {once} = require('node:events');
const {chromium, request: playwrightRequest} = require('playwright');

// Delivery budget for the login path, run against the real Go binary in token mode.
// Everything is asserted on bytes, headers and event order; nothing depends on wall-clock timing.
const COLD_BUDGET = 300 * 1000;
const WARM_BUDGET = 10 * 1000;
const HERO = /\/assets\/login-hero-/;

const root = path.resolve(__dirname, '..');
fs.mkdirSync(path.join(root, '.tmp'), {recursive:true});
const output = fs.mkdtempSync(path.join(root, '.tmp/login-budget-'));
const token = 'login-budget-test-token';
const binary = process.env.RSDW_TEST_BINARY || path.join(root, '.tmp/rsdw-c2');
const server = spawn(binary, [], {
  cwd:root,
  env:{...process.env, RSDW_AUTH_MODE:'token', RSDW_DEMO_DATA:'true', RSDW_ADMIN_TOKEN:token, RSDW_STATE_FILE:path.join(output, 'state.json'), RSDW_LISTEN_ADDR:'127.0.0.1:0'},
  stdio:['ignore','pipe','pipe'],
});
const exited = once(server, 'exit');
let logs = '';
let browser;

const DEVICES = {
  desktop:{viewport:{width:1280, height:720}, deviceScaleFactor:1},
  mobile:{viewport:{width:390, height:844}, deviceScaleFactor:3, isMobile:true, hasTouch:true},
};

// Tracks every response of a page together with its on-the-wire size.
function track(page) {
  const seen = [];
  page.on('response', (response) => {
    const entry = {url:response.url(), status:response.status(), request:response.request(), headers:response.headers()};
    entry.sized = response.finished().then(async () => {
      const sizes = await response.request().sizes();
      entry.transfer = sizes.responseBodySize + sizes.responseHeadersSize;
    }).catch(() => { entry.transfer = 0; });
    seen.push(entry);
  });
  return {
    seen,
    async transfer() {
      await Promise.all(seen.map((entry) => entry.sized));
      return seen.reduce((total, entry) => total + entry.transfer, 0);
    },
    heroes: () => seen.filter((entry) => HERO.test(new URL(entry.url).pathname)),
  };
}

// Holds matching responses until released, so ordering is controlled by the test instead of by timers.
async function hold(page, pattern) {
  let arrived, release;
  const reached = new Promise((resolve) => { arrived = resolve; });
  const released = new Promise((resolve) => { release = resolve; });
  await page.route((url) => pattern.test(url.pathname), async (route) => {
    arrived();
    await released;
    await route.continue();
  });
  return {reached, release};
}

// A missing event fails the test instead of hanging it.
function within(promise, message) {
  return Promise.race([promise, new Promise((_, reject) => setTimeout(() => reject(new Error(message)), 20000).unref())]);
}

async function waitForPaint(page) {
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
}

async function loginVisit(page, base) {
  const tracker = track(page);
  const hero = page.waitForResponse((response) => HERO.test(new URL(response.url()).pathname));
  await page.goto(`${base}/`, {waitUntil:'commit'});
  await page.getByTestId('open-login').waitFor();
  await (await hero).finished();
  await page.waitForFunction(() => {
    const image = document.querySelector('.login-art-image');
    return image?.complete && image.naturalWidth > 0;
  });
  return tracker;
}

async function run() {
  const base = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('Demo server did not start')), 15000);
    server.on('error', reject);
    server.on('exit', (code) => { clearTimeout(timeout); reject(new Error(`Demo server exited with ${code}`)); });
    server.stderr.on('data', (chunk) => {
      logs += chunk;
      const match = logs.match(/listening on (127\.0\.0\.1:\d+)/);
      if (match) { clearTimeout(timeout); resolve(`http://${match[1]}`); }
    });
  });

  // Response headers, straight from the server (no browser cache involved).
  const api = await playwrightRequest.newContext({baseURL:base, extraHTTPHeaders:{'Accept-Encoding':'gzip'}});
  const index = await api.get('/');
  assert.equal(index.status(), 200);
  assert.equal(index.headers()['cache-control'], 'no-cache');
  assert.match(index.headers()['etag'], /^"[a-f0-9]{32}/);
  assert.ok(index.headers()['last-modified']);
  assert.equal(index.headers()['content-encoding'], 'gzip');
  assert.match(index.headers()['vary'], /Accept-Encoding/i);
  const html = await index.text();
  const asset = (name) => {
    const match = html.match(new RegExp(`"(/${name}\\?v=[a-f0-9]{64})"`));
    assert.ok(match, `index.html has no content-hashed ${name} URL`);
    return match[1];
  };
  const hashedImages = [...html.matchAll(/\/assets\/login-hero-[A-Za-z0-9-]+\.[a-f0-9]{10}\.(?:avif|webp)/g)].map((match) => match[0]);
  assert.ok(hashedImages.length >= 5, `expected the responsive AVIF/WebP set in index.html, found ${hashedImages.length}`);
  for (const url of [asset('app.js'), asset('styles.css'), ...new Set(hashedImages)]) {
    const response = await api.get(url);
    assert.equal(response.status(), 200, url);
    assert.equal(response.headers()['cache-control'], 'public, max-age=31536000, immutable', url);
    assert.ok(response.headers()['etag'], `${url} has no ETag`);
    if (/\.(?:avif|webp)$/.test(url)) {
      assert.equal(response.headers()['content-encoding'], undefined, `${url} must not be compressed again`);
      assert.ok(Number(response.headers()['content-length']) <= 250 * 1000, `${url} is over the 250 kB image budget`);
    } else {
      assert.equal(response.headers()['content-encoding'], 'gzip', url);
      assert.match(response.headers()['vary'], /Accept-Encoding/i, url);
    }
    const again = await api.get(url, {headers:{'If-None-Match':response.headers()['etag']}});
    assert.equal(again.status(), 304, `${url} conditional request`);
  }
  const revalidated = await api.get('/', {headers:{'If-None-Match':index.headers()['etag']}});
  assert.equal(revalidated.status(), 304);
  const auth = await api.get('/api/auth');
  assert.equal(auth.headers()['cache-control'], 'no-store');
  assert.equal(auth.headers()['etag'], undefined);
  await api.dispose();

  browser = await chromium.launch({headless:true, executablePath:process.env.RSDW_TEST_CHROMIUM});

  // Cold and warm transfer, desktop and phone art directions.
  for (const [name, device] of Object.entries(DEVICES)) {
    const context = await browser.newContext({...device, locale:'en-US'});
    const cold = await (async () => {
      const page = await context.newPage();
      const tracker = await loginVisit(page, base);
      assert.equal(tracker.heroes().length, 1, `${name}: exactly one artwork request`);
      const url = new URL(tracker.heroes()[0].url);
      assert.match(url.pathname, /\.avif$/, `${name}: Chromium should pick AVIF`);
      assert.equal(await page.locator('.login-art-image').getAttribute('fetchpriority'), 'high');
      assert.equal(await page.locator('.login-scene picture').count(), 1);
      return {page, tracker};
    })();
    const coldBytes = await cold.tracker.transfer();
    assert.ok(coldBytes <= COLD_BUDGET, `${name}: cold login transfer ${coldBytes} B exceeds ${COLD_BUDGET} B`);
    const coldEtag = cold.tracker.seen.find((entry) => new URL(entry.url).pathname === '/').headers['etag'];
    assert.ok(coldEtag, `${name}: index.html has no ETag`);
    await cold.page.close();

    const page = await context.newPage();
    const warm = await loginVisit(page, base);
    const warmBytes = await warm.transfer();
    assert.ok(warmBytes <= WARM_BUDGET, `${name}: warm login transfer ${warmBytes} B exceeds ${WARM_BUDGET} B`);
    const indexRequest = warm.seen.find((entry) => new URL(entry.url).pathname === '/').request;
    assert.equal((await indexRequest.allHeaders())['if-none-match'], coldEtag, `${name}: warm visit must revalidate the HTML shell`);
    for (const entry of warm.seen.filter((item) => /\/(?:app\.js|styles\.css)$|login-hero-/.test(new URL(item.url).pathname))) {
      assert.equal(entry.transfer, 0, `${name}: ${entry.url} was fetched from the network on a warm visit`);
    }
    console.log(`${name}: cold ${coldBytes} B, warm ${warmBytes} B`);
    await context.close();
  }

  // Signed out: the shell and the Log In button paint first; the artwork is only requested after /api/auth,
  // and nothing waits for it.
  {
    const context = await browser.newContext(DEVICES.desktop);
    const page = await context.newPage();
    const tracker = track(page);
    const authGate = await hold(page, /^\/api\/auth$/);
    const heroGate = await hold(page, HERO);
    await page.goto(`${base}/`, {waitUntil:'commit'});
    await within(authGate.reached, '/api/auth was never requested');
    await page.locator('#boot-shell').waitFor({state:'visible'});
    assert.equal(await page.locator('.login-scene').count(), 0, 'login scene mounted before /api/auth answered');
    assert.equal(await page.locator('[data-testid="open-login"]').count(), 0);
    assert.equal(tracker.heroes().length, 0);
    authGate.release();
    await within(heroGate.reached, 'artwork was never requested after /api/auth reported signed out'); // in flight and held
    const button = page.getByTestId('open-login');
    await button.waitFor({state:'visible'});
    await waitForPaint(page);
    assert.equal(tracker.heroes().length, 0, 'artwork completed before the Log In button painted');
    assert.equal(await page.locator('.login-art-image').evaluate((image) => image.complete && image.naturalWidth > 0), false);
    const placeholder = await page.locator('.login-art').evaluate((element) => getComputedStyle(element).backgroundImage);
    assert.match(placeholder, /^url\("data:image\/webp;base64,/, 'inline placeholder is missing');
    assert.equal(await page.locator('#boot-shell').isVisible(), false);
    await button.click();
    await page.getByTestId('admin-token').waitFor({state:'visible'});
    heroGate.release();
    await page.waitForFunction(() => document.querySelector('.login-art-image.is-loaded'));
    await context.close();
  }

  // Signed in: the login scene is never mounted and the artwork is never requested.
  {
    const context = await browser.newContext(DEVICES.desktop);
    await context.addInitScript((value) => {
      try { sessionStorage.setItem('rsdw-admin-token', value); } catch {}
      window.__sawLogin = false;
      new MutationObserver(() => {
        if (document.querySelector('.login-scene, [data-testid="open-login"]')) window.__sawLogin = true;
      }).observe(document, {childList:true, subtree:true});
    }, token);
    const page = await context.newPage();
    const tracker = track(page);
    const bootstrap = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/bootstrap');
    await page.goto(`${base}/`, {waitUntil:'commit'});
    await bootstrap;
    await page.waitForFunction(() => !document.body.classList.contains('auth-locked') && !document.body.classList.contains('auth-booting'));
    await page.locator('.dashboard-stats').waitFor();
    await waitForPaint(page);
    assert.equal(tracker.heroes().length, 0, 'signed-in visitor fetched the login artwork');
    assert.equal(await page.evaluate(() => window.__sawLogin), false, 'signed-in visitor saw the login scene');
    assert.equal(await page.locator('.login-scene').count(), 0);
    await context.close();
  }
  console.log('login budget passed');
}

run().finally(async () => {
  await browser?.close();
  if (server.exitCode === null && server.signalCode === null) server.kill();
  await exited;
  fs.rmSync(output, {recursive:true, force:true});
}).catch((error) => {
  console.error(error, logs);
  process.exitCode = 1;
});
