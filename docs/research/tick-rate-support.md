# Tick rate support research

Researched 2026-10-17 against `main` at `ee2746e`. The owner reported that the "Tick rate" metric "seems to not be supported anymore" and always shows failures. I could not observe the owner's deployment, so the diagnosis below comes from the code, the published artifacts, and the companion repositories.

## Verdict

**Supported only with a specific patched image, and that image is broken on every current Dragonwilds build. Treat tick metrics as unsupported today.**

- No upstream RSDWServerAPI release has a `/api/metrics` endpoint. Only the `petzkod5/rsdragonwilds-helm` image adds it.
- That image only produces tick data on game executable build ID `3b4ce30aed886594`, a pre-1.0 (Early Access) build. Every other build fails closed.
- Dragonwilds 1.0 shipped on 2026-09-15. The dedicated server binary changed, and the hook has been inert since then.
- The console's default image (`0.1.1`) predates the hook and has no `/api/metrics` route at all.
- The fix is open upstream and unmerged (helm PR #7). It re-locks the hook to a single 1.0 build, and Steam's public build has moved twice since.

## Evidence

### 1. Where the endpoint comes from

- Upstream `dkoz/RSDWServerAPI` (HEAD `994a7209`, 2026-09-15, release `0.1.3`) registers `/api/health`, `/api/players`, `/api/items`, `/api/kick`, `/api/ban`, `/api/unban`, `/api/bans`, `/api/broadcast`, `/api/give` and `/api/hello`. There is no metrics or tick route.
- The `ApachaiZ/RSDWServerAPI` fork (HEAD `fd00729c`, 2026-10-15) adds `/api/status`, `/api/tell`, `/api/heal`, `/api/teleport` and others, but still no metrics or tick route. Nexus mod 530 returned 403 to anonymous requests, so I did not read it. Both source trees show no such endpoint.
- `GET /api/metrics` exists only through [`patches/rsdw-api-ticks.patch`](https://github.com/petzkod5/rsdragonwilds-helm/blob/v0.2.1/patches/rsdw-api-ticks.patch) in `petzkod5/rsdragonwilds-helm`, applied by the Dockerfile on top of pinned RSDWServerAPI commit `1bf3b918`. That patch first shipped in helm **v0.2.0** (2026-09-16). It is absent from v0.1.0 and v0.1.1.

### 2. Published artifacts

`ghcr.io/petzkod5/rsdragonwilds-server` and `ghcr.io/petzkod5/charts/rsdragonwilds` both list tags `0.1.0 0.1.1 0.2.0 0.2.1` (anonymous OCI tags API, 2026-10-17). `0.2.1` is the latest. The helm repo has had no release since 2026-09-18.

| Image / chart | `/api/metrics` | Result in this console |
| --- | --- | --- |
| `0.1.0`, `0.1.1` (console default, `main.go:633`, `main.go:1251`) | Does not exist (404) | `collectTicks` maps the 404 to status `unsupported` (`kube_telemetry.go:683-686`). |
| `0.2.0`, `0.2.1` on a pre-1.0 game build | Works only for build `3b4ce30aed886594` | Not reachable now, because Steam no longer serves that build. |
| `0.2.0`, `0.2.1` on any 1.0 game build | Returns `status:"unsupported"`, `reason:"unsupported-build-or-mapping"` | The decoder accepts `unsupported` (`kube_telemetry.go:727`) and every tick reading is marked unsupported. |

Every image tag extends Jagex `rsdw-dedicated:1.1.1`, pinned by digest. The game binary is not baked in. SteamCMD downloads whatever Steam's public branch serves at start (helm issue #5; helm README "Known limits": "Steam can update the game independently"). So the image tag does not pin the game build.

### 3. The build-ID lock

- The patch hardcodes `kBuildId = 3b4ce30aed886594`, plus the vtable slot `0x2252418` and the function address `0x9d2eab0`. Installation validates the build ID, ELF headers and byte patterns. Unknown builds fail closed with no late retry (patch README section; the helm README says "Tick measurements require executable build ID `3b4ce30aed886594`").
- The `3b4ce...` build is the one recorded in this repo's `verification/tick-design.md` and `verification/tick-verification.md`, dated 2026-09-16. It is a pre-1.0 build.
- **1.0 build `f9be766885c63969`** (Steam buildid `25387240`) was reported in [helm issue #5](https://github.com/petzkod5/rsdragonwilds-helm/issues/5) on 2026-09-20 against image `0.2.1`. Its `/api/metrics` returned `unsupported-build-or-mapping`.
- [Helm PR #7](https://github.com/petzkod5/rsdragonwilds-helm/pull/7) (open, unmerged, 2026-09-23, from a third party) reports that the code addresses are unchanged in `f9be...` and that only the vtable position (`+0x40`) and build ID differ. It claims a live `ready` response after the change. This repo has not independently verified that claim. The PR replaces the single build ID rather than adding a second profile, and it leaves the one-build-at-a-time design in place.
- The game has moved on since. `api.steamcmd.net` shows the public branch at buildid **25991736**, updated 2026-10-16. The `ApachaiZ` fork documents 1.0.1 as Steam build `25811348` and CL `248854`. Neither build's ELF build ID or tick addresses have been checked against the hook. I could not download the binary here. Expect another failure on each Jagex patch until the hook gets a multi-build profile list or a build-independent lookup.
- Helm PR #6 (`characterName` offsets) is unrelated to tick metrics.

### 4. What the owner sees in this console

- `collectTicks` runs on every 15-second collection (`kube_telemetry.go:441`, `:678-691`). A 404 or an `unsupported` payload becomes status `unsupported`. The UI maps that to the text **"Unsupported"** (`web/app.js:647`). It appears on the dashboard Tick-rate column, the server overview stat, the Telemetry page stat, and the four tick-duration tiles.
- **"Check failed"** is a different path (`web/app.js:646`). It shows for any metric, not just ticks, when the whole server status is `unknown` and the reading is `unavailable` with a reason. It does not come from the tick decoder. If the owner sees "Check failed" next to tick rate, the cause is a server-wide collection failure, not tick support, and players or CPU would show the same text. I did not reproduce this, so it is an inference from the code.
- `scripts/kind-smoke.sh:134` already asserts that `tickRate` is `unsupported` against the fixture game API, so "unsupported" is the tested steady state for older images.

## Surfaces if tick metrics are disabled or hidden

Line numbers are for `ee2746e`.

### Backend

| Surface | Location |
| --- | --- |
| Tick constants, key list, source string | `telemetry.go:15-16`, `:19`, `:21` |
| Metric definitions (6 entries, shown in "Metric definitions" and CSV) | `telemetry.go:75-80` |
| Legacy flat `TickRate` assignment | `telemetry.go:269`, `:290-291` |
| Collector call | `kube_telemetry.go:441` |
| Fetch and decode (`collectTicks`, `applyTicks`) | `kube_telemetry.go:678-770` |
| `metrics` endpoint allow-list in `podGameAPI` | `kube_telemetry.go:356` |
| `Server.TickRate` JSON and legacy marshal field | `main.go:97`, `:120` |
| Demo data (`TickRate: 60`, "Tick 482,913", tick-loop log) | `main.go:462`, `:468`, `:600` |

### UI (`web/app.js`)

| Surface | Location |
| --- | --- |
| Metric labels, also driving CSV export and the metric-sources table | `:632-634` |
| Dashboard row `tickText` | `:697` |
| Dashboard card "Tick rate" | `:732` |
| Dashboard table column "Tick rate" | `:739` |
| Telemetry page stat tile and "Tick rate" chart panel | `:790` |
| Telemetry page `tickDurations(server)` call | `:790`, defined at `:850-851` |
| Server overview stat | `:817` |

### Tests, scripts and docs

- Tests: `telemetry_test.go` (22 matches), `kube_telemetry_test.go` (36), `web/app.test.cjs` (26), `tests/dashboard-browser.cjs:19`.
- Scripts: `scripts/kind-smoke.sh:134`, `scripts/verify.sh:10`, and the `verification/check-*tick*.py`, `inspect-tick-build.py`, `tick-preview.cjs`, `tick-live-*.json` fixtures.
- Docs: `docs/telemetry.md` (tick rows and the closing paragraphs), `ARCHITECTURE.md:13`, `verification/tick-design.md`, `verification/tick-verification.md`.

## Options and risks

### Keep as is

- No work and no risk of regression.
- Ongoing cost: every server shows "Unsupported" in several tiles, a chart that never populates, and a failing row in "Metric sources". The owner already reads this as a failure.
- A 404 or `unsupported` response once per 15 seconds adds a small `kubectl exec` curl to every collection cycle.

### Fix

- The console cannot fix this alone. The fix has to land in `petzkod5/rsdragonwilds-helm` (merge PR #7 or write a multi-build profile list, verify against the current Steam build, publish `0.2.2+`). This repo then needs a default-version bump (`RSDW_CHART_VERSION`, `RSDW_DEFAULT_IMAGE_TAG`, `main.go:633` and `:1251`) and a re-run of the live evidence.
- The build ID changed repeatedly in about a month (`3b4ce...`, `f9be...`, then 1.0.1 and Steam buildid `25991736`). A single-build lock gives an ongoing per-patch treadmill. The `f9be...` build was last checked 2026-09-23 and the public build has moved at least twice since.
- The hook writes a function pointer into the game process at startup. The profile checks are strict and fail closed, but each new profile needs fresh disassembly and live verification before shipping. The patch's own README says never to bypass the build check.
- Cost is high and depends on someone outside this repo.

### Disable (recommended)

Add a switch (for example `RSDW_TICK_METRICS`, default off) and hide the feature behind it:

1. Backend: skip `collectTicks` when off, and drop the six tick keys from `emptyMetrics` and the definitions so that no "unsupported" rows appear. Keep the decoder and its tests so re-enabling is a flag flip.
2. UI: render the tick tiles, chart panel, duration section, dashboard column and overview stat only when the server's `metrics` map contains `tickRate` (a data-driven check that avoids a second config path in the browser).
3. Update `scripts/kind-smoke.sh:134` to assert absence, and mark `docs/telemetry.md` and the verification docs as dormant.

Risks of disabling:

- `/api/servers` and CSV exports stop emitting `tickRate`. No in-repo consumer other than the UI was found. An external client of `tickRate` would see it disappear (`omitempty` already omits it when the metric is unavailable).
- History is in-memory only (one hour), so nothing persistent is lost.
- Test churn is moderate because the UI and test coverage references are broad (see the lists above).
- Re-enabling later needs a working upstream image plus a re-check against the new build.

Removing the code entirely is not recommended. The decoder is well tested, and the producer may come back once the helm repo ships a multi-build profile.

## Recommendation

Disable behind a default-off flag now. Revisit when `petzkod5/rsdragonwilds-helm` publishes an image verified against the current Steam build, ideally with more than one accepted build profile, and the live evidence check (`verification/check-live-ticks.py`) passes against it.
