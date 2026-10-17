# Design brief: RSDW C2 console

Status: proposal for owner sign-off, 2026-10-17. Companion to [react-migration-plan.md](./react-migration-plan.md) and [login-performance.md](./login-performance.md).

## Decisions at a glance

| Area | Decision | Replaces |
| --- | --- | --- |
| Direction | **"Front panel"**: the console reads like the front of a rack unit. Status LEDs with words, hairline rules, serial-number style IDs, dense tables. No cards. | Card-per-section dashboard in blue |
| Colour rule | **Colour is information.** The only hues on screen are the five status colours, plus one ember brand mark. Everything else is warm neutral. The interactive accent is **bone** (`#E8DFC6`), not a hue. | Saturated blue `#0063ff` accent |
| Type | **Barlow** (UI) + **Barlow Semi Condensed** (page and section titles, big numbers) + **IBM Plex Mono** (IDs, endpoints, logs). All SIL OFL 1.1, self-hosted, latin subset, 6 files, ~120 KB. | Inter, "Segoe UI", Arial |
| Layout | Rows and rules instead of boxes. One focal point per page. Tables wherever data is tabular. | `.panel` everywhere (76 in `app.js`), 20 `stat()` tiles |
| Status | **ONLINE / OFFLINE / STOPPED** are the headline. Each has its own silhouette *and* a word. STOPPED is neutral, never red. | Stopped rendered red; "Check failed" tiles |
| Missing data | Four explicit metric states: loading, value, stale, unavailable. Unavailable is a quiet `—`, never an error word. | "Check failed", "Collection failed", "Awaiting sample" |
| Motion | State changes only. The single looping animation is the *Starting* ring, and it stops under `prefers-reduced-motion`. | Infinite glow and ember animations on login |
| MASTER.md | **Evolve** its structure rules (44 px targets, white focus ring, reduced motion, accessible charts), **replace** its palette, type and card model. | n/a |

Two choices need your confirmation, flagged **D1** and **D2** in [Open decisions](#open-decisions).

---

## 1. What "AI slop" looks like, and what fixes it

The vocabulary is consistent across 2026 sources. The sources are design blogs and tool vendors (several are selling an anti-slop tool), so treat the list as a shared checklist, not as research. I have marked which tells come from a source and which are this repo's own house rules.

### The tells

| # | Tell | Source |
| --- | --- | --- |
| T1 | Inter as the default face, "paired with a system fallback and nothing else, so no one made a type choice" | [Canatselis, *Spot the Slop*, 2026-07-23](https://world.hey.com/kostac/spot-the-slop-a-ui-designer-s-guide-to-fixing-ai-defaults-4c448c9c); [925 Studios](https://www.925studios.co/blog/ai-slop-design-tells) ("the typeface itself is fine; the tell is that no one chose it") |
| T2 | Purple-to-blue / indigo gradients behind headlines, backgrounds, CTAs; colours named by appearance (`gradient-start`) rather than function | Canatselis; 925 Studios (traces it to Tailwind's default indigo); [Hallmark](https://dev.to/rams901/hallmark-stop-ai-generated-ui-slop-in-one-command-in-2026-3p9n) flags "purple gradient detected" |
| T3 | Identical cards: same radius, same padding, same height everywhere ("everything gets the same 16px radius and 24px padding, the page goes flat"); a dark hero followed by a row of three even feature cards | Canatselis; 925 Studios |
| T4 | Nested cards: icon on top, heading, description; cards inside cards | Hallmark ("Nested cards, generic AI pattern") |
| T5 | Vanity or identical stat tiles: "identical stat cards, the default look of a generated interface" | Canatselis (caption) |
| T6 | A coloured left-edge line with a curve on a panel | Canatselis |
| T7 | Generic copy: "Build the future of work", "Build faster. Ship smarter." | Canatselis; 925 Studios |
| T8 | Thin-line generic icons that could fit any product | 925 Studios |
| T9 | Motion that is either absent or identical everywhere ("everything fades in with the same timing") | Canatselis |
| T10 | Happy path only: no empty, loading, error or invalid-input states | Canatselis |
| T11 | Everything visible at once: a data table, an entry panel and an export panel side by side | Canatselis |
| T12 | Soft drop shadows and thin grey borders on every card; gradient buttons with generic labels ("Get Started") | 925 Studios |
| H1 | *House rule:* uppercase, letter-spaced "eyebrow" micro-labels as decoration | This brief |
| H2 | *House rule:* gradient text, glassmorphism, emoji as icons, decorative gradient blobs, "sparkle" iconography | This brief (the sources I could read do not cover these) |

Context for *why*: the models that generate UI sample from "the high-probability center of their training data" (secondary summaries of Anthropic's [frontend-design guidance](https://claude.com/blog/improving-frontend-design-through-skills); I could not read the post body itself, so this is second-hand). Defaults are the average of many interfaces, not a decision.

### The countermeasures that recur

1. **One deliberate aesthetic direction**, written down, with a reason tied to the product (Canatselis: "Does this feel like it came from somewhere? Does it have a voice? Would I notice if it disappeared?").
2. **A limited palette with one accent, named by function** (primary action, warning, success), not by appearance.
3. **A chosen type pair**: one face with personality for display, one readable face for body.
4. **Hierarchy with a single focal point**: make the primary element bigger, closer and heavier and let the rest recede. Break uniformity on purpose.
5. **Design the unhappy paths**: empty states that tell the user what to do, errors "in human language", loading and invalid states.
6. **Restraint in motion**: micro-interactions on the primary control; remove decorative motion.
7. **Progressive disclosure** instead of showing every control at once.
8. **Process**: build a piece, stop, look at it, note where your eye goes; click through slowly on a bad connection and a small screen; audit against the tells.

### Accessibility constraint that shapes the status design

WCAG 1.4.1 (Use of Color) does not forbid colour; it requires another visual means to carry the same information, such as text, an icon or a change in shape ([W3C, Understanding SC 1.4.1](https://w3c.github.io/wcag/understanding/use-of-color.html)). So every status gets a distinct silhouette **and** a word. Colour is a third, redundant channel.

---

## 2. The current console against the tells

Evidence is from `web/app.js`, `web/styles.css`, `design-system/.../MASTER.md` and `docs/screenshots/issue-39-overview-desktop.png`.

| Tell | Present? | Evidence |
| --- | --- | --- |
| T1 Inter by default | **Yes** | `styles.css:32` `font-family: Inter, "Segoe UI", Arial, sans-serif`; MASTER.md says the same |
| T2 Gradient / default blue | Partly | `--color-primary: #0063ff` is the stock saturated SaaS blue used for primary, active nav, links, chart and focus. Login has three `radial-gradient` glows (`styles.css:400-419`) including a purple `#bb83ff80` orb |
| T3 Identical cards | **Yes** | `class="panel` appears 76 times in `app.js`; 4 px is the radius on 26 of 39 `border-radius` declarations, and MASTER.md fixes one card padding (20/24 px) for all of them |
| T4 Nested cards | **Yes** | Server actions: a `.panel` containing bordered `.action-card`s containing bordered `<button>`s (screenshot). Also `.bot-card`, `.storage-card`, `.provider-card`, `.delivery-card` inside `.panel` |
| T5 Stat tiles | **Yes** | `stat()` has 19 call sites. The overview screenshot shows "Memory working set" and "Memory limit" as two tiles with the same CPU icon, next to a "5 / 12" tile: three different kinds of thing styled as equals. Dashboard leads with "Registered servers", a number you can read off the table below |
| T6 Left-edge accent | **Yes** | `nav a { border-left: 6px }` with a blue active edge (`styles.css:83-85`); `.console-row.attention { border-left-color: warning }`; deploying rows use `inset 3px 0 0 primary` |
| T7 Generic copy | **Yes** | Every page has a stock caption: "Monitor every server from one place.", "Make safe changes to your servers.", "Status, players, and server controls." (`pages` in `app.js`) |
| T8 Generic icons | Partly | One 24px outline set is consistent, but the nav icons (grid, bars, calendar, wrench) are interchangeable with any admin template |
| T9 Motion | Partly | Two infinite looping animations on the login scene; elsewhere only colour transitions (fine) |
| T10 Missing states | **Partly, and it is the owner's complaint** | The code *has* many states, but it surfaces collector trouble as content: `metricText()` returns "Check failed", "Collection failed", "Awaiting sample", "Unsupported", "Stale" in tiles (`app.js:641-648`). The status label for `unknown` is "Unable to confirm" |
| T11 Everything at once | **Yes** | The telemetry page stacks 12 panels plus 8 stat tiles: six charts, resource list, health checks, connected players, tick durations, metric sources, metric definitions, logs |
| T12 Shadows/borders | Partly | Mostly borders, little shadow |
| H1 Eyebrow caps | **Yes** | `.eyebrow`, `.console-label`, `th`, `.lifecycle-details dt`, `.profile-list-head` are uppercase and tracked (`styles.css:159,217,258,509,545`) |
| H2 Blobs, emoji, glass | No | Not present. Keep it that way |

Two product-level defects the audit also found, both fixed by this brief:

- **STOPPED is painted as a failure.** `.console-row.stopped { border-left-color: var(--color-destructive) }` (`styles.css:148`). A server you stopped on purpose looks like one that crashed.
- **The status the owner cares about is one tile among many.** The dashboard's first row is three equal tiles, none of which is "which servers are down".

The login art (see [login-performance.md](./login-performance.md#what-the-user-sees)) is a maximally saturated painted scene with the title laid across the dragon's face. It is the strongest "generated key art" signal on any screen, and it is also what makes the page slow.

---

## 3. Direction: "Front panel"

**One sentence:** a dark operations console for six people and a handful of game servers, designed like the front of a rack unit.

Why this direction and not another:

- It comes from the product's own nouns: servers, ports, uptime, LEDs. A status LED with a word next to it *is* the interface idea, not decoration.
- It justifies the things slop gets wrong: dense tables (a rack is a list of units), tabular numerals and monospace IDs (serial numbers), hairline separators (panel seams), restrained motion (LEDs change state, they do not animate in).
- It leaves room for the one place the game belongs, the login scene and the wordmark, without bleeding fantasy chrome into a tool people use to restart servers at 1 a.m.

Principles. Each one is testable in review.

| # | Principle | Test |
| --- | --- | --- |
| P1 | **Status first.** Every screen's focal point is server state or the thing being changed. | Cover everything but the largest element. Can you still tell what the page is for? |
| P2 | **Colour is information.** Hue appears only for status (and the brand mark). | Desaturate a screenshot. Nothing that mattered should disappear except the status dots, which still differ by shape |
| P3 | **Tables where data is tabular; no cards.** The only raised surfaces are dialogs, popovers and toasts. | Search the diff for a container with its own border and background that holds another bordered container |
| P4 | **Every number has four states** (loading, value, stale, unavailable) and none of them is an error word. | Kill the collector. The dashboard still shows ONLINE/OFFLINE/STOPPED correctly and nothing says "failed" |
| P5 | **One idea of motion.** State transitions only; one looping animation (Starting). | `prefers-reduced-motion: reduce` removes every animation without losing information |
| P6 | **Words a person would say.** "Restart Aurora? Everyone online is kicked." not "Confirm restart". No page captions that restate the title. | Read each string aloud |
| P7 | **Game flavour in two places**: the wordmark and the login scene. | Grep the CSS for the ember token: it appears in the mark only |
| P8 | **Hierarchy over uniformity.** Radius, density and weight vary by role. | Is there a reason each element is the size it is? |

---

## 4. Tokens

Dark only in v1 (`color-scheme: dark`). Tokens are CSS custom properties in one `tokens.css`; a light theme later is one more block. Names are functional (P2).

### 4.1 Colour

Every pair below was computed (WCAG 2.x relative luminance), not eyeballed. "Needs" is the threshold I applied: 7:1 for body text, 4.5:1 for secondary text and coloured text, 3:1 for non-text UI and status glyphs.

| Token | Hex | Role | Key contrast |
| --- | --- | --- | --- |
| `--bg` | `#111210` | page | text on it 15.6:1 |
| `--surface-1` | `#171815` | nav rail, table header band, dialogs | text 14.8:1 |
| `--surface-2` | `#1E1F1B` | inputs, row hover, menus | text 13.8:1 |
| `--surface-3` | `#282923` | selected row, pressed | text 12.2:1; **do not put `--text-3` on it (4.4:1)** |
| `--line` | `#2C2D28` | hairlines and row separators | decorative (1.35:1 vs `--bg`) |
| `--line-strong` | `#6D6E64` | control borders (inputs, buttons, checkboxes) | 3.45:1 on `--surface-1`, 3.21:1 on `--surface-2` (needs 3:1) |
| `--text` | `#ECEAE3` | primary text | 15.6:1 on `--bg` |
| `--text-2` | `#B7B5AB` | secondary text, labels | 9.1:1 on `--bg`, 8.1:1 on `--surface-2` |
| `--text-3` | `#8F8D83` | hints, placeholders, disabled | 5.6:1 on `--bg`, 5.0:1 on `--surface-2` |
| `--accent` | `#E8DFC6` ("bone") | primary buttons, links, focus ring, selected-nav text | 14.1:1 on `--bg` |
| `--accent-ink` | `#17150E` | text on accent fill | 13.7:1 on `--accent` |
| `--accent-hover` | `#F3ECD8` | hover/pressed primary | ink on it 15.5:1 |
| `--online` | `#4CC38A` | ONLINE glyph | 8.5:1 on `--bg`, 7.5:1 on `--surface-2` |
| `--offline` | `#F0645C` | OFFLINE glyph; destructive text | 6.0:1 on `--bg`, 4.7:1 on `--surface-3` |
| `--stopped` | `#A8A89E` | STOPPED glyph (neutral on purpose) | 7.8:1 on `--bg` |
| `--starting` | `#6EA8FE` | STARTING / DEPLOYING / DELETING ring, info | 7.8:1 on `--bg` |
| `--attention` | `#E8B14A` | warning severity; degraded | 9.7:1 on `--bg` |
| `--danger-fill` | `#B83A34` | destructive button fill (white text 5.7:1; 3.3:1 against `--bg`) | confirm dialogs only |
| `--brand-ember` | `#E8742F` | **logo mark only** | 6.2:1 on `--bg` |

Colour-blind check: under simulated deuteranopia the closest pair is ONLINE vs STOPPED (ΔE76 10.4; protanopia 20.7). That is expected for green vs grey, and it is why shape and word carry the meaning (see 5.1). Colour only ever reinforces.

**Why bone, not a hue, as the accent.** The status set already occupies green, red, amber and blue. A fifth hue (teal, orange, violet) would be adjacent to one of them and would make "is this link or a warning?" a question. Purple and indigo are the slop tell (T2). A warm off-white fill is unmistakably a button on this background (13.7:1), costs no hue, and reads as parchment/bone, which is the one game-flavoured touch the palette needs. The risk to watch: warm-neutral dark UIs with serif headlines are becoming a different generated default. This brief avoids the serif half, keeps neutrals very low in chroma, and anchors the look in instrument-panel conventions instead.

**Names are by function** (`--accent`, `--offline`), never `--blue-500` or `--gradient-start` (T2).

**No gradients in the product UI.** The only permitted `gradient` in CSS is the login scrim (a legibility device, allow-listed in lint, see 8.3).

### 4.2 Typography

| Role | Family | Why |
| --- | --- | --- |
| UI text | **Barlow** 400/500/600 | Slightly narrow, DIN/highway-sign heritage, strong at 13-15 px in dense tables, supports `tnum` (verified with fontkit: digits are proportional by default and equal-width with `font-variant-numeric: tabular-nums`) |
| Titles, section headings, the fleet numbers | **Barlow Semi Condensed** 600 | Same skeleton as the UI face, so it reads as one voice; the condensed width gives page titles an engraved-label feel without a second personality |
| IDs, endpoints, image tags, logs, EOS player IDs, JSON | **IBM Plex Mono** 400/500 | Digits tabular by default (verified), dotted zero separates `0`/`O` in 32-hex IDs, no code ligatures to corrupt an ID |

All three are SIL OFL 1.1 (npm `@fontsource/barlow`, `@fontsource/barlow-semi-condensed`, `@fontsource/ibm-plex-mono`, v5.3.0). Self-hosted from the build output; **no external request**. Latin subset only. Measured woff2 sizes: Barlow 400/500/600 22.2/22.0/22.8 KB, Barlow Semi Condensed 600 23.0 KB, Plex Mono 400 14.7 KB (+500 14.9 KB if used) = **~120 KB total across six files, immutable-cached**. The login page needs only Barlow Semi Condensed 600 and Barlow 500 (preload those two, 45 KB).

Not chosen, with reasons: **Inter** (T1), **Geist** and **Space Grotesk** (both turn up in anti-slop "overused default" lists), **JetBrains Mono** (fine, but Plex Mono's tabular default and dotted zero suit hex IDs better; JetBrains' `calt` ligatures are a liability for IDs).

Fallback stack: `"Barlow", "Segoe UI", system-ui, sans-serif`, with a metric-adjusted fallback `@font-face` (`size-adjust`/`ascent-override`) generated at build time so the swap does not shift layout. Numbers are computed in Phase 1; I am not guessing them here.

| Token | Size / line | Use |
| --- | --- | --- |
| `--text-hero` | 40 / 42, Semi Condensed 600, tabular | the fleet status line on the Dashboard (the one focal element) |
| `--text-xl` | 24 / 28, Semi Condensed 600 | page title |
| `--text-lg` | 18 / 24, Semi Condensed 600 | section heading |
| `--text-md` | 15 / 22, Barlow 400 | body, form fields, buttons |
| `--text-sm` | 14 / 20, Barlow 400, tabular numerals | table cells |
| `--text-xs` | 12 / 16, Barlow 500 | timestamps, helper text. Never smaller than 12 |
| `--mono` | 13 / 18, Plex Mono | IDs, logs. Logs 12 / 18 |

Rules: **sentence case everywhere** (H1). Table headers are `--text-xs`, `--text-2`, weight 500, not uppercase. Numbers in tables use `font-variant-numeric: tabular-nums` and right-align when comparable. Never letter-space body text. Page titles are not followed by a caption unless the caption carries information (scope, count).

### 4.3 Spacing, radius, border, elevation, motion

| Group | Tokens |
| --- | --- |
| Space (4 px base) | `--s-1` 4, `--s-2` 8, `--s-3` 12, `--s-4` 16, `--s-5` 24, `--s-6` 32, `--s-7` 48, `--s-8` 64 |
| Row heights | Table row 44 (it holds actions, so it doubles as the touch target). Control height 40, or 44 on `pointer: coarse`. Top bar 40. Nav rail 208 (icon-only 56 below 1024 px) |
| Radius **by role** (P8) | 0 table rows and rules; 3 buttons, inputs, pills; 6 dialogs, toasts, popovers; full circle only for the LED. Deliberately not one value |
| Border | 1 px hairline. Controls use `--line-strong`; separators `--line`. No card borders (P3) |
| Elevation | **None for content.** Overlays only: dialog `0 0 0 1px var(--line-strong), 0 24px 64px #000a`; popover `0 8px 24px #000a`; scrim `#000a`. No shadow on any resting element (T12) |
| Focus | `outline: 2px solid var(--accent); outline-offset: 2px` on `:focus-visible`. Carries over MASTER.md's high-visibility focus rule, now bone instead of white |
| Motion | `--dur-fast` 120 ms (hover, press), `--dur-base` 200 ms (colour/status change, row flash), `--dur-slow` 320 ms (dialog in/out; hero fade 350). Easing `cubic-bezier(.2, 0, 0, 1)`. No bounce, no stagger, no entrance fades on page content |
| Breakpoints | `sm` 640, `md` 1024, `lg` 1280. Below `sm` the nav becomes a bottom bar |
| Z-index | content 0, sticky 10, popover 20, dialog 30, toast 40 |

---

## 5. Status model and copy

### 5.1 Status pill (the signature component)

Anatomy: a 10 px glyph, 6 px gap, a label in `--text` (the label is **not** coloured; only the glyph is). Optional second line in `--text-2`, 12 px, giving the reason.

| State | Glyph (distinct silhouette) | Colour | Label | Meaning |
| --- | --- | --- | --- | --- |
| Online | solid disc | `--online` | Online | pod ready and game engine reported ready |
| Offline | solid diamond | `--offline` | Offline | should be running; not confirmed serving |
| Stopped | hollow square | `--stopped` | Stopped | parked on purpose (scaled to 0); data kept |
| Starting | open ring, slow pulse | `--starting` | Starting | coming up |
| Deploying | open ring, slow pulse | `--starting` | Deploying | create-server watch active (client-side) |
| Deleting | open ring, slow pulse | `--starting` | Deleting | deletion in progress |
| Checking | dashed ring, static | `--text-3` | Checking | first observation not collected yet |
| Needs retry | solid triangle | `--attention` | Needs retry | deletion `stale` |

Only the three headline states are bright. Transitional states share one blue ring. The pulse is `opacity` 1 to .45 over 1.6 s and is disabled by `prefers-reduced-motion`; the ring itself still differs in shape.

### 5.2 D1: mapping the API's statuses to ONLINE / OFFLINE / STOPPED

The Go API has **no `offline` status**. Its set is `online | starting | attention | stopped | deleting | stale | unknown` (`main.go:63-71`). From `kube_telemetry.go` and `telemetry.go`: `attention` means the pod is Ready but the game's own health reading (`engineReady`) is missing; `unknown` means there is no fresh observation (`telemetryMaxAge`) or the pod identity could not be verified. Proposed mapping, implemented in one pure function `presentStatus(server)` so it is a one-file change:

| API `status` | Shown as | Reason line (second line) | Counted in headline |
| --- | --- | --- | --- |
| `online` | **Online** | none | Online |
| `stopped` | **Stopped** | none | Stopped |
| `attention` | **Offline** | "Game health check not answering" | Offline |
| `unknown` after at least one observation | **Offline** | "No telemetry for {n} min" | Offline |
| `unknown` before the first observation (every metric `unavailable` with reason "No observation collected") | Checking | none | not counted |
| `starting` | Starting | none | shown as "· 1 starting" |
| `deleting`, `stale` | Deleting, Needs retry | none | not counted |

The honest weak spot: `unknown` can mean the console cannot see the cluster, not that the server is down. The reason line says "No telemetry", never "server is down", and **OFFLINE is not a system claim about the game process**. Recommended follow-up (not blocking, backend): add `phase` and `statusReason` fields computed server-side so the UI stops inferring. Until then the mapping above is the contract. **D1: confirm that `attention` and post-first-observation `unknown` should both read as Offline.**

### 5.3 Metric cell: four states

| State | When | Render | Accessible name |
| --- | --- | --- | --- |
| **Loading** | no data yet and a request is in flight | a static 28x10 px block in `--surface-3` (no shimmer) | "Loading" |
| **Value** | reading `available` | the number, tabular, with the unit in `--text-2` | the value and unit |
| **Stale** | reading older than 45 s, or `stale` | the last value in `--text-3` with "{n}s ago" in a tooltip | "last value, {n} seconds ago" |
| **Unavailable** | `unavailable`, `unsupported`, `error`, `faulted`, `warming_up`, or server Stopped/Offline | `—` in `--text-3`. A tooltip on hover and focus gives a plain-language reason | "Not available" |

Never rendered in a metric cell: "Check failed", "Collection failed", "Awaiting sample", "Unsupported", "Unavailable", "Collecting". (`unavailable` is the *state*; it is spoken to screen readers but not printed as a word.) When a stopped server is shown, its metric cells are `—` with no tooltip; the row already says Stopped.

Reasons are mapped, not leaked from the collector:

| Collector says | Tooltip says |
| --- | --- |
| `unsupported` | "This game version doesn't report it." |
| `stale` | "Last seen {n}s ago." |
| `error`, `faulted`, `unavailable` | "No reading right now." |
| `warming_up`, `warming`, `starting` | "Still starting up." |

Collector detail is not hidden from the people who need it: the server's **Data sources** disclosure (collapsed by default, admin only) keeps today's "Metric sources" table with raw status, source, observed time and reason. Plain language in the cell, raw detail one click away.

Collection trouble is reported **once**, in the connection indicator, in words: "Live, updated 12:04:11", "Showing data from 12:03:41, reconnecting", "Paused". A full-width error banner appears only when there is no data at all.

---

## 6. Components

Each component lists anatomy, states and the accessibility contract. All are plain React + CSS Modules on the tokens above (no component library, see the plan).

| Component | Spec |
| --- | --- |
| **StatusPill** | As 5.1. `role="img"` is not used: the label is real text, the glyph is `aria-hidden`. Reason line is part of the cell, not a tooltip |
| **ServerTable / ServerRow** | Desktop: a real `<table>`. Columns Status, Server (name; endpoint in mono with a copy button), Players, TPS, Memory, Uptime, Next reboot, Actions. Sorted by severity (Offline, Starting, Online, Stopped), then name. Row hover `--surface-2`, no border colour change, no lift. Below `sm`: each server becomes a **two-line row** (status+name / players, TPS, uptime) with actions in a `⋯` menu. It is a list item, not a card |
| **MetricCell / MetricTile** | The four states in 5.3. A `MetricTile` exists only for the two places a single number *is* the point (the Dashboard fleet line, the server header); everywhere else a metric is a labelled row in a definition list |
| **DataTable** | Sticky header on `--surface-1`, 44 px rows, `--line` separators, optional sortable headers (button inside `th`, `aria-sort`), horizontal scroll container with `tabindex=0` and an accessible name on narrow screens. Row selection uses `aria-selected` and `--surface-3` |
| **Button** | Primary: bone fill, ink text. Secondary: 1 px `--line-strong`, transparent. Quiet: text only, underline on hover. Destructive: only inside a confirm dialog (`--danger-fill`, white text) or as a quiet red-text row action. Min 40 px (44 coarse). Label is a verb plus object ("Restart Aurora") |
| **Form** | Label above, always visible. Helper text under in `--text-xs`. Errors inline under the field **and** in a summary (`role="alert"`, links to fields), as the current integrations/reboots forms already do. Required is stated in words, not asterisks. Units are suffixes inside the field ("MiB"). Native validation attributes are kept (they carry `reportValidity`, which the tests rely on) |
| **Dialog** | Native `<dialog>` + `showModal()`, so focus trapping, Esc and the top layer come free. Heading is the accessible name, first focus is the safest control, focus returns to the opener. Destructive confirms name the object and the consequence. **Type-to-confirm only for permanently deleting world data** (already the product's rule). Busy state disables close |
| **Toast** | One at a time, bottom-left, `role="status"`, 5 s (existing behaviour), text only, no icon, no gradient, dismissible. Never used for errors that need action (those go inline) |
| **Nav** | Left rail, 9 destinations with a 16 px icon and a label. Current page: `--surface-3` fill, `--text`, weight 600. No left-edge bar (T6). Below `md`: icon rail with `title`; below `sm`: bottom bar (Dashboard, Events, Maintenance, More) |
| **Segmented control** | For small closed sets (range 60 s/5 m/1 h, event category, fleet filter). Radio group semantics, 40 px, selected = `--surface-3` + bone text |
| **Connection indicator** | Top bar, right. A glyph and words: "Live, updated 12:04:11" / "Paused" / "Reconnecting, showing 12:03:41". Doubles as the pause toggle |
| **ID chip** | Mono text with a copy button; copies on click and announces "Copied" via the toast region. For endpoints, EOS IDs, image tags |
| **Empty state** | One sentence of what is missing, one sentence of what to do, one button. No illustration. Example: "No servers yet. Create one to see its status here." |
| **Charts** | Hand-built SVG (see plan). 1.5 px lines, no area fill, no gradient, no markers except the last point; direct label of the latest value at the line end; 4 horizontal gridlines in `--line`; time axis with two labels. Two series use `--text` and `--text-2` with dash difference, **not** two hues. `role="img"` with a text summary, and a "View data" disclosure holding a real table (kept from today) |
| **Icons** | The existing hand-drawn 24 px outline set (about 25 glyphs, all in `app.js`) ported to one `<Icon>` and trimmed to what is used. 1.5 px stroke, `currentColor`, always paired with text except in the icon rail. No icon-in-a-coloured-square |
| **Wordmark** | The existing hexagon mark in `--brand-ember` (the only ember pixel), "Dragonwilds Control" in Semi Condensed 600, sentence case |

---

## 7. Page layouts

Shell, 1280 px and wider:

```
+------------------------------------------------------------------------------------------+
| (mark) Dragonwilds Control          dragonwilds-prod . Kubernetes   o Live 12:04:11  Admin v |  40 px
+--------------+---------------------------------------------------------------------------+
| Dashboard    |  <page title>                         [Server: All servers v]  [refresh]    |
| Server       |  <page body>                                                              |
| Telemetry    |                                                                           |
| Events       |                                                                           |
| Maintenance  |                                                                           |
| Saved IDs    |                                                                           |
| Integrations |                                                                           |
| Reboots      |                                                                           |
| Backups      |                                                                           |
+--------------+---------------------------------------------------------------------------+
```

The page header holds the title and **one** scope control. The old "Pause updates" and "Updated at" move into the connection indicator. Pages that are not per-server hide the server selector, as today (`servers`, `users`, `integrations`, `reboots`, `backups`).

The wireframes use `●` online, `◆` offline, `▢` stopped, `◌` starting/checking, `—` unavailable.

### 7.1 Dashboard (focal point: the fleet status line)

```
Dashboard                                                      [Needs attention] [All]
                                                                    (segmented filter)
 ● 3 online      ▢ 1 stopped      ◆ 0 offline        ·  1 starting      <- 40 px, the focal point
 ----------------------------------------------------------------------------------------
 Status          Server                       Players  TPS    Memory          Uptime  Next reboot          
 ◆ Offline       Cinder North                    —      —       —                —    Tomorrow 05:00      [Restart]
   Game health   10.20.0.23:7777 [copy]
   check not answering
 ◌ Starting      Ember Vale                      —      —       —                —    Not scheduled       
 ● Online        Aurora Expanse    update        5 / 12 59.8   6.0 / 14.9 GB   3d 4h  Today 15:00         [Restart] [Stop]
                 10.20.0.21:7777 [copy]
 ▢ Stopped       Brin Hollow                    0 / 8   —       —                —    Not scheduled       [Start]
 ----------------------------------------------------------------------------------------
 Recent activity                                                       [All events ->]
 12:03   Aurora Expanse   Restart requested by admin
 11:40   Cinder North     Game health check stopped answering
```

- The fleet line is real information derived from the same rows (P1); it replaces the three tiles. A zero in **Offline** is dim; a non-zero is `--offline` and bold. Clicking a count filters the table.
- Table sorted by severity. "Update" is a small text tag next to the name, not a badge (kept for `updateAvailable`).
- One view. The Row/Table toggle (`dashboardView`) is removed: it showed the same data twice.
- Empty (no servers): one sentence, one button ("Create a server"). Viewers see the sentence only.
- Loading first paint: the fleet line shows `— online` placeholders and the table shows skeleton rows (names unknown) for up to 600 ms, then the first data. If bootstrap fails and there is no data: "Can't reach the console service. Retrying." with a "Try again" button.
- Mobile: fleet line wraps to two lines; table becomes two-line rows; actions in `⋯`.

### 7.2 Server overview (focal point: name + status + the one next action)

```
< Dashboard
Aurora Expanse   ● Online                                              [Restart]  [More v]
10.20.0.21:7777 [copy]    image 0.1.1  ->  0.1.2 available
 ----------------------------------------------------------------------------------------
 Players  5 / 12      Tick  59.8 TPS      Memory  6.0 / 14.9 GB      Uptime  3d 4h
 ----------------------------------------------------------------------------------------
 Connected players                                   Details
 Name            Character                           Next reboot   Today 15:00 (America/New_York)
 Aster Vale      Warden Aster                        Namespace     dragonwilds
 Brin Hollow     Brin                                Last restart  12 Oct 03:00
 ...                                                 Server ID     aurora   [copy]
 ----------------------------------------------------------------------------------------
 Telemetry  Maintenance  Events  Reboots                (text links, scoped to this server)
```

- The inline definition row replaces four stat tiles (P1, T5). Memory working set and limit are **one** value "6.0 / 14.9 GB".
- "More" holds Edit settings, Update image, Check update, and Delete (last, red text, separated). Restart is the single visible action, because it is the most common.
- Roster states keep today's logic (fresh, stale, error, names unavailable, no players) as one sentence in the empty area; no panel.

### 7.3 Telemetry (focal point: tick-rate chart)

```
Telemetry   Aurora Expanse v                       (60 s | 5 m | 1 h)             [Export CSV]
 ----------------------------------------------------------------------------------------
 Tick rate                                                           59.8 TPS  <- direct label
 [ 240 px line chart, 4 gridlines ]
 ----------------------------------------------------------------------------------------
 Players                      Network (in / out)          | Now
 [ 120 px ]                   [ 120 px ]                  | CPU          38 % of 2 cores
                                                          | Memory       6.0 / 14.9 GB
 CPU history                  Memory history              | Disk         41 %
 [ 120 px ]                   [ 120 px ]                  | Pod network  1.2 MB/s
 ----------------------------------------------------------------------------------------
 Tick duration p50 / p95 / p99      2.1 / 4.8 / 7.9 ms          (one row of text + p95/p99 chart)
 Health checks   (list)       ▸ Data sources (collapsed)       ▸ About these metrics (collapsed)
 Logs  [search...]  [Refresh] [Download]
 [ monospace console, 100 lines, focusable ]
```

- Four tiles at the top disappear; their numbers live in "Now" (a definition list) and as direct labels on the charts. Tick-duration tiles become one line of text.
- "Data sources" and "Metric definitions" are disclosures (progressive disclosure, T11). 20 boxes (12 panels and 8 tiles) become one large chart, four small charts, one list, two disclosures and the logs.
- Stopped or offline server: charts show their last data dimmed with a one-line "Server is stopped. Showing the last recorded data." and the logs section is omitted (matches `rejectStopped`).

### 7.4 Events (focal point: the event table)

```
Events   [ search events...        ]  (All | System | Players | Health | Updates)  [24 h v] [Export CSV]
 126 matching . 4 warnings . 1 critical                                  <- one line of text, not tiles
 ------------------------------------------------------+---------------------------------
 Time      Server           Category   Event            |  Event details
 12:03:11  Aurora Expanse   System     Restart reque... |  Restart requested by admin
 11:58:40  Cinder North     Health     Game health ...  |  Aurora Expanse . Warning . 12:03:11
 ...                                                    |  { "actor": "...", ... }   [Copy JSON]
 [Load older]                                           |
```

- Master/detail: the table is the page, the detail pane is sticky on the right (>= `lg`). Below `lg`, selecting a row opens the detail in a dialog.
- Severity is a StatusPill-style glyph (warning triangle, critical diamond) plus the word, replacing the coloured text.
- Keeps `event-row` as a real button inside the message cell so keyboard selection and focus restoration match today.

### 7.5 Maintenance (focal point: the action bar)

```
Maintenance   Aurora Expanse v                                  
 [Restart] [Update image] [Check update] [Stop] [Edit settings] [Restore ...]          [Delete server...]
 ----------------------------------------------------------------------------------------
 Lifecycle                                           Readiness
 Current image    ghcr.io/.../server:0.1.1           Health        ● Online
 Desired image    ghcr.io/.../server:0.1.2           Players       5 / 12
 Uptime           3d 4h                              Image         Update available
 Last restart     12 Oct 03:00                       Last seen     12:04:09
 Namespace / endpoint / limits / creator / ID        Recent changes (4 rows)
 ----------------------------------------------------------------------------------------
 Audit trail  (table, 10 rows)                                              [View events ->]
```

- The destructive action sits apart on the right and is a quiet red-text button; it opens the confirm dialog. Today's "action cards in a grid" are gone (T4).
- Deploy progress, when a create is running, is a 3-step line at the very top ("Accepted, Starting, Healthy") with the Starting ring pulsing; failed/timed-out states keep today's copy.
- A parked server shows **Start** as the single primary action and Restore beside it.
- Deletion receipts render below as a plain list with the same text content as today.

### 7.6 Saved IDs

A two-column table (Name, Player ID in mono with copy) with Edit and Delete as row actions, an **Add saved ID** primary button, and the one-sentence note "Pick a saved ID when creating a server. Editing or deleting one never changes servers that already exist." Empty: "No saved IDs. You can always paste an ID when creating a server."

### 7.7 Integrations (focal point: the destinations table)

```
Integrations                                  (All | Discord | HTTPS webhook)         [Add destination v]
 Destination        Type     Servers              Rules                   Status      Last delivery
 Ops channel        Discord  Aurora, Brin         Offline, Update         ● Enabled   Sent 11:40     [Configure] [Send test]
 Pager endpoint     Webhook  All                  Offline                 ▢ Disabled  Failed 11:02   [Configure] [Send test]
 ----------------------------------------------------------------------------------------
 Recent deliveries
 Time   Server   Message                          Destination     Attempts  Result
```

- The hub of provider cards disappears. The old routes `#integrations/discord` and `#integrations/webhook` stay as the filter state of the segmented control (URL parity).
- Delivery status uses the same pill grammar (Sent = disc, Failed = diamond, Retry = ring, Uncertain = triangle) plus the word.
- The "Uncertain means a message may have been sent..." explanation becomes a tooltip on the status word, not a standing paragraph.

### 7.8 Reboots

Schedules table (Server, Timing, Execution zone, State, Next run, Last result, row actions), with the **Display timezone** select and **Add schedule** in the section header. Execution history table below. The "availability" and "demo" notices remain as one-line text banners above the table. The add/edit dialog keeps the mode switch (Cron, Interval, Daily), the acknowledgement checkbox and the "Preview next five runs" result, restyled as a plain list.

### 7.9 Backups (focal point: last successful backup, then history)

```
Backups   [Run backup now]  [Settings]               (Overview | Schedules)
 Last successful  Aurora Expanse . 2 h ago . 412 MB      Next  tonight 02:00        Storage  1.2 / 20 GB
 [ 1 line of text; "Needs attention: Brin Hollow, backup failed 02:00 [View run]" appears only if true ]
 ----------------------------------------------------------------------------------------
 Recent backups   [Server: All v]                                 | Upcoming schedules
 Server / profile   Created  Source  Size  Result  Actions       | Aurora . daily 02:00 . next tonight
 ...                                                              | ...        [Manage schedules]
```

- The "Backup health" card with four sub-tiles becomes one sentence of text plus a conditional attention line.
- Schedules view: the schedules table, then history. Settings: a Profiles master-detail list and a Storage table. The numbered "step 1/2/3" bubbles in the profile, schedule and run forms become plain `<h3>` sections.
- Restore keeps its two-step destructive confirmation exactly (the 409 `restore_confirmation_required` flow).

### 7.10 Login (focal point: the sign-in action)

```
 (full-bleed artwork, graded dark; placeholder blur first, then the AVIF fades in)
 
                                                       
                                                        
 Dragonwilds Control                                   <- bottom-left plaque on a scrim,
 Petzko C2 Systems                                        nothing over the dragon
 [ Log in ]       (token mode: [ Admin token __________ ] [ Sign in ])
```

- The title block moves off the dragon's face to a bottom-left plaque with a gradient scrim (the one allow-listed gradient). The art is graded in the encode (darker, slightly desaturated), not with CSS filters.
- **Token mode shows the token field inline** instead of the Log in button then a dialog: one step instead of two. OIDC mode shows a single **Log in** button that goes to `/api/auth/login`.
- The two infinite glow/ember animations are removed (P5).
- Before auth is known the page is the boot shell only: flat `--bg`, wordmark, "Connecting...". No hero and no button until `/api/auth` says the visitor is signed out (see [login-performance.md](./login-performance.md#4-stop-painting-the-login-scene-before-auth-is-known-f7-f8)).
- Errors in words, in place: "That token wasn't accepted." / "Sign in failed, or this identity has no role here. Ask an admin or try another account." / "Couldn't confirm sign-out. Retry before leaving this browser."

---

## 8. Why none of this is slop

### 8.1 Decision to tell to reason

| Decision | Counters | Reason it is a decision, not a default |
| --- | --- | --- |
| Front-panel metaphor | T3, T4, T5 | Derived from servers/LEDs/ports; explains density, mono IDs, hairlines |
| Bone accent, hue only for status | T2, T12 | Prevents "is this link or a warning?"; no gradient to name |
| Barlow + Semi Condensed + Plex Mono | T1 | Chosen for tabular figures, narrow density, hex-ID legibility; verified features |
| Rows and rules, no cards | T3, T4, T6 | Data is tabular; boxes only where an overlay needs one |
| Fleet status line instead of tiles | T5 | The number that matters is bigger; the rest recede (P1, P8) |
| STOPPED neutral, shape-coded | (product bug), WCAG 1.4.1 | Stopped is not a failure; silhouettes survive colour-blindness |
| Four metric states, quiet `—` | T10 | Owner's requirement; nothing prints "failed" in a cell |
| Variable radius, density and weight | T3 | "Break the uniformity on purpose" |
| Disclosures for data sources/definitions | T11 | Progressive disclosure; 20 boxes to about 8 regions |
| Words in people's voice, no page captions | T7 | P6 |
| Motion: Starting ring only | T9 | State change is the only thing worth animating |
| Login plaque off the dragon, graded art | T3, product | Legibility and speed; art is the one allowed flavour (P7) |
| Hand-built SVG charts, no gradients | T2, T12 | Charts carry data, not decoration |

### 8.2 Review checklist (paste into the PR template)

1. Could the first screen belong to any admin template? If yes, find the specific noun it is missing.
2. Is there any hue that is not a status or the logo mark?
3. Any container with its own border and background inside another one?
4. Is any number a tile when it could be a labelled row?
5. Does every number have loading, stale and unavailable states, and does none print an error word?
6. Does every status have a distinct silhouette plus a word?
7. Is any text under 12 px, uppercase-tracked decoration, or a caption that restates the title?
8. Does anything move that is not a state change? Does reduced motion remove all of it?
9. Is every irreversible action named, with its consequence, in its own dialog?
10. Kill the collector and the network in turn. Is the dashboard still honest and calm?
11. Squint test: where does the eye go first? Is that the right thing?
12. Would you notice if this element disappeared?

### 8.3 Automated guards (cheap, run in prek and CI)

These make the rules enforceable instead of aspirational. Config lives with the Phase 1 scaffold (see plan).

- **stylelint** (`stylelint` 17.16, `stylelint-config-standard` 40) with `declaration-property-value-disallowed-list` forbidding `/gradient/` in `background*` and `box-shadow` outside `login.module.css` and `tokens.css`, `color` and `background*` values that are not `var(--*)` (so no raw hex outside `tokens.css`), `text-transform: uppercase` and `letter-spacing` anywhere except an allow-list, and `border-left`/`border-inline-start` on non-table elements.
- **ESLint**: a tiny local rule forbids a JSX string literal in a metric component matching `/check failed|collection failed|awaiting sample/i`.
- **Unit test** on `presentStatus` and `metricCell` proving every API status/reading maps to a state and that no cell label is an error word.
- **Playwright** screenshot assertions on the canonical states (all five statuses, each metric state, empty, loading) so regressions to the look are visible in review.
- **react-doctor** `design` command: I ran it on a sample containing a purple gradient and Inter. It reported only the layout-shift issue (`no-img-without-dimensions`), so it does **not** catch palette or type slop. Use it for what it is good at (see the plan), not as a design lint.

---

## 9. MASTER.md: keep, evolve, replace

`design-system/rsdw-command-and-control/MASTER.md` encodes the mockup palette and a card layout. Its structural and accessibility rules are good. Its identity is the slop.

| MASTER.md says | Verdict | Why |
| --- | --- | --- |
| Page `#171f25`, card `#1d272e` | **Replace** with warm basalt `--bg`/`--surface-*` | Cool blue-grey plus a saturated blue accent is the stock dark SaaS look; cards are removed |
| Primary/active `#0063ff`, links `#69afff`, chart `#2584ff` | **Replace** with bone accent; charts neutral | One accent, no hue except status (P2) |
| Status greens/ambers/reds `#32d766` `#ffc329` `#ff5d66` | **Evolve** (`#4CC38A` `#E8B14A` `#F0645C`) | Same semantic families; re-tuned for the new background and checked at 4.5:1 for text use. Add blue ring for transitional and a neutral for stopped |
| "Green only on healthy dots; status text stays white" | **Keep** | Matches the pill spec: only the glyph is coloured |
| "Status text accompanies every status dot" | **Keep, strengthen** | Add distinct silhouettes (WCAG 1.4.1) |
| Inter, Segoe UI, Arial | **Replace** | T1 |
| Title 40 px desktop / 32 px mobile, body 16 px | **Evolve** | Title 24 px Semi Condensed; 40 px is reserved for the fleet line; body 15 px, table 14 px. Density is the point of a console. 12 px floor |
| Header 66 px, sidebar 233 px | **Evolve** | 40 px bar, 208 px rail; gives rows back |
| Cards 4 px radius, 20/24 padding | **Replace** | No cards; radius by role |
| 44 px minimum control height | **Keep** | Rows and primary controls stay 44 (40 on fine pointers for form controls) |
| White 2 px keyboard focus outline | **Keep, retint** | Bone, 2 px, offset 2 |
| Tables scroll inside their container, text stays 16 px | **Keep scroll, change size** | Scroll container is focusable and named |
| Charts expose series names and latest values via accessible labels | **Keep** | Plus a real data table behind a disclosure, as today |
| Native dialogs keep labels, error announcements, focus handling | **Keep** | `<dialog>` + `showModal()` stays |
| Scrollable logs/JSON are keyboard focusable | **Keep** | |
| Reduced-motion disables transitions and animations | **Keep** | Now trivially true: one animation |
| "Missing metrics show an unavailable value or explicit empty state; no invented comparisons" | **Keep, sharpen** | Becomes the four-state metric cell (5.3) |
| Page list covers four pages | **Update** | Nine pages plus login |

Action: in Phase 1, replace MASTER.md with a generated page (`design-system/.../MASTER.md` written from `tokens.css` and this brief) so the doc and the code cannot drift.

---

## Open decisions

| # | Decision | Recommendation | If you disagree |
| --- | --- | --- | --- |
| **D1** | Map API `attention` and post-observation `unknown` to **Offline** (5.2) | Yes, with a reason line; add `phase`/`statusReason` to the API later | Add a fourth bucket "Degraded" (amber triangle) for `attention`; headline becomes four counts |
| **D2** | Keep the dragon artwork on the login page | Keep it, because it is the one place the game belongs. Regrade it darker and less saturated, move the title off the dragon, ship it small. If it still reads as generated key art to you, replace it with a flat `--bg` login and the wordmark (the fastest option possible) | Remove it; saves ~170 KB more and is the least "slop" option |
| D3 | Dark only in v1 | Yes. Tokens allow a light theme later | Add `prefers-color-scheme: light` block in Phase 6 |
| D4 | Token mode inline field on login instead of Log in then dialog | Yes (one step instead of two) | Keep the dialog for exact parity |

## Sources

- Kosta Canatselis, [Spot the Slop: A UI Designer's Guide to Fixing AI Defaults](https://world.hey.com/kostac/spot-the-slop-a-ui-designer-s-guide-to-fixing-ai-defaults-4c448c9c), 2026-07-23 (mirrored at [mania.design](https://www.mania.design/blog/spot-the-slop-a-ui-designers-guide-to-fixing-ai-defaults/))
- 925 Studios, [AI Slop Fonts and Gradients: The Tells That Give Away AI Design](https://www.925studios.co/blog/ai-slop-design-tells) (page metadata and visible date disagree: June or October 2026)
- [Hallmark: Stop AI-Generated UI Slop in One Command in 2026](https://dev.to/rams901/hallmark-stop-ai-generated-ui-slop-in-one-command-in-2026-3p9n) (tool vendor; used only for the named patterns)
- Anthropic, [Improving frontend design through Skills](https://claude.com/blog/improving-frontend-design-through-skills) and the [frontend-design skill listing](https://skills.sh/anthropics/claude-code/frontend-design) (post body not readable by my fetch; claims here come from third-party summaries)
- W3C, [Understanding SC 1.4.1 Use of Color](https://w3c.github.io/wcag/understanding/use-of-color.html)
- Verified locally: contrast ratios and colour-blind distances ([`tools/palette-check.cjs`](./tools/palette-check.cjs): WCAG 2.x relative luminance and Machado 2009 matrices), font OpenType features (fontkit), woff2 sizes (jsDelivr `content-length` of `@fontsource/*` files), `react-doctor design` behaviour (run on a sample)
