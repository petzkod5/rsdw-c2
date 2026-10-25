const assert = require('node:assert/strict');
const path = require('node:path');
const {chromium} = require('playwright');

const sse = (lines, reason = 'complete') => `: stream open\n\n${lines.map(line => `data: ${line}\n\n`).join('')}event: end\ndata: ${JSON.stringify({reason})}\n\n`;

async function run() {
  const browser = await chromium.launch({headless:true, executablePath:process.env.RSDW_TEST_CHROMIUM});
  try {
    const page = await browser.newPage({viewport:{width:1400,height:900}});
    const servers = ['aurora','brin'].map((id, index) => ({id, worldName:id, status:'online', maxPlayers:4, endpoint:`10.0.0.${index}:7777`, metrics:{}}));
    const streams = [];
    let role = 'admin';
    let batch = 0;
    let hold = null;
    await page.route('http://logs.test/**', async route => {
      const url = new URL(route.request().url());
      const files = {'/':'index.html','/app.js':'app.js','/styles.css':'styles.css'};
      if (files[url.pathname]) return route.fulfill({path:path.join(__dirname,'../web',files[url.pathname])});
      if (url.pathname.endsWith('/logs/stream')) {
        streams.push(`${url.pathname}${url.search}`);
        if (hold) await hold;
        batch++;
        const lines = Array.from({length:40}, (_, i) => `2026-10-24T20:00:00Z ${url.searchParams.get('container')} batch ${batch} line ${i + 1}`);
        return route.fulfill({status:200, headers:{'Content-Type':'text/event-stream'}, body:sse(lines, url.searchParams.get('follow') === '1' ? 'timeout' : 'complete')});
      }
      let body = {};
      if (url.pathname === '/api/auth') body = {mode:'oidc',authenticated:true,subject:role,role,csrfToken:'fixture',capabilities:{dashboard:true,telemetry:true,events:role === 'admin',logs:role === 'admin'}};
      else if (url.pathname === '/api/bootstrap') body = {servers,cluster:'test',mode:'kubernetes'};
      else if (url.pathname.endsWith('/telemetry')) body = {server:servers[0],metrics:{},samples:[]};
      await route.fulfill({json:body});
    });
    const settle = () => page.waitForFunction(() => state.loaded && !state.refreshing);
    const output = () => page.getByTestId('log-stream-output').innerText();
    const lastStream = () => streams.at(-1);

    await page.goto('http://logs.test/#dashboard');
    await settle();
    await page.getByTestId('nav-logs').click();
    await page.waitForFunction(() => state.page === 'logs');
    await page.waitForFunction(() => document.querySelector('[data-testid="log-stream-status"]')?.textContent.includes('Follow stopped after 15 minutes'));
    assert.equal(lastStream(), '/api/servers/aurora/logs/stream?container=server&tailLines=500&follow=1');
    assert.match(await output(), /server batch 1 line 40/);
    assert.equal(await page.locator('#server-filter').inputValue(), 'aurora');

    await page.getByTestId('log-reconnect').click();
    await page.waitForFunction(() => document.querySelector('[data-testid="log-stream-output"]').textContent.includes('batch 2 line 40'));
    assert.equal(streams.length, 2, 'reconnect did not open a new stream');

    await page.getByTestId('log-container').selectOption('metrics');
    await page.waitForFunction(() => document.querySelector('[data-testid="log-stream-output"]').textContent.includes('metrics batch 3'));
    assert.equal(lastStream(), '/api/servers/aurora/logs/stream?container=metrics&tailLines=500&follow=1');
    assert.doesNotMatch(await output(), /server batch/, 'old container lines stayed after switching');

    await page.getByTestId('log-previous').check();
    await page.waitForFunction(() => document.querySelector('[data-testid="log-stream-status"]')?.textContent.includes('End of log'));
    assert.equal(lastStream(), '/api/servers/aurora/logs/stream?container=metrics&tailLines=500&previous=1');
    assert.equal(await page.getByTestId('log-follow').isDisabled(), true, 'follow stayed enabled for previous logs');
    await page.getByTestId('log-previous').uncheck();

    await page.getByTestId('log-tail').selectOption('5000');
    await page.waitForFunction(() => document.querySelector('[data-testid="log-stream-output"]').textContent.includes('batch 6'));
    assert.equal(lastStream(), '/api/servers/aurora/logs/stream?container=metrics&tailLines=5000&follow=1');

    let release;
    hold = new Promise(resolve => { release = resolve; });
    await page.getByTestId('log-reconnect').click();
    await page.getByTestId('log-pause').click();
    release();
    hold = null;
    await page.waitForFunction(() => document.querySelector('[data-testid="log-resume"]')?.textContent.includes('40 new lines'));
    assert.match(await page.getByTestId('log-resume').innerText(), /40 new lines/);
    assert.doesNotMatch(await output(), /batch 7/, 'paused view kept appending');
    await page.getByTestId('log-resume').click();
    await page.waitForFunction(() => document.querySelector('[data-testid="log-stream-output"]').textContent.includes('batch 7 line 40'));
    const atBottom = await page.getByTestId('log-stream-output').evaluate(el => el.scrollHeight - el.scrollTop - el.clientHeight < 4);
    assert.ok(atBottom, 'resume did not scroll to the newest line');

    await page.getByTestId('log-stream-output').evaluate(el => { el.scrollTop = 0; el.dispatchEvent(new Event('scroll')); });
    await page.getByTestId('log-resume').waitFor();

    await page.locator('#server-filter').selectOption('brin');
    await page.waitForFunction(() => document.querySelector('[data-testid="log-stream-output"]').textContent.includes('batch 8'));
    assert.match(lastStream(), /^\/api\/servers\/brin\/logs\/stream\?/);
    assert.equal(new URL(page.url()).hash, '#logs?serverId=brin');
    await page.getByTestId('nav-dashboard').click();
    await page.waitForFunction(() => state.page === 'dashboard');
    await settle();
    assert.equal(await page.locator('#server-filter').inputValue(), '', 'logs selection leaked to the dashboard');
    const opened = streams.length;
    await page.waitForTimeout(300);
    assert.equal(streams.length, opened, 'stream kept reopening after leaving the logs page');
    await page.getByTestId('nav-logs').click();
    await page.waitForFunction(() => state.page === 'logs');
    await page.waitForFunction(() => document.querySelector('[data-testid="log-stream-output"]').textContent.includes('batch 9'));
    assert.equal(streams.length, opened + 1, 'returning to logs did not open a fresh stream');
    assert.doesNotMatch(await output(), /batch 8/, 'stale lines stayed after returning to logs');

    // Feed the next stream through fetch in hand-picked chunks so events split across reads.
    const streamChunks = (chunks) => page.evaluate((chunks) => {
      const realFetch = window.fetch;
      window.fetch = (input, init) => {
        if (!String(input).includes('/logs/stream')) return realFetch(input, init);
        window.fetch = realFetch;
        const encoder = new TextEncoder();
        const body = new ReadableStream({async pull(controller) {
          const chunk = chunks.shift();
          if (chunk === undefined) return controller.close();
          await new Promise(resolve => setTimeout(resolve, 20));
          controller.enqueue(encoder.encode(chunk));
        }});
        return Promise.resolve(new Response(body, {status:200, headers:{'Content-Type':'text/event-stream'}}));
      };
    }, chunks);
    const statusText = () => page.getByTestId('log-stream-status').innerText();

    await streamChunks([': stream open\n\ndata: chunk li', 'ne A\n', '\ndata: chunk line B\n\nevent: e', 'nd\ndata: {"reason":"tim', 'eout"}\n', '\n']);
    await page.getByTestId('log-reconnect').click();
    await page.waitForFunction(() => document.querySelector('[data-testid="log-stream-status"]')?.textContent.includes('Follow stopped after 15 minutes'));
    assert.match(await output(), /chunk line A\nchunk line B$/, 'events split across reads were lost or garbled');

    await streamChunks([': stream open\n\n', 'data: cut off line\n\n']);
    await page.getByTestId('log-reconnect').click();
    await page.waitForFunction(() => document.querySelector('[data-testid="log-stream-output"]').textContent.includes('cut off line'));
    await page.waitForFunction(() => !state.logView.controller);
    assert.equal(await statusText(), 'Connection closed. Reconnect to keep following.', 'a stream that ended without an end event still claimed to follow');

    role = 'viewer';
    await page.goto('http://logs.test/#logs');
    await page.reload();
    await settle();
    assert.equal(await page.getByTestId('nav-logs').count(), 0, 'viewer sees the logs nav');
    assert.notEqual(await page.evaluate(() => state.page), 'logs');
  } finally {
    await browser.close();
  }
}
run().then(() => console.log('logs browser test passed'), error => { console.error(error); process.exitCode = 1; });
