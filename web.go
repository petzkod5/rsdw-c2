package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cacheRevalidate = "no-cache"
	cacheImmutable  = "public, max-age=31536000, immutable"
)

// hashedAsset matches build-generated names such as assets/login-hero-d-1920.d4e67eed17.avif, where the
// hash is the first ten hex digits of the file's SHA-256.
var hashedAsset = regexp.MustCompile(`\.([0-9a-f]{10})\.[a-z0-9]+$`)

// contentTypes pins the types the console serves so they do not depend on the host's MIME database.
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".svg":  "image/svg+xml",
	".json": "application/json",
	".txt":  "text/plain; charset=utf-8",
	".avif": "image/avif",
	".webp": "image/webp",
	".png":  "image/png",
	".ico":  "image/x-icon",
}

// compressible lists the types worth gzipping; images are already compressed.
var compressible = map[string]bool{".html": true, ".js": true, ".css": true, ".svg": true, ".json": true, ".txt": true}

type webEntry struct {
	identity    []byte
	gzipped     []byte // nil unless gzip is allowed for this type and actually shrinks the body
	etag        string // strong ETag of the identity representation
	contentType string
	sha         string // full hex SHA-256 of the identity bytes
	hashed      bool   // file name carries a hash that matches the content
}

func newWebEntry(name string, body []byte) *webEntry {
	sum := sha256.Sum256(body)
	entry := &webEntry{identity: body, sha: hex.EncodeToString(sum[:])}
	entry.etag = `"` + entry.sha[:32] + `"`
	ext := strings.ToLower(path.Ext(name))
	if entry.contentType = contentTypes[ext]; entry.contentType == "" {
		if entry.contentType = mime.TypeByExtension(ext); entry.contentType == "" {
			entry.contentType = "application/octet-stream"
		}
	}
	if match := hashedAsset.FindStringSubmatch(name); match != nil && strings.HasPrefix(name, "assets/") {
		entry.hashed = match[1] == entry.sha[:10]
	}
	if compressible[ext] {
		var buffer bytes.Buffer
		writer, _ := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
		writer.Write(body)
		writer.Close()
		if buffer.Len() < len(body) {
			entry.gzipped = buffer.Bytes()
		}
	}
	return entry
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip, explicitly or through "*".
func acceptsGzip(header string) bool {
	allowed := false
	for _, part := range strings.Split(header, ",") {
		token, params, _ := strings.Cut(part, ";")
		token = strings.ToLower(strings.TrimSpace(token))
		if token != "gzip" && token != "*" {
			continue
		}
		accepted := true
		if key, value, found := strings.Cut(strings.TrimSpace(params), "="); found && strings.EqualFold(strings.TrimSpace(key), "q") {
			quality, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			accepted = err == nil && quality > 0
		}
		if token == "gzip" {
			return accepted
		}
		allowed = accepted
	}
	return allowed
}

// newWebHandler serves the embedded console. index.html is rendered once with content-hashed app.js and
// styles.css URLs. Responses carry validators so conditional requests work on the embedded FS (whose files
// have no modification time): HTML and unversioned files revalidate (no-cache + strong ETag); files whose
// URL is content-addressed (?v=<sha256> on app.js and styles.css, hashed names under assets/) are immutable.
// Text types are gzipped once and negotiated with Accept-Encoding.
func newWebHandler(root fs.FS) http.Handler {
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		panic(err)
	}
	html := string(index)
	versions := map[string]string{}
	for _, name := range []string{"app.js", "styles.css"} {
		content, err := fs.ReadFile(root, name)
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(content)
		versions[name] = fmt.Sprintf("%x", sum)
		html = strings.ReplaceAll(html, `"/`+name+`"`, fmt.Sprintf(`"/%s?v=%s"`, name, versions[name]))
	}
	indexEntry := newWebEntry("index.html", []byte(html))
	var entries sync.Map
	lookup := func(name string) *webEntry {
		if cached, ok := entries.Load(name); ok {
			return cached.(*webEntry)
		}
		if !fs.ValidPath(name) {
			return nil
		}
		body, err := fs.ReadFile(root, name)
		if err != nil {
			return nil
		}
		cached, _ := entries.LoadOrStore(name, newWebEntry(name, body))
		return cached.(*webEntry)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		entry, cache := indexEntry, cacheRevalidate
		if name != "" && name != "index.html" {
			if entry = lookup(name); entry == nil {
				http.NotFound(w, r)
				return
			}
			if version, ok := versions[name]; (ok && r.URL.Query().Get("v") == version) || entry.hashed {
				cache = cacheImmutable
			}
		}
		body, etag := entry.identity, entry.etag
		header := w.Header()
		header.Set("Content-Type", entry.contentType)
		header.Set("Cache-Control", cache)
		if entry.gzipped != nil {
			header.Add("Vary", "Accept-Encoding")
			if acceptsGzip(r.Header.Get("Accept-Encoding")) {
				body, etag = entry.gzipped, `"`+entry.sha[:32]+`-gzip"`
				header.Set("Content-Encoding", "gzip")
			}
		}
		header.Set("ETag", etag)
		// The embedded FS has no modification time, so there is deliberately no Last-Modified: process start would
		// change on every deploy even when the bytes did not, and the strong ETag already validates content.
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	})
}
