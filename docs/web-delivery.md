# Web delivery contract

How the Go service delivers the embedded console (`web.go`, tested by `web_test.go` and `tests/login-budget.cjs`), and what any replacement for it must keep.
The React migration ([plan](design/react-migration-plan.md)) will serve a Vite `web/dist` build from the same handler; this page lists the behaviours to preserve and the two places where React changes the rules.
The measurements behind them are in [Login page performance](design/login-performance.md#results).

## Behaviours to preserve

- **HTML shell: `Cache-Control: no-cache` and a strong ETag.** `/` and `/index.html` are one entry. The browser revalidates on every visit and gets a 304 when nothing changed. There is no `Last-Modified`: the embedded FS has no modification time, and process start would change on every deploy even when the bytes did not, so the ETag is the only validator.
- **`immutable` only for content-addressed URLs.** `public, max-age=31536000, immutable` is sent for `app.js` and `styles.css` only when `?v=` equals the SHA-256 of the current content, and for files under `assets/` only when the hash in the file name equals the first ten hex digits of the content's SHA-256 (`hashedAsset` in `web.go`). A stale `?v=` or a name whose hash does not match the bytes falls back to `no-cache`, so a wrong URL can never be pinned in a browser for a year. Unversioned files always revalidate.
- **gzip for text types, with `Vary: Accept-Encoding`.** HTML, JS, CSS, SVG, JSON and text are compressed once at startup (best compression, only kept if smaller). The gzip representation has its own ETag (`"<hash>-gzip"`), both representations answer `If-None-Match` with 304, and `Vary: Accept-Encoding` is set whenever a gzip variant exists, including on identity responses. `Accept-Encoding` is parsed with q-values and `*`.
- **Content-Length.** Identity and image responses carry `Content-Length`. gzip responses do not: `http.ServeContent` omits it when `Content-Encoding` is set, so they are chunked. That is by design; do not "fix" it by buffering.
- **No recompression of images.** AVIF, WebP and PNG are served as stored; they are already compressed.
- **405 for anything but GET and HEAD**, with `Allow: GET, HEAD`.
- **`fs.ValidPath`, and 404 for directories.** Requests are cleaned with `path.Clean` and validated against `fs.ValidPath` before reading; a directory, a missing file or an invalid path is a plain 404, never a listing.
- **`/api/*` is a separate world.** It is outside this handler and keeps `Cache-Control: no-store` with no ETag. The web handler must never cache or validate API responses, and `login-budget.cjs` asserts the split.
- **Pinned content types.** Types come from a table in `web.go`, not the host MIME database, so a Windows registry cannot change what Linux containers serve.

## What changes for React

1. **Vite's hash pattern is not the `\.[0-9a-f]{10}\.` matcher.** Vite names output `assets/[name]-[hash].[ext]` with an 8-character base64url hash (characters `A-Za-z0-9_-`, separated by a hyphen), and the hash is not a prefix of the SHA-256 of the final bytes. The name-hash-equals-content-hash rule therefore cannot be applied as written. Either teach the handler the Vite shape and treat everything Vite emitted under `assets/` as immutable (safe because Vite regenerates the name whenever the content changes, but the handler is then trusting the build), or have the build step write a manifest of SHA-256 names and keep the verify-the-bytes rule. Whichever is chosen, `index.html` stays `no-cache` and non-hashed files in `dist` (`favicon`, `robots.txt`) stay revalidating, and a test must show that a name that merely looks hashed but does not match is not marked `immutable`.
2. **The SPA fallback needs an explicit rule.** Today an unknown path is a 404, which is correct for a hash-routed vanilla console. A React router using history paths needs `index.html` for unknown navigations, but not for everything: `/api/*`, `assets/*` and anything with a file extension must stay 404, otherwise a missing chunk returns HTML with a 200 and the browser caches or executes it. Write the rule down in the handler and cover it with tests for `/servers/abc` (shell), `/assets/missing.js` (404), `/api/missing` (not the shell) and a directory path.
