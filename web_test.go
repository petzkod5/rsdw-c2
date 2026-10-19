package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedIndexVersionsAssets(t *testing.T) {
	app := newTestApp(t, false)
	for _, target := range []string{"/", "/index.html", "/?refresh=1", "/./index.html"} {
		res := httptest.NewRecorder()
		app.ServeHTTP(res, httptest.NewRequest(http.MethodGet, target, nil))
		if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-cache" || res.Header().Get("ETag") == "" || res.Header().Get("Last-Modified") == "" {
			t.Fatalf("%s: status=%d headers=%v", target, res.Code, res.Header())
		}
		for _, name := range []string{"app.js", "styles.css"} {
			if strings.Contains(res.Body.String(), `"/`+name+`"`) {
				t.Fatalf("%s: unversioned %s reference", target, name)
			}
			pattern := regexp.MustCompile(`"(/` + regexp.QuoteMeta(name) + `\?v=[a-f0-9]{64})"`)
			match := pattern.FindStringSubmatch(res.Body.String())
			if match == nil {
				t.Fatalf("%s: missing content hash for %s", target, name)
			}
			asset := httptest.NewRecorder()
			app.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, match[1], nil))
			want, err := webFiles.ReadFile("web/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if asset.Code != http.StatusOK || asset.Body.String() != string(want) {
				t.Fatalf("%s: response does not match embedded asset, status=%d", match[1], asset.Code)
			}
			if cache := asset.Header().Get("Cache-Control"); cache != "public, max-age=31536000, immutable" {
				t.Fatalf("%s: cache-control=%q", match[1], cache)
			}
			if contentType := asset.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") && !strings.Contains(contentType, "text/css") {
				t.Fatalf("%s: content-type=%q", match[1], contentType)
			}
		}
	}
	res := httptest.NewRecorder()
	app.ServeHTTP(res, httptest.NewRequest(http.MethodHead, "/", nil))
	if res.Code != http.StatusOK || res.Body.Len() != 0 || res.Header().Get("Cache-Control") != "no-cache" || res.Header().Get("ETag") == "" || !strings.HasPrefix(res.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("HEAD index: status=%d headers=%v body=%q", res.Code, res.Header(), res.Body.String())
	}
}

func TestAssetURLsChangeWithContent(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	root := fstest.MapFS{
		"index.html": {Data: index},
		"app.js":     {Data: []byte("abc")},
		"styles.css": {Data: []byte("abc")},
	}
	indexBody := func() string {
		t.Helper()
		res := httptest.NewRecorder()
		newWebHandler(root).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
		if res.Code != http.StatusOK {
			t.Fatalf("index status=%d", res.Code)
		}
		return res.Body.String()
	}
	original := indexBody()
	const abcHash = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	for _, name := range []string{"app.js", "styles.css"} {
		url := "/" + name + "?v=" + abcHash
		if !strings.Contains(original, `"`+url+`"`) {
			t.Fatalf("missing expected content-derived URL %s", url)
		}
		root[name].Data = []byte("changed")
		changed := indexBody()
		if strings.Contains(changed, `"`+url+`"`) || strings.Contains(changed, `"/`+name+`"`) {
			t.Fatalf("%s retained a stale asset reference", name)
		}
		match := regexp.MustCompile(`"(/` + regexp.QuoteMeta(name) + `\?v=[a-f0-9]{64})"`).FindStringSubmatch(changed)
		if match == nil {
			t.Fatalf("changed %s has no versioned URL", name)
		}
		asset := httptest.NewRecorder()
		newWebHandler(root).ServeHTTP(asset, httptest.NewRequest(http.MethodGet, match[1], nil))
		if asset.Code != http.StatusOK || asset.Body.String() != "changed" {
			t.Fatalf("changed asset status=%d body=%q", asset.Code, asset.Body.String())
		}
		for _, other := range []string{"app.js", "styles.css"} {
			if other != name && !strings.Contains(changed, `"/`+other+`?v=`+abcHash+`"`) {
				t.Fatalf("unchanged %s URL changed", other)
			}
		}
		root[name].Data = []byte("abc")
		if restored := indexBody(); restored != original {
			t.Fatal("identical content did not produce identical asset URLs")
		}
	}
}

func serveWeb(handler http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, request)
	return res
}

func TestWebConditionalRequests(t *testing.T) {
	app := newTestApp(t, false)
	index := serveWeb(app, "/", nil)
	etag := index.Header().Get("ETag")
	if !regexp.MustCompile(`^"[a-f0-9]{32}"$`).MatchString(etag) {
		t.Fatalf("index ETag=%q, want strong ETag", etag)
	}
	if other := serveWeb(app, "/index.html", nil).Header().Get("ETag"); other != etag {
		t.Fatalf("/ and /index.html ETags differ: %q vs %q", etag, other)
	}
	for _, target := range []string{"/", "/index.html", "/app.js", "/styles.css"} {
		first := serveWeb(app, target, nil)
		if first.Header().Get("ETag") == "" {
			t.Fatalf("%s: no ETag", target)
		}
		again := serveWeb(app, target, map[string]string{"If-None-Match": first.Header().Get("ETag")})
		if again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("ETag") != first.Header().Get("ETag") {
			t.Fatalf("%s: If-None-Match status=%d body=%d", target, again.Code, again.Body.Len())
		}
		modified := serveWeb(app, target, map[string]string{"If-Modified-Since": first.Header().Get("Last-Modified")})
		if modified.Code != http.StatusNotModified {
			t.Fatalf("%s: If-Modified-Since status=%d", target, modified.Code)
		}
		stale := serveWeb(app, target, map[string]string{"If-None-Match": `"stale"`})
		if stale.Code != http.StatusOK {
			t.Fatalf("%s: stale validator status=%d", target, stale.Code)
		}
	}
	// A bare (unversioned) script must revalidate; only the content-addressed URL is immutable.
	if cache := serveWeb(app, "/app.js", nil).Header().Get("Cache-Control"); cache != "no-cache" {
		t.Fatalf("unversioned app.js cache-control=%q", cache)
	}
	// A URL carrying a version that no longer matches the content must not be pinned in browsers.
	stale := serveWeb(app, "/app.js?v="+strings.Repeat("0", 64), nil)
	if cache := stale.Header().Get("Cache-Control"); stale.Code != http.StatusOK || cache != "no-cache" {
		t.Fatalf("stale ?v= status=%d cache-control=%q", stale.Code, cache)
	}
}

func TestWebGzipNegotiation(t *testing.T) {
	app := newTestApp(t, false)
	want, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	identity := serveWeb(app, "/app.js", nil)
	if identity.Header().Get("Content-Encoding") != "" || identity.Header().Get("Vary") != "Accept-Encoding" || identity.Body.String() != string(want) {
		t.Fatalf("identity: encoding=%q vary=%q", identity.Header().Get("Content-Encoding"), identity.Header().Get("Vary"))
	}
	for _, accept := range []string{"gzip", "br, gzip;q=0.8", "deflate, gzip", "*", "GZIP"} {
		res := serveWeb(app, "/app.js", map[string]string{"Accept-Encoding": accept})
		if res.Header().Get("Content-Encoding") != "gzip" || res.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatalf("Accept-Encoding %q: encoding=%q vary=%q", accept, res.Header().Get("Content-Encoding"), res.Header().Get("Vary"))
		}
		if res.Header().Get("ETag") == identity.Header().Get("ETag") || !strings.HasSuffix(res.Header().Get("ETag"), `-gzip"`) {
			t.Fatalf("Accept-Encoding %q: gzip ETag=%q must differ from identity %q", accept, res.Header().Get("ETag"), identity.Header().Get("ETag"))
		}
		if res.Body.Len() >= len(want)/2 {
			t.Fatalf("Accept-Encoding %q: gzip body is %d bytes for %d", accept, res.Body.Len(), len(want))
		}
		etag := res.Header().Get("ETag")
		reader, err := gzip.NewReader(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(decoded, want) {
			t.Fatalf("Accept-Encoding %q: gzip body does not decode to the embedded file (err=%v)", accept, err)
		}
		again := serveWeb(app, "/app.js", map[string]string{"Accept-Encoding": accept, "If-None-Match": etag})
		if again.Code != http.StatusNotModified {
			t.Fatalf("Accept-Encoding %q: gzip validator status=%d", accept, again.Code)
		}
		// An identity validator must not match the gzip representation.
		if mismatch := serveWeb(app, "/app.js", map[string]string{"Accept-Encoding": accept, "If-None-Match": identity.Header().Get("ETag")}); mismatch.Code != http.StatusOK {
			t.Fatalf("Accept-Encoding %q: identity validator matched gzip representation, status=%d", accept, mismatch.Code)
		}
	}
	for _, accept := range []string{"br", "identity", "gzip;q=0", "gzip; q=0.0", "deflate"} {
		if res := serveWeb(app, "/app.js", map[string]string{"Accept-Encoding": accept}); res.Header().Get("Content-Encoding") != "" || res.Body.String() != string(want) {
			t.Fatalf("Accept-Encoding %q: got encoding %q", accept, res.Header().Get("Content-Encoding"))
		}
	}
	for _, target := range []string{"/", "/styles.css"} {
		if res := serveWeb(app, target, map[string]string{"Accept-Encoding": "gzip"}); res.Header().Get("Content-Encoding") != "gzip" || res.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatalf("%s: encoding=%q vary=%q", target, res.Header().Get("Content-Encoding"), res.Header().Get("Vary"))
		}
	}
}

func TestWebHashedImagesAreImmutableAndNotRecompressed(t *testing.T) {
	assets, err := fs.Glob(webRoot, "assets/*")
	if err != nil || len(assets) == 0 {
		t.Fatalf("assets=%v err=%v", assets, err)
	}
	app := newTestApp(t, false)
	hashed := regexp.MustCompile(`\.([a-f0-9]{10})\.(?:avif|webp)$`)
	for _, name := range assets {
		match := hashed.FindStringSubmatch(name)
		if match == nil {
			t.Fatalf("%s: embedded asset is not a content-hashed AVIF or WebP file (large originals must stay out of web/)", name)
		}
		want, err := fs.ReadFile(webRoot, name)
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256(want); hex.EncodeToString(sum[:])[:10] != match[1] {
			t.Fatalf("%s: file name hash does not match content (rerun scripts/build-login-art.cjs)", name)
		}
		if len(want) > 250*1000 {
			t.Fatalf("%s: %d bytes is over the 250 kB image budget", name, len(want))
		}
		res := serveWeb(app, "/"+name, map[string]string{"Accept-Encoding": "gzip, br"})
		if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || res.Header().Get("ETag") == "" {
			t.Fatalf("%s: status=%d headers=%v", name, res.Code, res.Header())
		}
		if res.Header().Get("Content-Encoding") != "" || res.Header().Get("Vary") != "" || !bytes.Equal(res.Body.Bytes(), want) {
			t.Fatalf("%s: image was recompressed or altered (encoding=%q vary=%q)", name, res.Header().Get("Content-Encoding"), res.Header().Get("Vary"))
		}
		wantType := "image/" + path.Ext(name)[1:]
		if res.Header().Get("Content-Type") != wantType {
			t.Fatalf("%s: content-type=%q want %q", name, res.Header().Get("Content-Type"), wantType)
		}
	}
	// Every artwork URL referenced by the shell or stylesheet must exist, so the login scene cannot ship a 404.
	for _, name := range []string{"index.html", "styles.css"} {
		source, err := fs.ReadFile(webRoot, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range regexp.MustCompile(`/assets/[A-Za-z0-9._-]+`).FindAllString(string(source), -1) {
			if res := serveWeb(app, match, nil); res.Code != http.StatusOK {
				t.Fatalf("%s references %s: status=%d", name, match, res.Code)
			}
		}
	}
}

func TestWebHashedNameMustMatchContent(t *testing.T) {
	body := []byte("pixels")
	sum := sha256.Sum256(body)
	good := "assets/hero." + hex.EncodeToString(sum[:])[:10] + ".webp"
	root := fstest.MapFS{
		"index.html":                  {Data: []byte(`<script src="/app.js"></script><link href="/styles.css">`)},
		"app.js":                      {Data: []byte("a")},
		"styles.css":                  {Data: []byte("b")},
		good:                          {Data: body},
		"assets/hero.0123456789.webp": {Data: body},
		"assets/plain.webp":           {Data: body},
	}
	handler := newWebHandler(root)
	for name, want := range map[string]string{good: "public, max-age=31536000, immutable", "assets/hero.0123456789.webp": "no-cache", "assets/plain.webp": "no-cache"} {
		if res := serveWeb(handler, "/"+name, nil); res.Code != http.StatusOK || res.Header().Get("Cache-Control") != want {
			t.Fatalf("%s: status=%d cache-control=%q want %q", name, res.Code, res.Header().Get("Cache-Control"), want)
		}
	}
}

func TestWebHandlerRejectsOtherRequests(t *testing.T) {
	app := newTestApp(t, false)
	for _, target := range []string{"/missing.js", "/assets/", "/assets", "/assets/missing.avif", "/../main.go", "/%2e%2e/main.go"} {
		if res := serveWeb(app, target, nil); res.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d, want 404", target, res.Code)
		}
	}
	for _, target := range []string{"/", "/app.js"} {
		res := httptest.NewRecorder()
		app.ServeHTTP(res, httptest.NewRequest(http.MethodPost, target, nil))
		if res.Code != http.StatusMethodNotAllowed || res.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("POST %s: status=%d allow=%q", target, res.Code, res.Header().Get("Allow"))
		}
	}
	// API responses keep no-store and are never touched by the static handler.
	res := httptest.NewRecorder()
	app.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/auth", nil))
	if res.Header().Get("Cache-Control") != "no-store" || res.Header().Get("ETag") != "" || res.Header().Get("Content-Encoding") != "" {
		t.Fatalf("/api/auth headers=%v", res.Header())
	}
}
