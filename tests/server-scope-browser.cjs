const assert = require('node:assert/strict');
const path = require('node:path');
const {chromium} = require('playwright');

// A server picked on one page must not filter another page.
async function run() {
  const browser = await chromium.launch({headless:true, executablePath:process.env.RSDW_TEST_CHROMIUM});
  try {
    const page = await browser.newPage({viewport:{width:1400,height:900}});
    const servers = ['aurora','brin','cinder'].map((id, index) => ({id, worldName:id, status:'online', maxPlayers:4, endpoint:`10.0.0.${index}:7777`, metrics:{}}));
    await page.route('http://scope.test/**', async route => {
      const url = new URL(route.request().url());
      const files = {'/':'index.html','/app.js':'app.js','/styles.css':'styles.css'};
      if (files[url.pathname]) return route.fulfill({path:path.join(__dirname,'../web',files[url.pathname])});
      let body = {};
      if (url.pathname === '/api/auth') body = {mode:'oidc',authenticated:true,subject:'admin',role:'admin',csrfToken:'fixture',capabilities:Object.fromEntries(['dashboard','telemetry','events','maintenance','reboots'].map(key => [key,true]))};
      else if (url.pathname === '/api/bootstrap') body = {servers,cluster:'test',mode:'kubernetes'};
      else if (url.pathname.endsWith('/telemetry')) { const server = servers.find(s => url.pathname.includes(`/${s.id}/`)); body = {server,metrics:{},samples:[]}; }
      else if (url.pathname === '/api/reboots') body = {available:true,schedules:[]};
      await route.fulfill({json:body});
    });
    const settle = () => page.waitForFunction(() => state.loaded && !state.refreshing);
    const rows = () => page.locator('.console-row').evaluateAll(items => items.map(item => item.dataset.serverId));
    const filter = () => page.locator('#server-filter').inputValue();
    const go = async (testId) => { await page.getByTestId(testId).click(); await page.waitForFunction(name => state.page === name, testId.replace('nav-','')); await settle(); };

    await page.goto('http://scope.test/#dashboard');
    await settle();
    assert.deepEqual(await rows(), ['aurora','brin','cinder']);

    await go('nav-telemetry');
    await page.locator('#server-filter').selectOption('cinder');
    await settle();
    assert.equal(new URL(page.url()).hash, '#telemetry?serverId=cinder');

    await go('nav-dashboard');
    assert.deepEqual(await rows(), ['aurora','brin','cinder']);
    assert.equal(await filter(), '', 'dashboard filter inherited the Metrics selection');

    await go('nav-events');
    assert.equal(new URL(page.url()).hash, '#events', 'events link inherited the Metrics selection');
    assert.equal(await filter(), '');

    await page.locator('#server-filter').selectOption('brin');
    await settle();
    await go('nav-telemetry');
    assert.equal(new URL(page.url()).hash, '#telemetry?serverId=cinder', 'Metrics forgot its own selection');
    assert.equal(await filter(), 'cinder');

    await go('nav-dashboard');
    await page.locator('#server-filter').selectOption('aurora');
    await settle();
    assert.deepEqual(await rows(), ['aurora']);
    await go('nav-maintenance');
    assert.equal(new URL(page.url()).hash, '#maintenance');
    await go('nav-dashboard');
    assert.equal(await filter(), 'aurora', 'dashboard forgot its own selection');

    await page.goto('http://scope.test/#events?serverId=brin');
    await settle();
    assert.equal(await filter(), 'brin', 'deep link scope ignored');
    await go('nav-dashboard');
    assert.equal(await filter(), 'aurora');

    await page.goto('http://scope.test/#telemetry?serverId=cinder');
    await settle();
    await go('nav-dashboard');
    servers.splice(2, 1);
    await page.locator('#refresh').click();
    await settle();
    assert.equal(await page.getByTestId('nav-telemetry').getAttribute('href'), '#telemetry', 'deleted server still remembered');
    await go('nav-telemetry');
    assert.equal(await filter(), 'aurora', 'Metrics stuck on a deleted server');
    assert.doesNotMatch(await page.locator('#content').innerText(), /No servers registered/);

    servers.push({id:'cinder', worldName:'cinder', status:'online', maxPlayers:4, endpoint:'10.0.0.2:7777', metrics:{}});
    await page.locator('#refresh').click();
    await settle();
    assert.equal(await page.getByTestId('nav-telemetry').getAttribute('href'), '#telemetry', 're-added server silently re-filtered Metrics');

    await page.goto('http://scope.test/#telemetry');
    await settle();
    await page.locator('#server-filter').selectOption('brin');
    await settle();
    await page.evaluate(() => { location.hash = '#telemetry'; });
    await page.waitForFunction(() => location.hash === '#telemetry?serverId=brin');
    assert.equal(await filter(), 'brin', 'plain #telemetry did not show the remembered pick in the URL');
  } finally {
    await browser.close();
  }
}
run().then(() => console.log('server scope browser test passed'), error => { console.error(error); process.exitCode = 1; });
