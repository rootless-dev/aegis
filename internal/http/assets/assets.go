// Package assets serves the static files embedded in the binary, addressed by a
// fingerprint of their content: a new build produces a new path, which is what
// makes a year-long cache safe.
package assets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// Named rather than spelled out at each call site, so the boot check and the
// templates cannot drift apart.
const (
	Stylesheet = "css/app.css"
	Favicon    = "favicon.svg"
)

const (
	prefix      = "/assets"
	faviconPath = "/favicon.ico"

	immutableCache = "public, max-age=31536000, immutable"
	// Fixed path: a deploy cannot invalidate it, so it must not claim a year.
	faviconCache = "public, max-age=3600"

	// /favicon.ico is served as image/svg+xml, and an SVG navigated to directly
	// is a document that can carry script — harmless for the icon committed
	// here, wrong the day a realm logo is served from the same code.
	//
	// frame-ancestors is spelled out because it is one of the directives that
	// does not fall back to default-src, and these routes bypass
	// SecurityHeaders, so nothing else here denies framing.
	assetPolicy = "default-src 'none'; frame-ancestors 'none'"
)

// Router is the slice of the HTTP router this package needs. Same shape as the
// one in internal/infra/health.
type Router interface {
	Get(pattern string, handler http.HandlerFunc)
	Head(pattern string, handler http.HandlerFunc)
}

type asset struct {
	content []byte
	hash    string
	etag    string
}

type Server struct {
	files map[string]asset
	urls  map[string]string
}

// New reads the whole tree into memory. The files are embedded and already
// resident, so a second copy buys a hash computed once instead of per request.
func New(fsys fs.FS) (*Server, error) {
	server := &Server{
		files: make(map[string]asset),
		urls:  make(map[string]string),
	}

	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}

		sum := sha256.Sum256(content)
		// Half the digest: far beyond accidental collision, and readable in a
		// log line.
		hash := hex.EncodeToString(sum[:])[:16]

		server.files[name] = asset{content: content, hash: hash, etag: `"` + hash + `"`}
		server.urls[name] = prefix + "/" + hash + "/" + name

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("assets: walking the asset tree: %w", err)
	}

	return server, nil
}

// URL fails rather than returning a broken URL, which turns a typo in a
// template into a test failure instead of a missing stylesheet in a browser.
func (s *Server) URL(logical string) (string, error) {
	url, ok := s.urls[logical]
	if !ok {
		return "", fmt.Errorf("assets: no asset named %q", logical)
	}

	return url, nil
}

// Verify reports which of the required assets are absent. It works over the
// filesystem so the missing-stylesheet case can be tested without removing a
// file from an embed fixed at compile time.
func (s *Server) Verify(required ...string) error {
	var missing []string

	for _, logical := range required {
		if _, ok := s.files[logical]; !ok {
			missing = append(missing, logical)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf("assets: missing %s", strings.Join(missing, ", "))
}

// Mount registers the asset routes. HEAD sits next to each GET because the
// router matches by method, and a 405 to a cache validating an asset would make
// an immutable URL look broken. ServeContent writes as usual; net/http discards
// the body for HEAD.
func (s *Server) Mount(router Router) {
	router.Get(prefix+"/*", s.serve)
	router.Head(prefix+"/*", s.serve)

	router.Get(faviconPath, s.favicon)
	router.Head(faviconPath, s.favicon)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	hash, logical, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, prefix+"/"), "/")
	if !ok {
		http.NotFound(w, r)

		return
	}

	file, found := s.files[logical]
	// Verified, not merely stripped: if any string in that position served the
	// file, the year-long caching promise would be a lie.
	if !found || file.hash != hash {
		http.NotFound(w, r)

		return
	}

	s.write(w, r, logical, file, immutableCache)
}

func (s *Server) favicon(w http.ResponseWriter, r *http.Request) {
	file, found := s.files[Favicon]
	if !found {
		http.NotFound(w, r)

		return
	}

	// Its real name, so the content type comes out as SVG even though browsers
	// ask for it at /favicon.ico.
	s.write(w, r, Favicon, file, faviconCache)
}

func (s *Server) write(w http.ResponseWriter, r *http.Request, name string, file asset, cache string) {
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("ETag", file.etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", assetPolicy)

	// The zero time disables the modification-time comparison; ServeContent
	// answers If-None-Match from the ETag above.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(file.content))
}
