# React + TypeScript migration plan

Status: proposal, 2026-10-17. Companion to [design-brief.md](./design-brief.md), [login-performance.md](./login-performance.md) and the generated API draft [api-types.md](./api-types.md).

The decision to replace `web/app.js` (2,281 lines, 228 KB, template-string rendering) with a React + TypeScript application is already made. This document says **how**, in slices that each leave a working, shippable console.

## How this was verified

| Claim type | How |
| --- | --- |
| Versions, peer ranges, Node engines | `npm view` against the registry on 2026-10-17 |
| Inventories (actions, test IDs, capabilities, state keys, API paths) | Extracted from `web/app.js` by `docs/design/tools/legacy-inventory.cjs` (re-runnable) |
| API shapes | Read from the Go source by a sub-agent into [api-types.md](./api-types.md). I re-checked the Authorization-header rule against `auth.go` and compiled all eight TS blocks together under `tsc 6.0.3 --strict` (clean) |
| `react-doctor` behaviour, flags, exit codes | Installed `react-doctor@0.9.18` under Node 24.10 and ran it on a deliberately flawed sample |
| prek hook config | `prek validate-config` and a functional dry run with stub binaries: working directory, path rewriting and `files`/`exclude` filtering all behave as drawn below |
| Login performance | Measured against the real Go binary ([login-performance.md](./login-performance.md)) |
| **Not verified** | A full install-and-lint run of the exact pinned stack, and measured bundle sizes. The disk on this machine filled to 0 bytes twice during the session (partly my own scratch installs, partly other processes writing to it) before I could finish that spike. Compatibility below is from registry peer ranges plus the executed checks above. Phase 1 starts with that spike. Budgets are proposals until it runs |

## Decisions

| Area | Decision | Why, in one line |
| --- | --- | --- |
| Project location | **`web/` becomes the Vite project.** Build output `web/dist/` is what Go embeds. Root `package.json` stays for release tooling | Keeps every existing path (`web/`, Dockerfile, docs) and avoids mixing a 40-dependency app into the semantic-release manifest |
| Language | TypeScript 6.0.3, `strict`, plus `noUncheckedIndexedAccess` | TS 7 has no JS API yet, which `typescript-eslint` needs ([details](#typescript-6-now-7-later)) |
| UI | React 19.3.1 | Current stable |
| Build | Vite 8.3.4 (Rolldown/Oxc) with `@vitejs/plugin-react` 6.1.2 | Current; Node `^20.19 \|\| >=22.12` |
| Data layer | TanStack Query 5.104.4 + Zod 4.6.7 at the API boundary | Polling, caching, abort and retry are what `refresh()` hand-rolls today; Zod makes bad payloads a typed, local failure |
| Router | **TanStack Router 1.170.45, code-based routes, hash history** | Typed params and search; hash URLs keep every existing link ([why not React Router](#router-choice)) |
| Styling | **CSS Modules + one `tokens.css`** | Zero runtime, zero extra build plugins, tokens are the design system, lintable ([why not Tailwind](#styling-choice)) |
| Charts | Hand-built SVG components | 60-240 points per series; the current code already draws SVG |
| Unit/component tests | Vitest 5.0.5, **component tests in Vitest browser mode (real Chromium)** | `<dialog>`, form validity and focus are the product's behaviour; jsdom emulates them poorly and needs Node `>=24.15` |
| E2E | `@playwright/test` against the **real Go binary in demo mode** | Most existing scripts already do this ([map](#porting-the-existing-tests)) |
| Hooks | prek, local hooks, `language: system` | [Config](#prek-hooks) |
| Delivery | Strangler: new app at `/`, frozen old console at `/legacy/` until each page is ported | Every phase ships a working console |
| Node | **Bump CI and Docker from 24.10.0 to 24.22.0 (current LTS)** | `jsdom` 30 (fallback) needs `>=24.15`; newer tooling assumes it. Also required: `react-doctor` needs `^20.19 \|\| >=22.13`, which this laptop's Node 21.7 fails |

---

## 1. Target layout

```text
web/
  package.json            package-lock.json
  index.html              Vite entry: static boot shell + <div id="root">
  vite.config.ts          vitest.config.ts       playwright.config.ts
  tsconfig.json           tsconfig.app.json      tsconfig.node.json      (project references)
  eslint.config.js        stylelint.config.js    .prettierrc.json
  scripts/
    precompress.mjs       .br/.gz next to each text asset (node:zlib, no dependency)
    check-dist.mjs        fails the build if dist/assets has an unhashed file, or a budget is exceeded
  public/                 favicon.svg, robots.txt (copied verbatim)
  src/
    main.tsx              app/App.tsx   app/providers.tsx   app/router.tsx   app/routes/*.tsx
    api/                  client.ts  errors.ts  keys.ts  schemas/*.ts  endpoints/*.ts
    auth/                 AuthProvider.tsx  session.ts  capabilities.ts  LoginScreen.tsx
    components/           StatusPill  MetricCell  DataTable  Button  Dialog  Toast  Nav
                          Segmented  Field  IdChip  Icon  EmptyState  ConnectionIndicator  ...
    features/
      dashboard/  server-overview/  telemetry/  events/  maintenance/
      saved-ids/  integrations/  reboots/  backups/  server-forms/
    lib/                  format.ts  status.ts  time.ts  csv.ts  download.ts  ids.ts
                          legacy-hash.ts  roster.ts  deploy-watch.ts  settings-patch.ts ...
    styles/               tokens.css  base.css  fonts.css
    test/                 setup.ts  fake-api.ts  fixtures/*.ts
  e2e/                    specs/*.spec.ts  fixtures.ts  (server + page fixtures)
  legacy/                 frozen copy of today's app.js, styles.css, index.html, assets/  (deleted in Phase 6)
  dist/                   build output, gitignored, embedded by Go
```

Naming rule: a feature folder owns its components, hooks, query hooks and tests. `components/` holds only things used by two or more features. `lib/` holds pure functions with no React import (this is where today's `app.js` logic lands and where most unit tests live).

`.gitignore` adds `web/node_modules/`, `web/dist/*` with an exception `!web/dist/.gitkeep`. See [Go embedding](#8-go-embedding-and-serving) for why the placeholder exists.

---

## 2. Dependencies (pinned)

Exact versions, no ranges, committed lockfile, `npm ci --ignore-scripts` everywhere (matches the repo's existing supply-chain posture). Registry-verified 2026-10-17.

### Runtime

| Package | Version | Notes |
| --- | --- | --- |
| `react`, `react-dom` | 19.3.1 | 19.2 added `<Activity>` and `useEffectEvent` ([React 19.2 post](https://react.dev/blog/2025/10/01/react-19-2)). 19.3.0 shipped 2026-09-09; I could verify its notes only through secondary write-ups, not the react.dev post |
| `@tanstack/react-query` | 5.104.4 | peer: React 18 or 19 |
| `@tanstack/react-router` | 1.170.45 | peer: React `>=18` |
| `zod` | 4.6.7 | API boundary validation; `z.looseObject` so new server fields never break the client |
| `@fontsource/barlow`, `@fontsource/barlow-semi-condensed`, `@fontsource/ibm-plex-mono` | 5.3.0 each | SIL OFL 1.1, latin subset imported file by file |

### Build, types, lint, format

| Package | Version | Notes |
| --- | --- | --- |
| `vite` | 8.3.4 | engines `^20.19.0 \|\| >=22.12.0`. Rolldown replaced esbuild+Rollup in 8.0 (2026-03-12) ([announcement](https://vite.dev/blog/announcing-vite8)) |
| `@vitejs/plugin-react` | 6.1.2 | peer `vite ^8`. v6 uses Oxc for Fast Refresh, no Babel |
| `typescript` | 6.0.3 | see [below](#typescript-6-now-7-later) |
| `@types/react`, `@types/react-dom` | 19.3.0 | |
| `@types/node` | 24.19.2 | match the Node 24 runtime, not the registry's `latest` (26.x) |
| `eslint` | 10.13.0 | flat config only. engines `^20.19 \|\| ^22.13 \|\| >=24` |
| `@eslint/js` | 10.0.1 | |
| `typescript-eslint` | 8.71.2 | peers: `eslint ^8.57 \|\| ^9 \|\| ^10`, `typescript >=4.8.4 <6.1.0` |
| `eslint-plugin-react-hooks` | 7.1.1 | peer includes `eslint ^10`; ships the React Compiler-powered rules |
| `eslint-plugin-react-refresh` | 0.5.7 | peer `eslint ^9 \|\| ^10` |
| `globals` | 17.13.0 | |
| `prettier` | 3.9.10 | |
| `stylelint`, `stylelint-config-standard` | 17.16.0, 40.0.0 | enforces the [design guards](./design-brief.md#83-automated-guards-cheap-run-in-prek-and-ci) |
| `react-doctor` | 0.9.18 (devDependency) | pinned for reproducibility instead of `npx -y react-doctor@latest`; see [react-doctor](#react-doctor) |

**Deliberately not in the list: `eslint-plugin-jsx-a11y`.** Its latest release (6.10.2, 2024-10) declares `eslint ^3 ... ^9`, so it would force ESLint 9. `react-doctor` already ships ported jsx-a11y rules (it flagged `alt-text`, `click-events-have-key-events`, `no-static-element-interactions` in my sample run), and `@axe-core/playwright` checks the rendered DOM. If you prefer the classic plugin, the one-line alternative is ESLint 9.39.5 (latest 9.x) with `eslint-plugin-jsx-a11y` 6.10.2; nothing else in the stack changes.

### Test

| Package | Version | Notes |
| --- | --- | --- |
| `vitest` | 5.0.5 | engines `^22.12 \|\| ^24 \|\| >=26`; peer `vite ^6.4 \|\| ^7 \|\| ^8` |
| `@vitest/browser-playwright` | 5.0.5 | browser mode provider; peer `playwright *` |
| `vitest-browser-react` | 2.3.0 | peers React 18/19, vitest 4/5 |
| `@vitest/coverage-v8` | 5.0.5 | |
| `@playwright/test` | 1.62.1 | **match root `playwright` 1.62.1** so one browser install serves both. The registry's latest is 1.64.1; bump both together in a dedicated PR |
| `@axe-core/playwright` | 4.14.0 | serious/critical violations fail the e2e run |

No `jsdom`, `happy-dom`, `@testing-library/*` or `msw` in the baseline. Component tests inject a typed in-memory fake API into the client (see [Test strategy](#7-test-strategy)). If browser mode proves too heavy for contributors, the fallback is `jsdom` 30.1.3 plus `@testing-library/react` 16.3.3 and `@testing-library/dom` 10.4.2. That works on the bumped Node (jsdom needs `^22.22.2 \|\| ^24.15 \|\| >=26`) but loses `<dialog>` fidelity.

### TypeScript 6 now, 7 later

`typescript@latest` on npm is **7.0.2** (GA 2026-07-08, native compiler). It ships **no JavaScript API**, and `typescript-eslint` 8.71.2 declares `typescript <6.1.0`. The TypeScript team says 7.1 will ship "a new (and different) API" ([Announcing TypeScript 7.0](https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/)). So:

- Pin `typescript` 6.0.3 for `tsc`, `typescript-eslint` and editors.
- Write `tsconfig` so it also passes under 7.0 (no `baseUrl`, `moduleResolution: "bundler"`, `strict`, explicit `types`, explicit `rootDir`, no `target: es5`), which is what 7.0 requires. Code that is clean on 6.0 compiles the same on 7.0, per the announcement.
- If `tsc -b` ever exceeds ~10 s, add the official dual install (`"typescript": "npm:@typescript/typescript6@^6.0.2"` plus `"@typescript/native": "npm:typescript@^7.0.2"`) and run `tsc` from the native one. Not needed at this size.
- Revisit when `typescript-eslint` supports the 7.1 API.

Note that `react-doctor` bundles its own `typescript >=5.0.4 <6`, so `npm install` places a second TypeScript under it. Expected, harmless.

### Router choice

`react-router` 8.4.0 is current (peers React `>=19.2.7`, Node `>=22.22`) and would work. TanStack Router wins here for three concrete reasons: typed `params` and `search` (the app has `?serverId=`, ranges and filters, all currently untyped strings), no framework mode or loaders competing with TanStack Query, and first-class hash history. The cost is that route definitions are more verbose; with ten routes that is fine. Code-based routes avoid the file-generation Vite plugin (`@tanstack/router-plugin`), which is one fewer moving part.

URL parity: today's links are `#servers/abc`, `#telemetry?serverId=abc`, `#backups/settings/profiles`. TanStack's hash history writes `#/servers/abc`. A 15-line `legacy-hash.ts` rewrites the old form to the new on boot and is tested against every case in today's `parseLocationHash`. The OIDC callback returns to `/` or `/?signin=failed`, which both still work.

### Styling choice

CSS Modules plus custom properties. Tailwind v4 (4.3.3) is capable and `@tailwindcss/vite` works, but its utility-class JSX is the usual carrier of generic defaults, dense bespoke tables get little from it, and design tokens live more honestly in one `tokens.css` that stylelint can police. `vanilla-extract` (`@vanilla-extract/css` 1.21.3) gives typed tokens at the price of another build plugin and a second place to read styles. The one real downside of CSS Modules is untyped class names; mitigate with a `*.module.css.d.ts` generator only if it becomes a problem (it has not on projects this size).

### Charts

Hand-built `<TimeSeries>` and `<Sparkline>` in SVG, about 200 lines total, reusing the existing `chart()` logic (gap detection at 45 s, ceiling `*1.15`, per-field `observedAt` clipping, accessible summary plus data table). Data volume is small: the service keeps up to one hour at 15 s, so 240 points per series. A chart library would add bytes and a look to fight. **Escape hatch:** `uPlot` 1.6.32 (MIT, canvas) if a future requirement needs zoom/brush or tens of thousands of points.

---

## 3. Tooling configuration (draft)

### `tsconfig.app.json`

```jsonc
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2023", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "noImplicitOverride": true,
    "noFallthroughCasesInSwitch": true,
    "verbatimModuleSyntax": true,
    "isolatedModules": true,
    "skipLibCheck": true,
    "noEmit": true,
    "rootDir": ".",
    "types": ["vite/client", "node"],
    "tsBuildInfoFile": "node_modules/.tmp/tsconfig.app.tsbuildinfo"
  },
  "include": ["src", "e2e", "vite.config.ts", "vitest.config.ts", "playwright.config.ts"]
}
```

### `eslint.config.js` (flat)

```js
// Draft. `defineConfig`/`globalIgnores` from "eslint/config" is what ESLint's own docs use (typescript-eslint's
// older `tseslint.config()` helper is the deprecated style). The exact name of the react-hooks flat preset
// (`configs.flat.recommended` in 7.x) is the one thing to confirm against the plugin README in the Phase 1 spike.
import js from '@eslint/js';
import globals from 'globals';
import { defineConfig, globalIgnores } from 'eslint/config';
import tseslint from 'typescript-eslint';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';

const bannedMetricWords = '/check failed|collection failed|awaiting sample/i';

export default defineConfig([
  globalIgnores(['dist', 'legacy', 'node_modules', 'coverage']),
  js.configs.recommended,
  tseslint.configs.strictTypeChecked,
  tseslint.configs.stylisticTypeChecked,
  reactHooks.configs.flat.recommended,          // includes the compiler-powered rules in 7.x
  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      globals: globals.browser,
      parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname },
    },
    plugins: { 'react-refresh': reactRefresh },
    rules: {
      'react-refresh/only-export-components': ['error', { allowConstantExport: true }],
      '@typescript-eslint/switch-exhaustiveness-check': 'error',
      '@typescript-eslint/no-floating-promises': 'error',
      '@typescript-eslint/consistent-type-imports': 'error',
      'no-restricted-syntax': ['error',
        { selector: `Literal[value=${bannedMetricWords}]`, message: 'Metric cells never print collector errors. See design-brief.md 5.3.' },
        { selector: `JSXText[value=${bannedMetricWords}]`, message: 'Metric cells never print collector errors. See design-brief.md 5.3.' },
        { selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']", message: 'Not allowed. The legacy app escaped everything by hand; React does it by default.' }],
    },
  },
  { files: ['e2e/**', '*.config.ts'], languageOptions: { globals: globals.node } },
]);
```

`switch-exhaustiveness-check` plus a `satisfies Record<ActionName, ...>` table replaces the old `scripts/verify.sh` guard that compared rendered `data-action` values to `case` labels.

### `vite.config.ts`

```ts
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,              // keeps the Go binary small; upload maps from CI if wanted
    assetsInlineLimit: 0,          // no data: URIs except the hand-written login placeholder
    cssCodeSplit: true,
  },
  server: {
    port: 5173,
    proxy: { '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false } },   // go run . in demo mode
  },
});
```

Vite's default output puts every build artifact in `dist/assets/` with a content hash in the file name (`index-Abc12345.js`). That is the property the Go handler relies on to mark `/assets/*` immutable. `scripts/check-dist.mjs` fails the build if any file in `dist/assets` lacks a hash.

### `package.json` scripts

```jsonc
{
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build && node scripts/precompress.mjs && node scripts/check-dist.mjs",
    "typecheck": "tsc -b",
    "lint": "eslint --max-warnings 0 . && stylelint --max-warnings 0 \"src/**/*.css\"",
    "format:check": "prettier --check .",
    "doctor": "react-doctor --no-score --no-supply-chain",
    "test": "vitest run",
    "test:watch": "vitest",
    "e2e": "playwright test",
    "check": "npm run typecheck && npm run lint && npm run format:check && npm run doctor && npm test"
  },
  "engines": { "node": ">=24.15.0" }
}
```

### `react-doctor`

What I verified by running `react-doctor@0.9.18` (needs Node `^20.19 \|\| >=22.13`; on this laptop's Node 21.7 it failed to load its native `oxc-parser` binding, which is another reason for the Node bump):

- **Flags** (from `--help`): `--staged` (pre-commit mode, exits 0 when nothing is staged), `--scope changed --base <ref>` (only new issues versus a branch), `--blocking error|warning|none` (default `error`; exit 1 on errors), `--no-score` / `--no-telemetry` (skip the score API, share URL and crash reporting), `--no-supply-chain` (skip the Socket.dev dependency scan), `--json`, `--category <name>`, `rules` and `why <file:line>` subcommands, and `design` for focused UI diagnostics.
- **Config:** `doctor.config.ts` (or `.js`, `.mjs`, `.json`, or a `reactDoctor` key in `package.json`).
- **Network by default:** score API, Sentry crash reports and Socket.dev. Hooks pass `--no-score --no-supply-chain` to stay offline and fast; CI may run the supply-chain scan as a non-blocking job.
- **What it flagged** on my sample (10 findings, 1 error): derived value copied into state (`no-derived-state`), fetch inside an effect (`no-fetch-in-effect`), `fetch` response used without a status check, `.then` setting state with no `catch`, component defined inside a component (two rules), click handler without keyboard handler, interaction on a static element, array index used as key, and image without alt (the one error). It did **not** flag `dangerouslySetInnerHTML`, `target="_blank"` without `rel`, the purple gradient or Inter (hence the lint rules in `eslint.config.js` and stylelint).
- **`design` command:** only reported `no-img-without-dimensions` on that sample. It is a layout-stability check, not a visual-slop check.
- Its fetch-in-effect rule will push exactly the architecture chosen here: all server state goes through TanStack Query, never `useEffect`.

### prek hooks

`.pre-commit-config.yaml` at the repo root. Validated with `prek 0.5.5` (`validate-config` passes; `prek list` shows all six). prek splits `entry` into arguments and runs it directly, so on Windows a `.sh` wrapper cannot execute; the wrapper is Node, which the repo already requires.

```yaml
# Fast checks only. Anything slower than ~10 s (unit tests, build, e2e) runs in CI and on pre-push.
minimum_prek_version: '0.5.0'
default_stages: [pre-commit]
fail_fast: false

repos:
  - repo: local
    hooks:
      - id: web-typecheck
        name: web / tsc --noEmit
        language: system
        entry: node scripts/web-hook.mjs tsc -b
        pass_filenames: false
        files: ^web/.*\.(ts|tsx|json)$
        exclude: ^web/(legacy|dist|node_modules)/

      - id: web-eslint
        name: web / eslint
        language: system
        entry: node scripts/web-hook.mjs eslint --max-warnings 0 --no-warn-ignored
        files: ^web/.*\.(ts|tsx|js|mjs)$
        exclude: ^web/(legacy|dist|node_modules)/

      - id: web-stylelint
        name: web / stylelint
        language: system
        entry: node scripts/web-hook.mjs stylelint --max-warnings 0
        files: ^web/src/.*\.css$

      - id: web-prettier
        name: web / prettier --check
        language: system
        entry: node scripts/web-hook.mjs prettier --check --ignore-unknown
        files: ^web/.*\.(ts|tsx|js|mjs|css|json|md)$
        exclude: ^web/(legacy|dist|node_modules)/|package-lock\.json$

      - id: web-react-doctor
        name: web / react-doctor (staged files)
        language: system
        entry: node scripts/web-hook.mjs react-doctor --staged --no-score --no-supply-chain --no-color
        pass_filenames: false
        files: ^web/src/.*\.(ts|tsx)$

      - id: web-unit
        name: web / vitest (pre-push)
        language: system
        entry: node scripts/web-hook.mjs vitest run --reporter=dot
        pass_filenames: false
        files: ^web/(src|e2e)/
        stages: [pre-push]
```

`scripts/web-hook.mjs` (verified with stub binaries: runs with `cwd=web/`, receives `src/a.ts` for `web/src/a.ts`, and the `exclude` kept `web/legacy` out):

```js
#!/usr/bin/env node
// Runs a toolchain binary from web/ and rewrites prek's repo-relative paths (web/src/x.ts -> src/x.ts).
import { spawnSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const web = resolve(dirname(fileURLToPath(import.meta.url)), '..', 'web');
const [tool, ...args] = process.argv.slice(2);
const mapped = args.map((arg) => (arg.startsWith('web/') ? arg.slice(4) : arg));
const win = process.platform === 'win32';
const result = spawnSync(resolve(web, 'node_modules', '.bin', win ? `${tool}.cmd` : tool), mapped, {
  cwd: web, stdio: 'inherit', shell: win,
});
process.exit(result.status ?? 1);
```

Install: `prek install` (pre-commit) and `prek install --hook-type pre-push`. CI runs `prek run --all-files --show-diff-on-failure` and `prek run --all-files --stage pre-push` (flags confirmed with `prek run --help`). Pin prek in CI (`pip install prek==0.5.5`; `setup-python` is already in the workflow). Gotcha: hooks resolve binaries from `web/node_modules/.bin`, so contributors run `npm --prefix web ci` first; the wrapper prints that hint when the binary is missing (add in Phase 1).

---

## 4. State, data and routing

### 4.1 Where state lives

| Kind | Lives in | Rule |
| --- | --- | --- |
| Server state (everything fetched) | TanStack Query | Never `useState` + `useEffect` + `fetch` (react-doctor flags it) |
| Navigation and filter state | Router path and validated search params | Shareable; survives reload |
| Session (identity, CSRF, capabilities, role, mode) | `AuthProvider` (React context + `useSyncExternalStore`) | The single place that knows who is signed in |
| Transient UI (open dialog, selected row, filter text) | Local component state | Lifted only when two siblings need it |
| Persisted preference (display timezone) | `localStorage`, keyed per identity | Same key as today: `rsdw-display-timezone:<authMode>:<subject>` |
| Admin token (token mode) | `sessionStorage['rsdw-admin-token']` | Unchanged; never in a URL or `localStorage` |

### 4.2 Query keys and polling

All keys are prefixed with the session id (`<mode>:<subject>:<role>:<csrf>`, the same identity string `applyAuth` builds) so a different identity can never read another identity's cache. The `QueryClient` is also rebuilt and `clear()`ed whenever that id changes, which replaces `state.epoch` ([auth parity](#5-auth-csrf-and-session-parity)).

| Query | Endpoint | Enabled when | Interval | Replaces |
| --- | --- | --- | --- | --- |
| `auth` | `GET /api/auth` | always | none; refetch on focus | `discoverAuth()` |
| `bootstrap` | `GET /api/bootstrap` | authenticated | 10 s | `state.servers`, `state.deletions`, cluster name, `state.mode` |
| `events` (infinite) | `GET /api/events` | `can('events')` | 10 s | `state.events`, `eventTotal`, `eventWarnings`, `eventCritical` |
| `users` | `GET /api/users` | `can('create')` | 10 s | `state.users` |
| `integrations` | `GET /api/integrations` | `can('integrations')` | 10 s | `integrations`, `deliveries`, `alertRules`, `pendingRestarts`, `integrationsDemo` |
| `reboots` | `GET /api/reboots` | `can('reboots')` | 10 s | `reboots`, `rebootHistory`, `rebootsAvailable`, `rebootsDemo` |
| `backups` | `GET /api/backups` | `can('backups')` | 10 s | the seven `backup*` data fields |
| `telemetry` | `GET /api/servers/:id/telemetry?range=` | page is telemetry or overview and a server is selected | 10 s | `telemetry`, `telemetryCache`, roster fields |
| `logs` | `GET /api/servers/:id/logs?tail=100` | telemetry page, `can('logs')`, server not stopped | 10 s | `state.logs` |
| `imageTags` | `GET /api/image-tags` | create or update dialog open | none; `staleTime` 5 min | `loadImageTags()` |

Polling policy reproduces the old `setInterval` guard: poll only when not paused, the tab is visible, **no dialog is open**, and the focused element is not an `INPUT` or `SELECT`. Implemented once as `usePollingInterval()` returning `10_000 | false`, with `refetchOnWindowFocus` standing in for the `visibilitychange` handler.

**Failure model (a deliberate change, driven by the owner's complaint):** only `auth` and `bootstrap` can put the app in a degraded state. Today any single failed request raises the global red banner. In the new app a failed `events`, `users`, `integrations`, `reboots`, `backups`, `telemetry` or `logs` query shows a quiet inline "couldn't load {thing}, retry" in its own region and leaves the rest of the page intact. `reboots` failure sets `rebootsAvailable:false` semantics and `backups` failure sets `backupAvailable:false` semantics through `select`, exactly as the legacy `.catch` handlers did.

### 4.3 Routes (hash history, all current URLs preserved)

| Legacy hash | New route | Search params (zod-validated) | Guard |
| --- | --- | --- | --- |
| `#dashboard` (default, also any unknown hash) | `/dashboard` | none | none |
| `#servers/<id>` | `/servers/$id` | none | needs `can('telemetry')`. Malformed or unknown id: redirect to dashboard with the `routeError` banner "Server overview unavailable: malformed server ID." / "...unknown server ID "x"." |
| `#telemetry?serverId=` | `/telemetry` | `serverId?`, `range` (`60s`/`5m`/`1h`) | `can('telemetry')`. No `serverId` means the first server |
| `#events?serverId=` | `/events` | `serverId?`, `q?`, `category?`, `since` (`24h`/`all`) | `can('events')` |
| `#maintenance?serverId=` | `/maintenance` | `serverId?` | `can('maintenance')`. No `serverId` means the first server |
| `#reboots?serverId=` | `/reboots` | `serverId?` | `can('reboots')` |
| `#users` | `/users` | none | `can('create')` (client alias, see [4.4](#44-capability-gating)) |
| `#integrations`, `#integrations/discord`, `#integrations/webhook` | `/integrations` | `provider?` (`discord`/`webhook`) | `can('integrations')`. The two old paths rewrite to the `provider` filter |
| `#backups`, `#backups/schedules`, `#backups/settings/profiles`, `#backups/settings/storage` | `/backups`, `/backups/schedules`, `/backups/settings/$tab` | none | `can('backups')` |

Rules carried over from `parseLocationHash`/`navigate()`: scrolling to top on navigation; a route the identity cannot use falls back to the dashboard; the server selector is hidden on `servers`, `users`, `integrations`, `reboots` and `backups`; `serverId` presence ("scoped") locks the selector, and when the selector changes on telemetry/maintenance/events/reboots the URL is replaced (not pushed).

### 4.4 Capability gating

`GET /api/auth` returns 15 capability keys (`dashboard, telemetry, events, maintenance, integrations, reboots, backups, create, delete, restart, stop, start, update, logs, updateCheck`). There is **no `users` key**, and the UI asks for several names the server never sends, so `can()` carries a client-side alias table. Port it verbatim into `auth/capabilities.ts` with a unit test:

`servers` to `telemetry`; `users`, `add-user`, `edit-user`, `delete-user`, `add-server` to `create`; `edit-settings`, `add-admin-id`, `restore-server` to `maintenance`; `add-integration`, `edit-integration`, `test-integration` to `integrations`; `add-reboot`, `edit-reboot`, `delete-reboot`, `preview-reboot` to `reboots`; every `backups-settings`, `run-backup`, `add-backup-schedule`, `edit-backup-schedule`, `delete-backup-schedule`, `run-scheduled-backup`, `run-scheduled-backup-menu`, `delete-backup-schedule-menu`, `backup-details`, `download-backup`, `retry-backup`, `new-backup-profile`, `delete-backup-profile`, `edit-backup-profile`, `import-backup-profile`, `export-backup-profile`, `create-server-from-backup` to `backups`.

`handleAction` carries a **second** table that maps UI actions to capabilities before running them: `restart`, `stop`, `start`, `update` to themselves; `check-update` to `updateCheck`; `add-server` to `create`; `view-events`, `event-category`, `select-event`, `copy-event`, `export-events`, `load-older-events` to `events`; `copy-endpoint` to `maintenance`; `export-telemetry` to `telemetry`; `export-logs` and `refresh-logs` to `logs`; `add-daily-time` and `remove-daily-time` to `reboots`. Port both into one `actionCapability` constant so a button is hidden by the same rule that would reject its click.

In React this becomes a typed `can(capability: Capability)` where `Capability` is the union of server keys and aliases, so an unknown capability is a compile error instead of a silent `false`.

---

## 5. Auth, CSRF and session parity

Source of truth: `auth.go`, [docs/oidc.md](../oidc.md), `web/app.js` (`api`, `applyAuth`, `discoverAuth`, `requireLogin`, `logout`, `submitLogin`), and [api-types.md](./api-types.md#authentication-model-affects-every-call). Every row is a behaviour to keep and to test.

| # | Behaviour today | New implementation | Test |
| --- | --- | --- | --- |
| A1 | Boot calls `GET /api/auth` first. If it reports `oidc` while the client thought `token`, it clears the stored token and asks again | `AuthProvider` runs the `auth` query before anything else; on mode change drop `sessionStorage` token and refetch | unit |
| A2 | **Token mode:** `Authorization: Bearer <token>` from `sessionStorage` on every call; sign-in validates with `POST /api/session` (204) *then* stores the token | `api/client.ts` adds the header only when `mode !== 'oidc'`. `LoginScreen` posts `/api/session` first | e2e |
| A3 | **OIDC mode must never send `Authorization`**: the server treats any such header as unauthenticated (verified in `auth.go` `authenticate`) | Same guard in the client | unit: OIDC mode request has no `Authorization` |
| A4 | OIDC login is a full navigation: `location.assign('/api/auth/login')` | `<a href="/api/auth/login">` styled as a button (works without JS state) | e2e (Go OIDC fixture) |
| A5 | OIDC callback lands on `/` or `/?signin=failed`; the param is stripped with `history.replaceState` and shows "Sign in failed or this identity has no assigned role..." | Read once at boot, strip, store in login-screen state | e2e |
| A6 | CSRF: every request carries `X-CSRF-Token` from `/api/auth`; the browser adds `Origin` | Client adds the header when a token is present | unit |
| A7 | **Session swap detection:** in OIDC mode, if an OK response's `X-RSDW-Session` differs from the remembered CSRF token, treat as signed out and discard the response | Client wrapper compares and throws `StaleSessionError`; `AuthProvider` goes to signed-out | unit + e2e (two tabs) |
| A8 | **`epoch`:** every identity change or sign-out aborts in-flight requests, clears all protected state, and rejects any response that arrives afterwards | New `QueryClient` per identity (`key={identity}`), `clear()` on change, `AbortController` from Query, plus the A7 check. Mutations resolve into an unmounted observer and are dropped | unit + the Go OIDC "delayed callback after logout" scenario |
| A9 | `401` on any path except `/api/session`: clear protected state, remove the stored token, show login. `401` from `/api/auth/logout` counts as success | Central `onUnauthorized` in the client | unit |
| A10 | Logout in OIDC mode: `POST /api/auth/logout` with `X-CSRF-Token`; on failure keep the CSRF token (`logoutCSRF`), show "Could not confirm server sign out. Retry sign out before leaving this browser.", show Retry, and **suspend polling** | `useLogout()` mutation; while `logoutCsrf` is set, all queries are disabled | e2e |
| A11 | Token-mode logout just forgets the token | same | e2e |
| A12 | Identity string `mode:subject:role:csrf` decides "did the identity change" (re-load display timezone, clear state) | `session.ts` derives it once; everything keys off it | unit |
| A13 | `required:false` (demo without a token) hides sign-out; role badge shows Admin or Viewer | Same condition in the top bar | e2e |
| A14 | Admin token field: `type=password`, `autocomplete=current-password`, `aria-invalid` cleared on input, error focused | Keep in `LoginScreen`; token mode inline field (decision D4 in the brief) | e2e + axe |
| A15 | Storage blocked (private mode): "Browser session storage is blocked. Enable it for this site, then sign in again." | Keep the message | unit |
| A16 | Viewer role gets different `bootstrap` and `telemetry` shapes and only those two routes | Zod `discriminatedUnion` on role; type-level, so a viewer never reads admin fields | unit + contract |

Hardening to consider later, **not** in scope: token mode keeps the admin token in `sessionStorage`, which any XSS can read. A strict CSP is cheap once the bundle has no inline script (see [Go embedding](#8-go-embedding-and-serving)).

---

## 6. Mapping the legacy app

### 6.1 Every `state.*` field (76 keys plus `state.mode`)

| Legacy field(s) | New home |
| --- | --- |
| `servers`, `deletions`, `mode` | `bootstrap` query. `deletions` may be `null` in the payload, normalise in the Zod schema |
| `authRequired`, `loginBusy`, `authMode`, `identity`, `csrfToken`, `role`, `capabilities`, `authSubject`, `signInFailed`, `logoutCSRF`, `epoch` | `AuthProvider` store (`session.ts`). `loginBusy` is `useMutation().isPending`; `epoch` is the per-identity `QueryClient` |
| `page`, `integrationView`, `backupView`, `routeScope`, `routeError` | Router location (path + search). `routeError` is router `state` set by the redirect guard |
| `serverId` | `serverId` search param, plus the selector component |
| `fleetFilter` | Dashboard local state, mirrored in `?filter=` (additive) |
| `dashboardView` | **Removed.** The Row/Table toggle showed the same data twice ([brief 7.1](./design-brief.md#71-dashboard-focal-point-the-fleet-status-line)) |
| `events`, `eventTotal`, `eventWarnings`, `eventCritical` | `events` infinite query + its `select` |
| `query`, `category`, `eventRange` | `events` search params (`q`, `category`, `since`); search input debounced 300 ms as today |
| `selectedEventId` | Events page local state |
| `users` | `users` query |
| `integrations`, `deliveries`, `alertRules`, `pendingRestarts`, `integrationsDemo` | `integrations` query |
| `reboots`, `rebootHistory`, `rebootsAvailable`, `rebootsDemo`, `displayTimezone` | `reboots` query; `displayTimezone` in a `useDisplayTimezone()` hook over `localStorage` |
| `backups`, `backupProfiles`, `backupSchedules`, `backupStorage`, `backupAvailable`, `backupDemo`, `backupLimits` | `backups` query |
| `backupProfileQuery`, `backupProfileType`, `selectedBackupProfileId`, `backupServerFilter`, `backupAttentionOnly` | Backups feature local state (`useReducer`) |
| `range` | `range` search param on telemetry |
| `telemetry`, `telemetryCache`, `rosterObservation`, `rosterDeadline` | `telemetry` query (key includes server and range gives the per-range cache for free) + `useRosterFreshness()` which ports the 45 s monotonic-clock expiry and the `performance.now()` deadline logic from `acceptTelemetry` |
| `logs`, `logQuery` | `logs` query; `logQuery` local state with client-side line filter |
| `paused` | `PollingContext`, toggled from the connection indicator |
| `loaded`, `lastUpdated`, `refreshing`, `request` | Derived: `isSuccess`, `dataUpdatedAt`, `isFetching`, Query's `signal` |
| `modalAction`, `modalServerId`, `modalUserId`, `modalIntegrationId`, `modalRebootId`, `modalBackupId`, `modalBackupScheduleId`, `modalBackupProfileRevision`, `modalInitialSettings` | Gone as globals: each dialog is a component that receives its target as props. `modalBackupProfileRevision` and `modalInitialSettings` live in that dialog's form state |
| `modalBusy` | the dialog's `useMutation().isPending` |
| `previewSequence` | `usePreviewReboot()` mutation; `onSuccess` ignores results whose variables no longer match the form (last-write-wins) |
| `deployWatches` | `DeployWatchProvider` (per-session, in memory), `useDeployWatch(serverId)`; port `beginDeployWatch`, `deployProgress`, `DEPLOY_WATCH_TIMEOUT_MS` (10 min) as pure functions |

### 6.2 Every `data-action` (67 handler cases)

`web/app.js` renders 65 distinct `data-action` values and `handleAction` has 67 `case` labels (adds `delete-backup-schedule` and `view-server`, which are reached programmatically). All 67 are listed. "Dialog" means a component under `features/*` opened by a `Dialog` host; "mut" means a `useMutation`.

| Area | Actions | New component / hook |
| --- | --- | --- |
| Session | `login`, `logout`, `close-login`, `retry` | `LoginScreen` (link or inline form), `useLogout()`, no dialog so `close-login` disappears if D4 is accepted, `retry` = `queryClient.refetchQueries` on the error state |
| Server lifecycle | `restart`, `stop`, `start`, `update`, `check-update` | `ConfirmServerActionDialog` for the first four (mut `POST .../actions/<name>`; `update` also loads `imageTags` and sends `{imageTag}`); `check-update` is a plain mut with a toast and `refetch` |
| Server forms | `add-server`, `edit-settings`, `delete`, `toggle-resource`, `retry-image-tags`, `add-admin-id` | `CreateServerDialog` (multipart when a `.sav` is attached), `EditSettingsDialog` (builds the patch with the ported `editSettingsPatch`), `DeleteServerDialog` (receipt-aware, `retry-deletion`), `ResourceLockToggle` (memory = (2 + players) GiB, CPU = 500 m x players), `ImageTagSelect` retry button, `AdminIdsField` |
| Navigation helpers | `view-server`, `view-events`, `dashboard-view`, `fleet-filter` | Router `<Link>`s; `dashboard-view` is removed; `fleet-filter` is the segmented control / count click |
| Clipboard | `copy-endpoint`, `copy-event` | `IdChip` / `CopyButton` using `navigator.clipboard.writeText` + toast ("Endpoint copied.", "Event JSON copied.") |
| Events | `event-category`, `select-event`, `load-older-events`, `export-events` | Segmented control (search param); row button (`data-testid="event-row"`, `aria-pressed`); `fetchNextPage` (appends and de-duplicates by `id`); export fetches `limit=2500` then `csv.ts` |
| Telemetry | `export-telemetry`, `refresh-logs`, `export-logs` | `exportCsv(telemetryRows())`; `logs` `refetch()` + toast "Server logs refreshed."; download of the filtered log text ("Log download ready.") |
| Reboots | `add-reboot`, `edit-reboot`, `delete-reboot`, `add-daily-time`, `remove-daily-time`, `preview-reboot` | `RebootDialog` (mode switch cron/interval/daily, dynamic time list min 1, warning minutes, acknowledgement), `DeleteRebootDialog`, `usePreviewReboot()` (next five runs shown in the display zone) |
| Integrations | `add-integration`, `edit-integration`, `test-integration` | `IntegrationDialog` (Discord vs webhook fieldsets, quiet hours, server and rule checklists, server-side `fields` errors into an error summary), `useTestIntegration()` ("Test queued...") |
| Saved IDs | `add-user`, `edit-user`, `delete-user` | `SavedIdDialog`, `DeleteSavedIdDialog` |
| Backups: runs | `run-backup`, `backup-details`, `download-backup`, `retry-backup`, `backup-attention`, `run-scheduled-backup`, `run-scheduled-backup-menu` | `RunBackupDialog` (resolved-source preview is derived from server state and profile), `BackupDetailsDialog` (its primary button downloads the bundle when the run succeeded), blob download helper, retry mut, attention filter toggle, `RunScheduledBackupDialog` |
| Backups: schedules | `add-backup-schedule`, `edit-backup-schedule`, `delete-backup-schedule`, `delete-backup-schedule-menu`, `add-backup-time`, `remove-backup-time`, `backup-view-schedules`, `backups-settings` | `BackupScheduleDialog`, `DeleteBackupScheduleDialog`, time list, router links to `/backups/schedules` and `/backups/settings/profiles`. The two `*-menu` actions use the schedule `<select>` |
| Backups: profiles | `new-backup-profile`, `edit-backup-profile`, `delete-backup-profile`, `import-backup-profile`, `export-backup-profile`, `select-backup-profile`, `clear-backup-profile-filter`, `add-backup-item`, `remove-backup-item` | `BackupProfileDialog` (item rows, live YAML preview via ported `backupProfilePreview`), `DeleteBackupProfileDialog`, `ImportProfileDialog` (reads the YAML file as text), export download, master list selection, filter reset |
| Restore | `restore-server`, `create-server-from-backup` | `RestoreDialog` (two-step: the `409 restore_confirmation_required` response swaps the dialog into the "Replace existing world data?" confirmation), `CreateServerFromBackupDialog` |
| Dialog chrome | `close-modal` | `Dialog` close, disabled while the mutation is pending |

### 6.3 The 23 modal submit flows (`submitModal`)

`add-reboot`, `edit-reboot`, `delete-reboot`, `edit-settings`, `delete`, `run-backup`, `run-scheduled-backup`, `backup-details`, `new-backup-profile`, `edit-backup-profile`, `delete-backup-profile`, `import-backup-profile`, `add-backup-schedule`, `edit-backup-schedule`, `delete-backup-schedule`, `restore-server`, `create-server-from-backup`, `add-integration`, `edit-integration`, `add-user`, `edit-user`, `delete-user`, `add-server`. The `default:` branch handles `restart`, `stop`, `start`, `update`. Each becomes one dialog component with its own `useMutation`, its own pending/error state and a unit or component test.

Behaviours to port deliberately (all in the legacy handler, easy to lose):

- Dialog opens only when `can(action)`; heading is the accessible name; first focus differs per dialog (token/name field, safest button, error summary).
- On success: close, toast the exact legacy message, refetch, and for `edit-settings` return focus to the opener.
- On failure: inline `modal-error` (focused), except integrations and reboots which render a **field-error summary** with links that focus the control. Reboot errors are attributed to fields by substring match in Go (`reboots.go:577-597`), so contract-test real payloads before wiring field mapping ([api-types.md open question 19](./api-types.md)).
- `create` navigates to Maintenance and starts a deploy watch; `delete` refetches even on failure (the receipt may have changed); `edit-settings` with no diff closes with "No settings changed. No rollout requested." and sends nothing.
- `edit-settings` requires the world-name acknowledgement when the world name changes; passwords are write-only with explicit clear checkboxes; admin IDs are merged from saved IDs and manual 32-hex fields and de-duplicated.
- `add-server` posts JSON, or multipart (`request` + `save`) when a `.sav` is attached. Client rules: `.sav` extension, no leading `.` or `-`, no path separators or control characters, non-empty, at most 32 MiB.
- `restore-server` always sends multipart (`request` + optional `save`), requires `status === 'restored'` in the response, and runs the two-step destructive confirmation.

### 6.4 Global listeners, imperative DOM and derived behaviour

| Legacy | New |
| --- | --- |
| `#refresh` click, `#pause` click, `#updated-at`, `#connection-status` | `ConnectionIndicator` + a refresh button calling `queryClient.invalidateQueries` |
| `document` `click` delegation (`handleAction`) and the integration-error-link handler | Per-element `onClick`. The error summary renders real links with `onClick` that focus the field |
| `document` `input`: `maxPlayers` recalculates locked memory/CPU; `ownerId` clears the saved-ID select; `admin-token` clears `aria-invalid`; `event-search` 300 ms debounce; `log-search` live filter; `backup-profile-search`; profile item rows refresh the YAML preview | Controlled inputs and derived values in the owning component |
| `document` `change` (provider and quiet-hours toggles, delete-mode, saved-user, telemetry-range, event-range, reboot-mode, backup run/schedule previews, schedule-mode, restoreSource, restore select, display-timezone, profile type, server-filter) | Controlled `onChange` handlers; previews are `useMemo` over form state, not DOM surgery |
| `#modal-form` `invalid` capture (opens the `<details>` that contains an invalid control) | A `useRevealOnInvalid` hook on the Advanced disclosure of the create form |
| `hashchange` | Router |
| `visibilitychange` (re-render roster expiry, refetch) | `refetchOnWindowFocus` + `useRosterFreshness` |
| `setInterval(refresh, 10000)` with guards | `usePollingInterval()` |
| `#login-dialog` / `#modal` `cancel` (Esc) prevented while busy | `Dialog` `onCancel` handler |
| `render()` focus restoration by `data-testid` + selection range after each repaint | Not needed: React preserves DOM and focus. Keep one test that types in the event search while polling refetches |
| `details[data-chart][open]` state preserved across repaints | Not needed (React keeps `open`) |
| `clearProtectedState()` | `AuthProvider` identity change: new `QueryClient`, dialogs unmounted, toasts cleared |

### 6.5 Pure logic to extract and unit-test first

These are the high-value, low-risk ports. Each keeps its name and gets the assertions from `web/app.test.cjs` (129 assertion blocks, 72 KB, today run through `vm`) moved into Vitest.

`number`, `bytes`, `duration`, `date`, `zonedDate`, `timezoneOptions`, `validTimezone`; `parseLocationHash` (to `legacy-hash.ts`), `scopedHash`; `can` aliases; `serverStatus`, `waitingForFirstObservation`, `serverLabel`, `worldLabel`, `metricValue`, `metricText` (replaced by `metricCell()`), `dashboardModel`; `acceptTelemetry` roster-freshness maths; `chart()` series and gap logic; `eventTable` helpers; `deliveryTime`, `discordConnectionStatus`, `latestAttemptedDeliveryFor`; reboot request builder and `rebootTimingLabel`; backup helpers (`latestBackupRuns`, `backupProfilePreview`, `backupProfilePath`, `backupSourceText`); `parseAdminIds`, `normalizePlayerIdClient`, `adminIdsFromForm`, `createSettingsFromForm`, `editSettingsPatch`, `createRequestBody` (save validation); `exportCSV` with the spreadsheet-formula guard (`'` prefix for cells starting `= + - @ tab CR`); `beginDeployWatch`, `deployProgress`, `rememberCreatedServer`.

### 6.6 API client: every endpoint

Typed with the drafts in [api-types.md](./api-types.md) (which lists roughly 50 routes with request and response types, status codes and `file:line`). The client is one `request()` function plus thin per-domain modules. Behaviours it must encode, all from the Go side:

- Responses carry `Cache-Control: no-store`; `204` has an empty body (never call `.json()`).
- Error envelope is `{error, code?, fields?}`. `code` appears once (`restore_confirmation_required`, 409); `fields` only on 400s from integrations and reboots.
- `DELETE /api/servers/:id` **requires a JSON body** (`confirm`, optional `mode`, `purgeConfirm`); other DELETEs take none.
- Request decoders use `DisallowUnknownFields`: the client must not send extra keys.
- `Server` has two wire shapes (`metrics` map with no scalars, or `metrics: null` with legacy scalars). Read `metrics` only; ignore scalars.
- The legacy code reads fields Go never emits (`bootstrap.clusterName`, telemetry `points`/`current`/`latest`, `result.logs`, `result.server.updateAvailable`). Do **not** port those fallbacks ([open question 9](./api-types.md)).

| Endpoint (from ARCHITECTURE.md and the code) | Client function | Hook | Used by |
| --- | --- | --- | --- |
| `GET /api/auth` | `auth.status()` | `useAuthStatus` | AuthProvider |
| `POST /api/session` | `auth.session(token)` | `useSignInWithToken` | LoginScreen |
| `GET /api/auth/login` | (navigation) | link | LoginScreen |
| `GET /api/auth/callback` | (server redirect) | none | n/a |
| `POST /api/auth/logout` | `auth.logout(csrf)` | `useLogout` | top bar |
| `GET /api/bootstrap` | `servers.bootstrap()` | `useBootstrap` | everything |
| `GET /api/servers/:id/logs?tail=100` | `servers.logs(id, tail)` | `useServerLogs` | Telemetry |
| `GET /api/servers/:id/telemetry?range=` | `servers.telemetry(id, range)` | `useTelemetry` | Telemetry, Overview |
| `GET /api/servers/:id/deletion` | `servers.deletion(id)` | (reserved; receipts come from bootstrap) | n/a |
| `POST /api/servers` (JSON or multipart) | `servers.create(...)` | `useCreateServer` | CreateServerDialog |
| `DELETE /api/servers/:id` (JSON body) | `servers.remove(id, body)` | `useDeleteServer` | DeleteServerDialog |
| `POST /api/servers/:id/restore` (multipart) | `servers.restore(id, form)` | `useRestoreServer` | RestoreDialog |
| `POST .../actions/restart`, `stop`, `start` | `servers.action(id, name)` | `useServerAction` | Confirm dialog, dashboard rows |
| `POST .../actions/update` | `servers.update(id, tag)` | `useUpdateImage` | Update dialog |
| `POST .../actions/check-update` | `servers.checkUpdate(id)` | `useCheckUpdate` | Maintenance |
| `POST .../actions/edit-settings` | `servers.editSettings(id, patch)` | `useEditSettings` | EditSettingsDialog |
| `GET /api/events` (`query, category, serverId, since, limit, offset`) | `events.list(params)` | `useEvents` (infinite) | Events, Dashboard, Maintenance |
| `GET /api/reboots` | `reboots.list()` | `useReboots` | Reboots, Dashboard, Overview |
| `POST /api/reboots`, `PUT/DELETE /api/reboots/:id`, `POST /api/reboots/preview` | `reboots.create/update/remove/preview` | `useSaveReboot`, `useDeleteReboot`, `usePreviewReboot` | RebootDialog |
| `GET/POST /api/users`, `PUT/DELETE /api/users/:id` | `users.*` | `useUsers`, `useSaveUser`, `useDeleteUser` | Saved IDs, create/edit server |
| `GET/POST /api/integrations`, `PUT /api/integrations/:id`, `POST /api/integrations/:id/test` | `integrations.*` | `useIntegrations`, `useSaveIntegration`, `useTestIntegration` | Integrations |
| `GET /api/backups`, `/profiles`, `/runs`, `/schedules` (same payload) | `backups.overview()` | `useBackups` | Backups |
| `POST /api/backups` or `/runs`; `GET /api/backups/runs/:id`; `GET .../bundle` | `backups.run`, `backups.run(id)`, `backups.bundle(id)` | `useRunBackup`, download helper | Backups |
| `POST /api/backups/profiles`, `PUT/DELETE .../profiles/:id`, `POST .../profiles/import`, `GET .../profiles/:id/export` | `backups.profiles.*` | `useSaveProfile`, `useDeleteProfile`, `useImportProfile`, export helper | Profiles |
| `POST /api/backups/schedules`, `PUT/DELETE .../schedules/:id`, `POST .../schedules/:id/run` | `backups.schedules.*` | `useSaveSchedule`, `useDeleteSchedule`, `useRunSchedule` | Schedules |
| `POST /api/backups/:runId/create-server` | `backups.createServer(id, body)` | `useCreateServerFromBackup` | Dialog |
| `GET /api/image-tags` | `images.tags()` | `useImageTags` | Create, Update dialogs |

`ARCHITECTURE.md`'s API list is incomplete (it omits users, integrations, backups, restore, delete, image-tags and edit-settings). Phase 1 updates it from this table.

## 7. Test strategy

| Layer | Tool | What it covers | Runs |
| --- | --- | --- | --- |
| Static | `tsc -b`, ESLint 10 (type-aware), stylelint, Prettier, `react-doctor --staged` | Types, hooks rules, design guards, formatting, React smells | prek pre-commit; CI |
| Unit (`environment: 'node'`) | Vitest 5 | Everything in `lib/`, `api/schemas`, `auth/`: the ported pure logic (about 60% of all tests) | pre-push; CI |
| Component (Vitest **browser mode**, Chromium) | `@vitest/browser-playwright`, `vitest-browser-react` | Dialogs, forms, tables, focus return, `reportValidity`, native `<dialog>`, keyboard flows, each metric state | CI |
| Contract | Playwright spec against the real Go binary | Every `GET` endpoint response parses with the Zod schemas; `z.strictObject` copies flag *new* fields as warnings and missing or retyped fields as failures | CI |
| E2E | `@playwright/test` against the **real binary in demo mode** (token mode) and the Go OIDC fixture | Journeys per page (below) | CI |
| Visual | Playwright `toHaveScreenshot`, Linux CI only | Canonical states: five statuses, each metric state, empty, loading, login | CI, baselines in `web/e2e/__screenshots__` |
| Accessibility | `@axe-core/playwright` on every route; keyboard-only and reduced-motion specs | WCAG 2.2 AA rules below | CI |
| Performance | `check-dist.mjs` (bytes, hashes) and a login budget spec (headers, transfer, request count) | The budgets in the [checklist](#10-acceptance-checklist). Lab timings (Lighthouse) are report-only because they flake | build; CI |

**Component-test seam.** `createApiClient({ fetch })` takes the fetch implementation as an argument. `src/test/fake-api.ts` builds a `fetch` from a typed route table (`GET /api/bootstrap` returns `BootstrapAdmin`, and so on), records calls, and can delay or fail individual routes. Fixtures are typed against `api/types`, so an API change breaks fixtures at compile time. This avoids `msw` (3.0.3, Node `>=22.12`) and its service worker inside browser mode; it can be added later without changing tests.

**E2E harness.** A worker-scoped `server` fixture spawns `.tmp/rsdw-c2` (built by `go build`) with `RSDW_DEMO_DATA=true`, a known `RSDW_ADMIN_TOKEN`, a private `RSDW_STATE_FILE`, `RSDW_LISTEN_ADDR=127.0.0.1:0`, parses the `listening on` line for the port (this is `fixture-processes.cjs` rewritten), and kills it on teardown. One server per worker, so specs that mutate state (stop, delete, backups) run in parallel without sharing it. Fake clock with `page.clock.install`, as the dashboard script does today.

**Selector policy.** Tests use `data-testid` and ARIA roles only, never CSS classes. Today 46 selector uses are `.status` and a handful are `.console-row`, `.stat`, `.panel`. Phase 1's first commit adds `data-testid`s to the legacy markup wherever a test needs one and rewrites the existing scripts to use them, so **the same specs run against `/legacy/` and `/`** (`RSDW_UI_BASE` selects). That is the parity guarantee: a spec that passes on both proves the port, and failing on `/` only is a regression.

**`data-testid` contract.** `web/app.js` and `index.html` define 150 literal test IDs plus 14 dynamic prefixes (`filter-`, `chart-data-`, `category-`, `backup-details-`, `download-backup-`, `run-schedule-`, `storage-card-`, `nav-`, `edit-`, and the `backup-item-name|kind|requirement`, `backup-running-path`, `backup-stopped-path` families). Keep them all. Intentional changes: `dashboard-view` removed with the toggle; `close-login`, `cancel-login` and the login dialog go away if D4 (inline token field) is accepted, `open-login` stays as the token-mode submit or the OIDC link. Everything else keeps its name and gains `data-server-id` where rows are addressed.

### Porting the existing tests

| Legacy file | Lines | What it covers today | New home | How |
| --- | --- | --- | --- | --- |
| `tests/dashboard-browser.cjs` | 105 | Route-mocked, fake clock, 4 servers, row actions, viewer, no overflow at 5 widths, 2 screenshots | `e2e/specs/dashboard.spec.ts` | Keep route-mocking with typed fixtures; add the five statuses and metric states; screenshots become visual assertions |
| `tests/events-browser.cjs` | 89 | Real binary, search, category, ranges, desktop/mobile | `events.spec.ts` | Real binary |
| `tests/users-browser.cjs` | 312 | Saved IDs CRUD, `.sav` type/size errors, multipart rejection in demo, empty-world create, saved-ID selection | `saved-ids.spec.ts`, `create-server.spec.ts` | Split in two |
| `tests/settings-browser.cjs` | 225 | Edit-settings prepopulation, scalar and access edits, repeated admin IDs, password clear | `edit-settings.spec.ts` + component tests for `settings-patch` | |
| `tests/stop-browser.cjs` | 124 | Stop/start, parked actions, demo start, viewer denial, overflow | `lifecycle.spec.ts` | Real binary |
| `tests/deletion-browser.cjs` | 184 | World labels, keep/purge, confirmation, persisted receipts | `deletion.spec.ts` | Real binary; the "receipt after restart" step needs the harness to restart a server on the same state file |
| `tests/integrations-browser.cjs` | 198 | Dialog focus and headings, checkbox layout, keyboard, narrow/short viewports | `integrations.spec.ts` + component tests | |
| `tests/player-roster-browser.cjs` | 91 | Roster states, overview navigation, keyboard, escaping, persistent route error, paused expiry | `server-overview.spec.ts` + unit `roster.test.ts` | Route-mocked |
| `tests/backups-browser.cjs` | 328 | Overview, profiles, multi-item authoring, YAML import/export, storage, schedules, Run now, bundle download, delete | `backups.spec.ts` | Real binary |
| `tests/backups-ui-contract.cjs` | 254 | Route-mocked forms, revisions, restore confirmation, manifest details, 375 px layout | component tests for forms; `backups-restore.spec.ts` for the two-step confirmation | |
| `tests/oidc-browser.cjs` | 132 | Spawns `go test -run TestOIDCBrowserFixture`: HTTPS login, delayed callback after logout, route rejection, browser isolation | `oidc.spec.ts` | **Keep the Go fixture unchanged**; it embeds the UI, so it needs `web/dist` built first |
| `tests/fixture-processes.cjs` | 39 | Child-process helper | the `server` fixture | Rewritten |
| `web/app.test.cjs` | 72 KB, 129 assertion blocks | `vm`-loaded `app.js` internals | `lib/*.test.ts` and component tests | Port assertion-by-assertion alongside each function; delete with legacy in Phase 6 |
| `verification/save-import.test.cjs` | 92 | Helm/RBAC/fixture importer | **unchanged** | Not UI |
| `verification/tick-preview.cjs` | 39 | Serves recorded telemetry at `:18085` for manual review | A Vite dev middleware that serves the same recorded JSON | Phase 2 |

**Differential check while both UIs exist.** `legacy-inventory.cjs` re-extracts actions, test IDs and capabilities from `web/legacy/app.js`; a CI step diffs that against a checked-in manifest listing, per item, the new component that replaced it or an explicit "removed (reason)". The migration is not complete until the diff is empty.

### Accessibility rules (WCAG 2.2 AA)

Target [WCAG 2.2](https://www.w3.org/TR/WCAG22/) level AA. Concretely:

- Text contrast 4.5:1 (7:1 for body text by the token design); non-text UI and status glyphs 3:1 (all computed in [design-brief.md](./design-brief.md#41-colour)).
- **Colour is never the only carrier** (SC 1.4.1): every status has a silhouette and a word.
- Focus visible, never obscured by sticky bars (SC 2.4.7, 2.4.11); `scroll-padding-top` equals the sticky header.
- Target size at least 24 px (SC 2.5.8); the design uses 40-44 px.
- Keyboard: every action reachable and operable; native `<dialog>` traps focus and returns it to the opener; Esc closes unless busy; scrollable regions (tables, logs, JSON) are focusable and named.
- Status messages (SC 4.1.3): toasts and the connection indicator use `role="status"`; blocking errors use `role="alert"` and receive focus; form errors are inline, linked, and summarised.
- Forms: visible labels, programmatic descriptions, `aria-invalid`, errors that say what to do (SC 3.3.1, 3.3.3).
- Charts: `role="img"` with a text summary, plus a real data table.
- `prefers-reduced-motion` removes the only animation (the Starting ring). The e2e suite runs once with reduced motion emulated.
- Skip link retained; page title updates per route (`<Page> · RSDW C2`); one `<h1>` per page.
- `@axe-core/playwright` runs on every route in the signed-in and signed-out states; any **serious or critical** violation fails CI. Manual screen-reader pass (NVDA + Chrome, VoiceOver + Safari) on login, dashboard, one dialog, one form with errors, before the Phase 6 cutover.

---

## 8. Go embedding and serving

### Today

`//go:embed web` embeds the directory; `newWebHandler(root)` (`main.go:37-59`) reads `index.html`, rewrites `"/app.js"` and `"/styles.css"` to `"/app.js?v=<sha256>"`-style URLs (issue #23, `web_test.go`), serves `/` and `/index.html` with `Cache-Control: no-store`, and hands everything else to `http.FileServer`. Measured consequences: no `Cache-Control`, `ETag`, `Last-Modified` or compression on any static file ([login-performance.md](./login-performance.md#what-the-user-sees)). `ServeHTTP` already sets `Referrer-Policy: no-referrer` and `X-Content-Type-Options: nosniff` on every response.

### Target

```go
//go:embed all:web/dist
var distFiles embed.FS            // new console (hashed Vite output + precompressed siblings)

//go:embed all:web/legacy
var legacyFiles embed.FS          // frozen old console, deleted in Phase 6
```

| Path | `Cache-Control` | Notes |
| --- | --- | --- |
| `/`, `/index.html` | `no-cache` | strong `ETag`, 304 on `If-None-Match`. The shell holds no secret. Replaces `no-store` (this changes a `web_test.go` assertion on purpose) |
| `/assets/*` | `public, max-age=31536000, immutable` | Vite emits every file here with a content hash; `check-dist.mjs` fails the build otherwise. Includes JS, CSS, fonts and hero images |
| anything else in `dist` (favicon, robots) | `no-cache` | `ETag` |
| `/legacy/*` (Phases 1-5) | as today, plus `Cache-Control` for the `?v=<hash>` URLs | Old handler kept, mounted under a prefix |
| `/api/*` | `no-store` | unchanged |

Hash routing means the server never needs an SPA fallback: only `/` (and `/?signin=failed`) reach it. Unknown paths stay `404`, as now.

Illustrative handler (the real change comes with tests):

```go
type asset struct {
	body, br, gz []byte
	contentType  string
	etag         string
	immutable    bool
}

func newWebHandler(root fs.FS) http.Handler {
	assets, err := loadAssets(root)           // walks root, pairs name with name.br / name.gz, sha256 for ETag
	index := assets["index.html"]
	if err != nil || index == nil {           // no Node build (fresh clone): compile and test still work
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "The web console is not built. Run: npm --prefix web ci && npm --prefix web run build",
				http.StatusServiceUnavailable)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		a := assets[name]
		if a == nil || a.body == nil {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", a.contentType)
		h.Add("Vary", "Accept-Encoding")
		if a.immutable {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		body, etag := a.body, a.etag
		switch enc := r.Header.Get("Accept-Encoding"); {
		case a.br != nil && acceptsEncoding(enc, "br"):
			h.Set("Content-Encoding", "br")
			body, etag = a.br, a.etag[:len(a.etag)-1]+`-br"`        // a strong ETag must differ per representation
		case a.gz != nil && acceptsEncoding(enc, "gzip"):
			h.Set("Content-Encoding", "gzip")
			body, etag = a.gz, a.etag[:len(a.etag)-1]+`-gz"`
		}
		h.Set("ETag", etag)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body)) // handles HEAD and If-None-Match
	})
}
```

Details that matter:

- **Content types:** set explicitly for `.avif`, `.webp`, `.woff2`, `.svg`, `.mjs`; do not rely on the host's MIME database.
- **Fresh clone without Node:** `web/dist/.gitkeep` is committed so `//go:embed all:web/dist` compiles. `TestEmbeddedIndex*` calls `t.Skip("web/dist not built")` when `index.html` is absent. CI always builds first. This also removes today's `panic(err)` in `newWebHandler` when `index.html` is missing.
- **Precompression** happens in Node at build time (`scripts/precompress.mjs`, `node:zlib`, brotli quality 11 and gzip level 9) for `.js .css .html .svg .json .txt`. Go only reads bytes. No new Go dependency. The legacy `?v=` rewrite keeps running on the legacy handler, where Phase 0 adds runtime gzip with the standard library (measured: `app.js` 228,031 B to 56,204 B gzip, 45,551 B brotli).
- **Binary size:** removing the two PNGs takes 20.8 MB out of the binary and image; dist plus compressed siblings adds on the order of a megabyte (unmeasured).
- **Rollback switch:** `RSDW_LEGACY_UI=true` mounts the legacy console at `/` for one release after cutover of each phase. Remove it in Phase 6.
- **CSP (optional, Phase 6):** once the shell has no inline script, send `Content-Security-Policy: default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'`. React sets styles through the CSSOM, which `style-src 'self'` allows; the data-URI placeholder needs `img-src data:`. Worth doing because token mode stores the admin token in `sessionStorage`.
- **Login hero hint (optional):** in OIDC mode the handler can add a `Link: <hero>; rel=preload; as=image; fetchpriority=high` header when no valid session cookie is present ([login-performance.md](./login-performance.md#4-stop-painting-the-login-scene-before-auth-is-known-f7-f8)).

### Go tests to write or change (`web_test.go`)

1. `/` and `/index.html`: `200`, `no-cache`, an `ETag`; a repeat with `If-None-Match` returns `304`; `HEAD` has no body.
2. `/assets/<hashed>`: `immutable`; body equals the embedded bytes; correct `Content-Type` for `.js .css .woff2 .avif .webp .svg`.
3. A file in `assets/` whose name has no hash fails `check-dist` at build time (Node test), not at runtime.
4. `Accept-Encoding: br, gzip` picks br; `gzip` only picks gzip; neither serves identity; ETags differ per encoding; `Vary` set.
5. Unknown path and missing asset: `404`. `POST /`: `405` with `Allow`.
6. Missing `index.html` (empty `MapFS`): `503` and no panic.
7. Legacy mount: the old `TestAssetURLsChangeWithContent` and `TestEmbeddedIndexVersionsAssets` logic, pointed at `/legacy/` with the prefix applied.

### Dockerfile (Node stage first)

```dockerfile
# syntax=docker/dockerfile:1
FROM node:24.22.0-bookworm-slim@sha256:<pin at implementation> AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ ./
RUN npm run build                      # tsc -b && vite build && precompress && check-dist

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY *.go ./
COPY --from=web /src/web/dist ./web/dist
COPY web/legacy ./web/legacy           # until Phase 6
COPY charts/rsdw-c2/values.yaml ./charts/rsdw-c2/values.yaml
COPY verification/tick-live-observations.json ./verification/tick-live-observations.json
RUN CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/rsdw-c2 .
# runtime stage unchanged
```

The build stage is Debian (glibc) because Rolldown, Oxc and lightningcss ship native binaries; only `dist/` crosses into the Go stage, so the final image stays Alpine. Pin the Node image by digest like the workflow actions are pinned by SHA. `.dockerignore` adds `**/node_modules`, `web/dist`, `web/coverage`, `web/test-results` (the current `node_modules` entry only matches the context root).

### CI and `scripts/verify.sh`

- Bump `actions/setup-node` from `24.10.0` to `24.22.0` in all three jobs; root `engines` becomes `>=24.15.0`.
- `verify` job: `npm ci --ignore-scripts` at the root **and** `npm --prefix web ci --ignore-scripts`; cache both lockfiles (`cache: npm`, `cache-dependency-path` with both); `pip install prek==0.5.5` then `prek run --all-files` and `prek run --all-files --stage pre-push`; install Chromium for Playwright as the workflow already does (one install serves the root `playwright` and `@playwright/test` because the versions match).
- **Keep the step `run: bash scripts/verify.sh` exactly as written.** `.github/release.test.mjs` asserts a step with that literal command, so the new work goes **inside** the script.
- `scripts/verify.sh` order becomes: `npm --prefix web ci`, `npm --prefix web run build` (must precede every Go command because `go test` embeds `web/dist`), then the existing `go test ./...`, `go vet ./...` and chart scripts, then `npm --prefix web run check`, then the Playwright suites. The legacy `node --check web/app.js`, `node --test web/app.test.cjs` and the "every rendered `data-action` has a `case`" guard stay until Phase 6 (they target `web/legacy/app.js` after the move), and are replaced by `switch-exhaustiveness-check` and the manifest diff above.
- `image` job: unchanged, except the Dockerfile now does the Node build. `.github/publish.sh` calls `docker buildx build` on the same Dockerfile, so release images get the new UI with no script change.
- Dependency updates: add a Renovate or Dependabot config for `web/` that groups `eslint`/`typescript-eslint`/`react-hooks`, `vite`/`@vitejs/plugin-react`, and `vitest`/`@vitest/*`, and pins Playwright with the root package.

---

## 9. Phased delivery

Each phase ends with a working, shippable console, all CI gates green, and a rollback. Sizes are relative effort (S/M/L), not calendar estimates.

### Phase 0: delivery fixes on the current console

Size S. Independent of React. Fixes what you reported first.

- Replace the 20.8 MB of PNGs with the responsive AVIF/WebP set and an inline placeholder; swap the login scene's CSS background for `<picture>` with `fetchpriority`; hashed file names.
- Go handler: `no-cache` + `ETag` on `/`; `immutable` on `?v=<hash>` assets and hashed images; runtime gzip with the standard library; update `web_test.go`.
- Boot shell: do not mount the login scene until `/api/auth` says signed out; remove the two infinite animations.
- Add `tests/login-budget.cjs` (headers, transfer, no hero request when signed in).

Exit gates: [login numbers](./login-performance.md) re-measured and recorded in the PR; cold login transfer at most 300 KB; warm at most 10 KB; every existing script passes. Rollback: revert; no data or API change.

### Phase 1: scaffold, shell, login, dashboard

Size L. Starts with the spike that this document could not finish.

0. **Spike (first commit, discard-able):** install the pinned stack under Node 24.22, run `tsc -b`, ESLint, Prettier, stylelint, `react-doctor`, `vite build`, `prek run --all-files` on a hello-world with all runtime deps; record the real framework-floor bundle sizes; adjust the budgets below with the numbers.
1. Freeze the old UI: `git mv` `app.js`, `styles.css`, `index.html`, `assets/`, `app.test.cjs` to `web/legacy/`; rewrite its absolute asset URLs to `/legacy/...`; mount it under `/legacy/`; add `RSDW_LEGACY_UI`. Add `data-testid`s for class-only selectors and parameterise the existing scripts with `RSDW_UI_BASE`.
2. Node bump (CI, Docker, `engines`, README), Dockerfile Node stage, `verify.sh` ordering, prek config and `scripts/web-hook.mjs`, Renovate config.
3. Go: new `newWebHandler` (headers, precompressed siblings, stub when unbuilt) and tests.
4. Design tokens, fonts, `StatusPill`, `MetricCell`, `DataTable`, `Button`, `Field`, `Dialog`, `Toast`, `Nav`, `ConnectionIndicator`, `Icon`, `EmptyState`; MASTER.md regenerated from tokens.
5. API client, Zod schemas for `auth` and `bootstrap`, `AuthProvider` with every behaviour in [section 5](#5-auth-csrf-and-session-parity), `LoginScreen` (token inline + OIDC link), boot shell, hash redirect shim.
6. App shell, router, capability gating, polling policy, error model.
7. Dashboard: fleet status line, server table, filters, restart/stop/start confirm dialogs, recent activity, empty/loading states. Nav entries for unported pages link to `/legacy/#<page>`.

Exit gates: dashboard, lifecycle (stop/start/restart) and OIDC e2e specs pass against `/`; login budget spec passes (at most 450 KB cold, 12 KB warm, proposals to be ratified by the spike); axe clean on login and dashboard; `react-doctor` no errors; `prek run --all-files` clean; Docker image builds; the parity manifest lists the dashboard and login items as replaced. Rollback: `RSDW_LEGACY_UI=true`.

### Phase 2: read-heavy pages

Size M. Server overview (roster states and freshness logic), Telemetry (SVG charts, logs, metric sources disclosure, CSV export), Events (infinite list, detail pane, CSV export). Ports `events-browser`, `player-roster-browser`, and the telemetry/event unit assertions. Replaces `verification/tick-preview.cjs`. Exit: those specs pass on `/`; roster expiry test passes with the paused clock; charts match the data table.

### Phase 3: lifecycle forms and Saved IDs

Size L. Maintenance page, create server (multipart, resource lock, saved-ID select, admin IDs, advanced disclosure), edit settings, update image, delete with receipts and retry, deploy watch, Saved IDs. Ports `users-browser`, `settings-browser`, `stop-browser` (remainder), `deletion-browser`. Exit: those specs pass; the deploy-watch state machine is unit-tested (ready, failed, timed out at 10 min, starting).

### Phase 4: Reboots and Integrations

Size M. Reboot schedules table and dialog (3 timing modes, preview, history, display timezone), integrations table/dialog (provider fieldsets, quiet hours, field-error summary) and deliveries. Ports `integrations-browser`. Exit: real-payload contract tests for reboot and integration validation errors before the field mapping ships.

### Phase 5: Backups

Size L (the largest feature: profiles with live YAML preview, schedules, run history, manifest details, bundle download, restore with two-step confirmation, create-from-backup, YAML import/export, storage). Ports `backups-browser` and `backups-ui-contract`. Exit: both specs pass; restore's 409 confirmation flow is covered; blob downloads verified in e2e.

### Phase 6: cutover and cleanup

Size S. Delete `web/legacy/`, `app.test.cjs`, the legacy checks in `verify.sh`, `RSDW_LEGACY_UI` and the `/legacy/` mount; update `README.md`, `ARCHITECTURE.md` (full API list), `docs/screenshots/`; decide D3 (light theme); optional CSP; manual screen-reader pass; final Lighthouse and budget audit. Exit: parity manifest diff is empty; all budgets and a11y gates green; `git grep` finds no `legacy` references.

---

## 10. Acceptance checklist

### Feature parity (every item has an e2e or component test before its phase closes)

- [ ] **Dashboard.** Server list with world label (disambiguated with ID when two share a label), endpoint (admin only), Attention and Update tags, players as `n / max` with the "known players only" note when some are unknown, TPS, memory/CPU, uptime, next reboot (restricted, unavailable, not scheduled), actions by state (running: Restart and Stop; stopped: Start), deploying marker; filters; empty state (admin: create button; viewer: sentence); Add server; recent activity; viewer sees only what `capabilities` allows.
- [ ] **Server overview.** Status, engine readiness line, links to Telemetry/Maintenance/Events/Reboots scoped by `serverId`, key numbers, connected-players roster in every state (fresh, stale, error, names unavailable, none, count without names), next reboot in the display zone, join endpoint with copy, actions gated by capability and hidden while a deletion is pending.
- [ ] **Telemetry.** Server picker, range 60 s / 5 m / 1 h, CSV export (formula guard), charts for tick rate, players, network in/out, CPU, memory, p95/p99 tick duration with per-chart data tables, resource list, health checks, tick-duration summary, metric sources, metric definitions, logs (search, refresh, download; absent for stopped servers).
- [ ] **Events.** Debounced search, 24 h / all range, category filter, counts, selection with JSON and copy, load older (de-duplicated), CSV export of up to 2,500 rows.
- [ ] **Maintenance.** Lifecycle details, readiness, recent changes, audit trail, deploy-progress panel, deletion-in-progress messaging, receipts, actions by state (running: restart, update image, check update, stop, edit settings, restore, delete; stopped: start, restore).
- [ ] **Create / edit / delete server.** All create fields and defaults; resource lock formula; saved-ID select; additional admin IDs (32-hex, de-duplicated); `.sav` rules (extension, name, non-empty, at most 32 MiB); password write-only with clear; world-name acknowledgement; no-op edit sends nothing; delete keep/purge with type-to-confirm, retry from receipt.
- [ ] **Saved IDs.** CRUD, 32-hex validation, duplicate error, empty state.
- [ ] **Integrations.** Provider status logic (connected / disconnected / not connected from deliveries), Discord and webhook views, add/edit with secret reference, quiet hours, server and rule checklists, field-error summary, send test, recent deliveries with all statuses, pending-restart note, demo note.
- [ ] **Reboots.** Availability and demo notices, display-timezone preference, schedules table, add/edit in cron/interval/daily with acknowledgement and warning minutes, delete, preview next five runs, history, scoping by `serverId`.
- [ ] **Backups.** Overview and health, history with server filter and attention filter, details with manifest, download, retry, schedules (add/edit/delete/run now/extra run), profiles (search, type filter, detail, new/edit/delete, import/export YAML), storage, run-now dialog with resolved-source preview, restore (backup and custom `.sav`, empty-target consent, destructive confirmation), create server from backup.
- [ ] **Login and session.** All 16 rows of [section 5](#5-auth-csrf-and-session-parity).
- [ ] **Cross-cutting.** 5 s toasts with the legacy messages; CSV export with formula guard; pause and refresh; 10 s polling with the dialog and input guards; document title; skip link; `<noscript>` message; demo-mode chip; viewer role; all hash URLs from the legacy console still resolve.

### Performance budgets

| Budget | Phase 0 | From Phase 1 | Enforced by |
| --- | --- | --- | --- |
| Cold login, total compressed transfer (HTML, JS, CSS, fonts, hero) | at most 300 KB | at most 450 KB (proposal; the spike sets the final value) | login budget spec |
| Warm login transfer | at most 10 KB | at most 12 KB | login budget spec |
| Any single image | at most 250 KB | at most 250 KB | `check-dist.mjs` |
| Hero requested by a signed-in user | none | none | login budget spec |
| Every `/assets/*` response | immutable + hashed | immutable + hashed | Go test + budget spec |
| Initial JS (entry chunk, brotli) | n/a | at most 150 KB (proposal) | `check-dist.mjs` |
| Route chunks (lazy: telemetry, backups, forms) | n/a | at most 60 KB each (proposal) | `check-dist.mjs` |
| Fonts on the login path | n/a | two files, at most 50 KB (measured: 45 KB) | `check-dist.mjs` |
| Lab: button painted, 9 Mbps/150 ms RTT, 4x CPU | at most 1.0 s | at most 1.5 s | Lighthouse, report-only |
| Core Web Vitals (lab and, if collected, field) | n/a | LCP at most 2.5 s, INP at most 200 ms, CLS at most 0.1 ([web.dev](https://web.dev/articles/vitals)) | Lighthouse, report-only |

Phase 1's higher byte budget is honest: React and fonts raise the floor above the legacy bundle (about 245 KB cold for the Phase 0 console by my arithmetic: 56 KB gzip JS + 9 KB CSS + 2 KB HTML + 175 KB hero). It is paid for by removing the 10.6 MB image, and the numbers are **proposals until the Phase 1 spike measures the framework floor**.

### Quality gates

- [ ] `prek run --all-files` clean (typecheck, ESLint with zero warnings, stylelint, Prettier, `react-doctor --staged`).
- [ ] `react-doctor` reports no errors in CI (`--blocking error`); warnings triaged in the PR, not ignored in config.
- [ ] Unit and component tests green; coverage at least 90% lines on `lib/`, at least 80% overall (set after Phase 1 measurement).
- [ ] E2E green on Chromium for every ported spec on both `/legacy/` (while it exists) and `/`.
- [ ] axe: zero serious or critical violations on every route, signed-in and signed-out.
- [ ] Parity manifest diff empty at Phase 6.
- [ ] No `dangerouslySetInnerHTML`, no raw hex outside `tokens.css`, no gradient outside the allow-list, no metric cell string matching the banned error words (lint-enforced).
- [ ] `ARCHITECTURE.md` lists every endpoint in the [client table](#66-api-client-every-endpoint).

---

## 11. Risks and open items

| # | Risk or open item | Mitigation |
| --- | --- | --- |
| R1 | The pinned stack has not been installed and linted end to end (disk filled during my spike) | Phase 1, step 0 is the spike; budgets are proposals until then. The executed checks (react-doctor, prek, TS drafts) and registry peer ranges lower the odds of surprises |
| R2 | TypeScript 7 is current but unusable with `typescript-eslint` | Pin 6.0.3; configs already valid on 7.0; revisit at 7.1 |
| R3 | Dual UI means two sets of tests and a `/legacy/` path for up to five phases | Strangler order puts the most-used pages first; the manifest diff and the parameterised specs make "done" mechanical; `RSDW_LEGACY_UI` gives a one-flag rollback |
| R4 | Hash URL format changes (`#a` to `#/a`) | `legacy-hash.ts` rewrites on boot, tested against every `parseLocationHash` case |
| R5 | Node bump touches CI, Docker, `engines`, contributor machines (this laptop runs Node 21.7 and cannot run `react-doctor` or ESLint 10) | One PR in Phase 1; README states the requirement; prek hook prints a hint if `web/node_modules` is missing |
| R6 | `go test` on a fresh clone with no Node build | `web/dist/.gitkeep` plus a stub handler and `t.Skip` |
| R7 | `react-doctor` contacts a score API, Sentry and Socket.dev by default | Hooks use `--no-score --no-supply-chain`; CI decides separately about the supply-chain scan |
| R8 | The API has quirks the UI hid (two `Server` shapes, `null` collections, `DELETE` with a body, field errors attributed by substring) | Zod schemas normalise; contract tests replay real responses before each form's error mapping ships ([api-types.md open questions](./api-types.md#open-questions--ambiguities-found)) |
| R9 | Admin `Server` JSON includes `ownershipToken`, `passwordSecret` and `saveSeed`, which no UI reads | Out of scope for this migration; the new client's types omit them. Flagged for a separate Go change (API open question 1) |
| R10 | The status model (D1) infers OFFLINE from `attention`/`unknown` | One pure function; owner to confirm; backend `phase`/`statusReason` recommended |
| R11 | AVIF support (Chrome 85+, Firefox 93+, Safari 16.4+, from general knowledge, not re-verified here) | `<picture>` falls back to WebP |
| R12 | `DELETE` with a JSON body can be dropped by some proxies | Existing behaviour; the Go side requires it. Consider accepting `POST .../delete` as an alias in a later API change |
| R13 | The multi-second button delay was not reproduced locally | Measure production with the committed harness before and after Phase 0 |

Owner decisions pending: **D1** (status mapping), **D2** (keep the dragon art), D3 (dark only), D4 (inline token field) in [design-brief.md](./design-brief.md#open-decisions).

## Sources

- [Announcing TypeScript 7.0](https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/), 2026-07-08: no JS API in 7.0, `@typescript/typescript6` compatibility package, breaking changes
- [Announcing Vite 8](https://vite.dev/blog/announcing-vite8), 2026-03-12: Rolldown/Oxc, Node `20.19+ / 22.12+`, `@vitejs/plugin-react` v6
- [React 19.2](https://react.dev/blog/2025/10/01/react-19-2): `<Activity>`, `useEffectEvent`, `eslint-plugin-react-hooks` v6 flat config and compiler rules
- React 19.3.0 (2026-09-09): npm registry dates; feature notes only via a secondary write-up ([DevX](https://www.devx.com/coding/19-3-view-transitions-fragment-refs/)), not independently verified
- [react-doctor](https://github.com/millionco/react-doctor) and [react.doctor/docs](https://react.doctor/docs): README and docs pages; flags, config and behaviour verified by running `react-doctor@0.9.18`
- [prek](https://prek.j178.dev/configuration/) and [local hooks](https://prek.j178.dev/local-hooks/): `repo: local`, `language: system`, `pass_filenames`, workspace mode; syntax verified with `prek 0.5.5`
- [WCAG 2.2](https://www.w3.org/TR/WCAG22/) and [Understanding SC 1.4.1](https://w3c.github.io/wcag/understanding/use-of-color.html)
- [web.dev Web Vitals](https://web.dev/articles/vitals) thresholds
- npm registry metadata for every package and engine range above (queried 2026-10-17); Node release index ([nodejs.org/dist/index.json](https://nodejs.org/dist/index.json)): 24.22.0 is the current LTS
- Repository sources: `main.go`, `web_test.go`, `auth.go`, `docs/oidc.md`, `ARCHITECTURE.md`, `Dockerfile`, `.github/workflows/ci.yml`, `.github/release.test.mjs`, `scripts/verify.sh`, `web/app.js`, `tests/*.cjs`
