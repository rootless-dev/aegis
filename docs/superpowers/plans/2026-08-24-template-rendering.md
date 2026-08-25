# Template Rendering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give aegis an HTML surface — an embedded template renderer, a fingerprinted asset server, strict security headers, and a landing page at `/`.

**Architecture:** Four new packages with one job each. `internal/templates` owns two `embed.FS` and nothing else. `internal/http/render` parses and writes HTML, taking its filesystem as a parameter. `internal/http/assets` fingerprints and serves static files, also over an injected filesystem. `internal/handler/page` holds the handlers. The router grows a third surface beside the probes and the JSON group, with its own recoverer so a panic on a page answers HTML rather than JSON.

**Tech Stack:** Go 1.26, `html/template` (stdlib), chi v5, Tailwind CSS v4 via the standalone CLI (no Node), `phuslu/log`.

**Spec:** `docs/superpowers/specs/2026-08-24-template-rendering-design.md`

## Global Constraints

- **No `git commit` in any task.** Carlos authorises commits explicitly and separately; authorising implementation does not authorise committing. Leave every change in the working tree. This overrides the writing-plans skill's own instruction to commit per task.
- **Branch:** `feat/template-rendering`. Do not create another branch, do not switch branches.
- **Comments in English**, and only where the code is genuinely hard to follow — matching the existing style, which explains *why* and never *what*.
- **`CGO_ENABLED=0`.** Nothing may introduce a cgo dependency.
- **No new Go module dependencies.** Everything here is stdlib plus chi and `phuslu/log`, which are already required.
- **No Node, no npm, no `package.json`.** Tailwind runs from its standalone binary.
- **CSP has no `unsafe-inline` and no `unsafe-eval`.** Therefore: no `<style>` blocks, no `onclick=` or any inline handler, no inline `<script>` in any template.
- **The CSP string in tests is written out by hand**, never imported from the constant it verifies.
- **Every rendered page sends** `Content-Type: text/html; charset=utf-8` and `Cache-Control: no-store`.
- **`response.WriteJSON` and the JSON writers are untouched.**
- Run `make fmt` before considering a task done; `gofmt` compliance is enforced by CI.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/configs/csp.go` | The `csp` section: one `Enabled bool`, its default, its no-op `Validate`. |
| `internal/http/assets/assets.go` | Walk an `fs.FS`, hash each file, resolve logical paths to fingerprinted URLs, serve them, and report missing required assets. |
| `internal/http/render/render.go` | Compose layouts with pages, execute into a buffer, write the response. Plus the hard-coded fallback document. |
| `internal/http/middleware/security_headers.go` | The CSP constant and the four-header middleware. |
| `internal/http/middleware/recoverer.go` | Grows an `ErrorWriter` parameter so each surface answers in its own format. |
| `internal/http/response/response.go` | Gains `ServerError`, an `ErrorWriter` adapter over the existing `WriteServerError`. |
| `internal/templates/templates.go` | Two `//go:embed` declarations and their accessors. No logic. |
| `internal/templates/layouts/base.gohtml` | The document shell. |
| `internal/templates/pages/landing.gohtml` | The landing page body. |
| `internal/templates/pages/error.gohtml` | The 404 and 500 body. |
| `internal/templates/tailwind/input.css` | Tailwind's input. Outside `assets/` so it is never served. |
| `internal/templates/assets/favicon.svg` | The committed file that keeps the asset embed non-empty. |
| `internal/handler/page/page.go` | Landing, 404, 405 and the panic writer, each falling back when the error template itself fails. |
| `internal/application/wiring.go` | `setWeb`: build the asset server, verify the stylesheet, build the renderer. |
| `internal/application/router.go` | Third surface, shared chain extracted, assets and favicon mounted, root 404/405. |

Nine tasks. Tasks 1 through 4 are self-contained packages testable in isolation; task 5 brings the real files; task 6 makes the stylesheet exist; tasks 7 and 8 assemble; task 9 documents.

---

### Task 1: The `csp` configuration section

**Files:**
- Create: `internal/configs/csp.go`
- Create: `internal/configs/csp_test.go`
- Modify: `internal/configs/application.go` (struct field, `Default()`, `sections()`)
- Modify: `internal/infra/configbuilder/env_source.go` (`applyEnv` call and `applyCSP`)
- Modify: `aegis.example.yaml`, `.env.example`, `docs/configuration.md`

**Interfaces:**
- Consumes: nothing.
- Produces: `configs.CSP` with field `Enabled bool`; `cfg.CSP` on `configs.Application`.

- [ ] **Step 1: Write the failing test**

Create `internal/configs/csp_test.go`:

```go
package configs_test

import (
	"testing"

	"github.com/rootless-dev/aegis/internal/configs"
)

func TestCSPDefaultsToEnabled(t *testing.T) {
	cfg := configs.Default()

	if cfg.CSP == nil {
		t.Fatal("csp section is missing from the defaults")
	}

	if !cfg.CSP.Enabled {
		t.Error("csp must default to enabled, including under the prod profile")
	}
}

func TestCSPValidateAcceptsBothStates(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		cfg := &configs.CSP{Enabled: enabled}

		if err := cfg.Validate(); err != nil {
			t.Errorf("enabled=%v: unexpected error: %v", enabled, err)
		}
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails**

Run: `go test ./internal/configs/ -run TestCSP -v`
Expected: compile failure — `cfg.CSP undefined` and `undefined: configs.CSP`.

- [ ] **Step 3: Create the section**

Create `internal/configs/csp.go`:

```go
package configs

// CSP declares whether the Content-Security-Policy header is sent. The policy
// itself is not configurable, for the same reason the TLS cipher suites are
// not: a policy pinned in configuration ages into the weakest thing this
// service still allows, and an operator able to weaken the policy of an
// identity provider from a file is a downgrade attack with a config file.
//
// The other security headers have no switch at all — no deployment needs them
// off, so there is no knob to get wrong.
type CSP struct {
	// Enabled exists so a developer chasing something can turn the policy off
	// locally. It defaults on in every profile.
	Enabled bool `yaml:"enabled"`
}

func defaultCSP() *CSP {
	return &CSP{
		Enabled: true,
	}
}

func (cfg *CSP) Validate() error {
	return nil
}
```

- [ ] **Step 4: Register the section in the three places**

In `internal/configs/application.go`, add the field after `HSTS`:

```go
	HSTS       *HSTS       `yaml:"hsts"`
	CSP        *CSP        `yaml:"csp"`
	Database   *Database   `yaml:"database"`
```

In `Default()`, after `HSTS: defaultHSTS(),`:

```go
		CSP:        defaultCSP(),
```

In `sections()`, after the `hsts` entry:

```go
		{"csp", cfg.CSP == nil, func() error { return cfg.CSP.Validate() }},
```

- [ ] **Step 5: Run the test and confirm it passes**

Run: `go test ./internal/configs/ -run TestCSP -v`
Expected: PASS, both tests.

- [ ] **Step 6: Wire the environment variable**

In `internal/infra/configbuilder/env_source.go`, add the call in `applyEnv` right after `applyHSTS(cfg.HSTS)`:

```go
	applyCSP(cfg.CSP)
```

And the function itself, placed after `applyHSTS` so the file order still matches the file order of the configuration:

```go
func applyCSP(cfg *configs.CSP) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.Enabled, "CSP_ENABLED")
}
```

- [ ] **Step 7: Confirm the whole config package still passes**

Run: `go test ./internal/configs/... ./internal/infra/configbuilder/... -v`
Expected: PASS. If a fixture in `configbuilder` asserts a full configuration, it needs the new section — update it.

- [ ] **Step 8: Document the section**

In `aegis.example.yaml`, after the `hsts` block:

```yaml
csp:
  # The Content-Security-Policy sent with every HTML page. The policy itself is
  # not configurable: a policy pinned in configuration ages into the weakest
  # thing this service still allows. This switch exists for local debugging and
  # defaults on in every profile.
  #
  # The other security headers — nosniff, referrer policy, frame options — are
  # always sent and have no switch.
  enabled: true
```

In `.env.example`, beside the HSTS variables:

```
CSP_ENABLED=true
```

In `docs/configuration.md`, add a `csp` section following the shape the `hsts` one uses in that file.

- [ ] **Step 9: Format and verify**

Run: `make fmt && go build ./... && go test ./internal/configs/... ./internal/infra/configbuilder/...`
Expected: clean build, tests pass.

---

### Task 2: The asset server

**Files:**
- Create: `internal/http/assets/assets.go`
- Create: `internal/http/assets/assets_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `assets.New(fsys fs.FS) (*assets.Server, error)`
  - `(*Server) URL(logical string) (string, error)`
  - `(*Server) Verify(required ...string) error`
  - `(*Server) Mount(router assets.Router)`
  - `assets.Router` interface with `Get(pattern string, handler http.HandlerFunc)`
  - `assets.Stylesheet` constant, value `"css/app.css"`
  - `assets.Favicon` constant, value `"favicon.svg"`

- [ ] **Step 1: Write the failing tests**

Create `internal/http/assets/assets_test.go`:

```go
package assets_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rootless-dev/aegis/internal/http/assets"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"css/app.css":  &fstest.MapFile{Data: []byte("body{color:red}")},
		"favicon.svg":  &fstest.MapFile{Data: []byte("<svg/>")},
	}
}

func newServer(t *testing.T, files fstest.MapFS) *assets.Server {
	t.Helper()

	server, err := assets.New(files)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return server
}

func TestURLIsStableForTheSameContent(t *testing.T) {
	first := newServer(t, testFS())
	second := newServer(t, testFS())

	firstURL, err := first.URL(assets.Stylesheet)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}

	secondURL, err := second.URL(assets.Stylesheet)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}

	if firstURL != secondURL {
		t.Errorf("same content produced different urls: %q and %q", firstURL, secondURL)
	}

	if !strings.HasPrefix(firstURL, "/assets/") || !strings.HasSuffix(firstURL, "/css/app.css") {
		t.Errorf("unexpected url shape: %q", firstURL)
	}
}

func TestURLChangesWithContent(t *testing.T) {
	original := newServer(t, testFS())

	changed := testFS()
	changed["css/app.css"] = &fstest.MapFile{Data: []byte("body{color:blue}")}

	before, _ := original.URL(assets.Stylesheet)
	after, _ := newServer(t, changed).URL(assets.Stylesheet)

	if before == after {
		t.Error("changed content must produce a different url, or a deploy cannot invalidate the cache")
	}
}

func TestURLRejectsAnUnknownPath(t *testing.T) {
	server := newServer(t, testFS())

	if _, err := server.URL("css/missing.css"); err == nil {
		t.Error("an unknown logical path must fail rather than emit a broken url")
	}
}

func TestVerifyReportsWhatIsMissing(t *testing.T) {
	files := testFS()
	delete(files, "css/app.css")

	err := newServer(t, files).Verify(assets.Stylesheet)
	if err == nil {
		t.Fatal("Verify must fail when a required asset is absent")
	}

	if !strings.Contains(err.Error(), assets.Stylesheet) {
		t.Errorf("the error must name the missing asset, got %q", err)
	}
}

func TestVerifyPassesWhenEverythingIsPresent(t *testing.T) {
	if err := newServer(t, testFS()).Verify(assets.Stylesheet, assets.Favicon); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// router is the smallest thing satisfying assets.Router, so the test does not
// depend on the router the application happens to use.
type router struct{ routes map[string]http.HandlerFunc }

func (r *router) Get(pattern string, handler http.HandlerFunc) {
	r.routes[pattern] = handler
}

func mounted(t *testing.T, server *assets.Server) *router {
	t.Helper()

	mux := &router{routes: map[string]http.HandlerFunc{}}
	server.Mount(mux)

	return mux
}

func TestServeReturnsTheFileWithImmutableCaching(t *testing.T) {
	server := newServer(t, testFS())
	mux := mounted(t, server)

	url, _ := server.URL(assets.Stylesheet)

	recorder := httptest.NewRecorder()
	handler := mux.routes["/assets/*"]
	if handler == nil {
		t.Fatal("Mount did not register the asset route")
	}

	handler(recorder, httptest.NewRequest(http.MethodGet, url, nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	if got := recorder.Body.String(); got != "body{color:red}" {
		t.Errorf("body = %q", got)
	}

	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", got)
	}

	if recorder.Header().Get("ETag") == "" {
		t.Error("ETag must be set, it is what makes If-None-Match work")
	}

	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestServeRejectsAWrongHash(t *testing.T) {
	server := newServer(t, testFS())
	mux := mounted(t, server)

	recorder := httptest.NewRecorder()
	mux.routes["/assets/*"](recorder, httptest.NewRequest(http.MethodGet, "/assets/deadbeef/css/app.css", nil))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: without the check any string serves the file and immutable caching is a lie", recorder.Code)
	}
}

func TestServeAnswersNotModified(t *testing.T) {
	server := newServer(t, testFS())
	mux := mounted(t, server)

	url, _ := server.URL(assets.Stylesheet)

	first := httptest.NewRecorder()
	mux.routes["/assets/*"](first, httptest.NewRequest(http.MethodGet, url, nil))

	request := httptest.NewRequest(http.MethodGet, url, nil)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))

	second := httptest.NewRecorder()
	mux.routes["/assets/*"](second, request)

	if second.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", second.Code)
	}
}

func TestFaviconIsServedFromTheFixedPath(t *testing.T) {
	server := newServer(t, testFS())
	mux := mounted(t, server)

	handler := mux.routes["/favicon.ico"]
	if handler == nil {
		t.Fatal("Mount did not register /favicon.ico; browsers request it whatever the document says")
	}

	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	// The path is fixed, so it cannot be invalidated by a deploy and must not
	// claim to be immutable.
	if strings.Contains(recorder.Header().Get("Cache-Control"), "immutable") {
		t.Error("the favicon path is fixed and must not be cached as immutable")
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./internal/http/assets/ -v`
Expected: build failure — the package does not exist.

- [ ] **Step 3: Implement the package**

Create `internal/http/assets/assets.go`:

```go
// Package assets serves the static files embedded in the binary, addressed by
// a fingerprint of their content.
//
// The fingerprint is what allows the responses to be cached for a year: a new
// build produces a new path, so nothing has to be invalidated and no stale
// stylesheet can survive a deploy.
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

// Logical paths every deployment is expected to carry. They are named rather
// than spelled out at each call site so the boot check and the templates
// cannot drift apart.
const (
	Stylesheet = "css/app.css"
	Favicon    = "favicon.svg"
)

const (
	prefix      = "/assets"
	faviconPath = "/favicon.ico"

	immutableCache = "public, max-age=31536000, immutable"
	// The favicon answers on a fixed path, so a deploy cannot invalidate it and
	// it must not claim a year.
	faviconCache = "public, max-age=3600"
)

// Router is the slice of the HTTP router this package needs, declared here so
// the package carries no dependency on the router in use. Same shape as the
// one in internal/infra/health.
type Router interface {
	Get(pattern string, handler http.HandlerFunc)
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

// New reads the whole tree into memory. These are the files embedded in the
// binary, already resident, so holding them costs nothing beyond a second copy
// and buys a hash computed once instead of per request.
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
		// Half the digest: still far beyond collision by accident, and it keeps
		// the URL readable in a log line.
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

// URL resolves a logical path to the fingerprinted one. It fails rather than
// returning a broken URL, which is what turns a typo in a template into a test
// failure instead of a missing stylesheet in a browser.
func (s *Server) URL(logical string) (string, error) {
	url, ok := s.urls[logical]
	if !ok {
		return "", fmt.Errorf("assets: no asset named %q", logical)
	}

	return url, nil
}

// Verify reports which of the required assets are absent. It is a plain
// function over the filesystem rather than a check inside the boot, so the
// missing-stylesheet case can be tested without removing a file from an embed
// that was fixed at compile time.
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

func (s *Server) Mount(router Router) {
	router.Get(prefix+"/*", s.serve)
	router.Get(faviconPath, s.favicon)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	hash, logical, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, prefix+"/"), "/")
	if !ok {
		http.NotFound(w, r)

		return
	}

	file, found := s.files[logical]
	// The hash is verified, not merely stripped: if any string in that position
	// served the file, the year-long caching promise would be a lie.
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

	// Served under its real name so the content type comes out as SVG, even
	// though browsers ask for it at /favicon.ico.
	s.write(w, r, Favicon, file, faviconCache)
}

func (s *Server) write(w http.ResponseWriter, r *http.Request, name string, file asset, cache string) {
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("ETag", file.etag)
	// It matters most here: this is what stops a browser from deciding a
	// stylesheet is really a script.
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// The zero time disables the modification-time comparison; the ETag set
	// above is what ServeContent uses to answer If-None-Match.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(file.content))
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/http/assets/ -v`
Expected: PASS, all nine tests.

- [ ] **Step 5: Run with the race detector and the security scanner**

Run: `go test -race ./internal/http/assets/ && go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 -fmt=text ./internal/http/assets/...`
Expected: tests pass, gosec reports no issue. If gosec flags the `sha256` use, that is a false positive for a content fingerprint — it is not a security hash here — and warrants a `#nosec` with that reason, not a change of algorithm.

- [ ] **Step 6: Format**

Run: `make fmt && go vet ./internal/http/assets/...`

---

### Task 3: The renderer

**Files:**
- Create: `internal/http/render/render.go`
- Create: `internal/http/render/render_test.go`

**Interfaces:**
- Consumes: nothing (the asset function arrives as a `template.FuncMap` entry supplied by the caller).
- Produces:
  - `render.New(opts render.Options) (*render.Renderer, error)` with `Options{Templates fs.FS; Funcs template.FuncMap}`
  - `(*Renderer) Page(w http.ResponseWriter, status int, name string, data any) error`
  - `render.Fallback(w http.ResponseWriter, status int)`

- [ ] **Step 1: Write the failing tests**

Create `internal/http/render/render_test.go`:

```go
package render_test

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rootless-dev/aegis/internal/http/render"
)

func templateFS() fstest.MapFS {
	return fstest.MapFS{
		"layouts/base.gohtml": &fstest.MapFile{Data: []byte(
			`{{define "base"}}<!doctype html><html><head><title>{{.Title}}</title></head><body>{{template "content" .}}</body></html>{{end}}`,
		)},
		"pages/landing.gohtml": &fstest.MapFile{Data: []byte(
			`{{define "content"}}<h1>{{.Title}}</h1><a href="{{.Link}}">go</a>{{end}}`,
		)},
	}
}

type model struct {
	Title string
	Link  string
}

func newRenderer(t *testing.T, files fstest.MapFS) *render.Renderer {
	t.Helper()

	renderer, err := render.New(render.Options{Templates: files})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return renderer
}

func TestPageComposesTheLayout(t *testing.T) {
	renderer := newRenderer(t, templateFS())
	recorder := httptest.NewRecorder()

	if err := renderer.Page(recorder, http.StatusOK, "landing", model{Title: "Aegis"}); err != nil {
		t.Fatalf("Page: %v", err)
	}

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	body := recorder.Body.String()
	for _, want := range []string{"<!doctype html>", "<title>Aegis</title>", "<h1>Aegis</h1>"} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, body)
		}
	}
}

func TestPageSetsContentTypeAndNoStore(t *testing.T) {
	renderer := newRenderer(t, templateFS())
	recorder := httptest.NewRecorder()

	_ = renderer.Page(recorder, http.StatusOK, "landing", model{Title: "Aegis"})

	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}

	// Every page, not only the sensitive ones: an allowlist of cacheable pages
	// is a decision someone eventually gets wrong, and a shared proxy caching a
	// login form hands it to the next visitor.
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// This is the property html/template was chosen for: escaping that depends on
// where in the document the value lands.
func TestPageEscapesContextually(t *testing.T) {
	renderer := newRenderer(t, templateFS())
	recorder := httptest.NewRecorder()

	_ = renderer.Page(recorder, http.StatusOK, "landing", model{
		Title: `<script>alert(1)</script>`,
		Link:  `javascript:alert(1)`,
	})

	body := recorder.Body.String()

	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("element text was not escaped")
	}

	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected escaped text in the body:\n%s", body)
	}

	if strings.Contains(body, `href="javascript:alert(1)"`) {
		t.Error("a javascript: url survived into an href; url context was not applied")
	}
}

func TestPageRejectsAnUnknownName(t *testing.T) {
	renderer := newRenderer(t, templateFS())

	if err := renderer.Page(httptest.NewRecorder(), http.StatusOK, "nope", nil); err == nil {
		t.Error("an unknown page name must return an error")
	}
}

func TestNewFailsOnABrokenTemplate(t *testing.T) {
	files := templateFS()
	files["pages/broken.gohtml"] = &fstest.MapFile{Data: []byte(`{{define "content"}}{{ .Unclosed `)}

	if _, err := render.New(render.Options{Templates: files}); err == nil {
		t.Error("a broken template must fail the boot, not the first request that touches it")
	}
}

func TestNewToleratesAMissingPartialsDirectory(t *testing.T) {
	// partials/ does not exist in the fixture. ParseFS errors on a pattern that
	// matches nothing, so the renderer has to glob before it parses.
	if _, err := render.New(render.Options{Templates: templateFS()}); err != nil {
		t.Errorf("unexpected error with no partials directory: %v", err)
	}
}

func TestFuncsReachTheTemplates(t *testing.T) {
	files := templateFS()
	files["pages/landing.gohtml"] = &fstest.MapFile{Data: []byte(
		`{{define "content"}}<link href="{{asset "css/app.css"}}">{{end}}`,
	)}

	renderer, err := render.New(render.Options{
		Templates: files,
		Funcs: template.FuncMap{
			"asset": func(string) (string, error) { return "/assets/abc/css/app.css", nil },
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	recorder := httptest.NewRecorder()
	if err := renderer.Page(recorder, http.StatusOK, "landing", model{}); err != nil {
		t.Fatalf("Page: %v", err)
	}

	if !strings.Contains(recorder.Body.String(), "/assets/abc/css/app.css") {
		t.Errorf("the asset function did not reach the template:\n%s", recorder.Body.String())
	}
}

func TestFallbackWritesAWholeDocument(t *testing.T) {
	recorder := httptest.NewRecorder()

	render.Fallback(recorder, http.StatusInternalServerError)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}

	body := recorder.Body.String()

	if !strings.Contains(body, "<!doctype html>") {
		t.Errorf("the fallback must be a complete document:\n%s", body)
	}

	// No template, no asset reference: this is what answers when the error
	// template itself failed, so it cannot depend on anything that could fail.
	if strings.Contains(body, "/assets/") {
		t.Error("the fallback must not reference an asset")
	}

	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./internal/http/render/ -v`
Expected: build failure — the package does not exist.

- [ ] **Step 3: Implement the renderer**

Create `internal/http/render/render.go`:

```go
// Package render turns the embedded templates into HTML responses.
//
// The filesystem is a parameter rather than an embed reached for here: this
// package knows how to render, and where the files live is the consumer's
// knowledge. It is the same boundary the migration runner and CertificateSource
// already draw.
package render

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sync"
)

const (
	// layoutName is the template every page is executed through. Pages supply
	// "content"; the layout supplies the document around it.
	layoutName = "base"

	contentType = "text/html; charset=utf-8"
	// Most pages this service will serve are sensitive by nature — a login form
	// carries a CSRF token, an authenticated page carries the user's data — and
	// an allowlist of cacheable pages is a decision someone eventually gets
	// wrong. Caching is what the fingerprinted assets are for.
	cacheControl = "no-store"
)

// fallbackDocument answers when rendering the error page itself failed. It
// carries no template, no asset and no data, because everything it could depend
// on is what already failed.
const fallbackDocument = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Error</title></head>
<body><h1>Something went wrong</h1><p>The request could not be completed.</p></body></html>`

type Options struct {
	Templates fs.FS
	Funcs     template.FuncMap
}

type Renderer struct {
	pages map[string]*template.Template
	pool  sync.Pool
}

// New parses everything up front, so a broken template fails the boot rather
// than the first request that reaches it.
func New(opts Options) (*Renderer, error) {
	shared, err := sharedPatterns(opts.Templates)
	if err != nil {
		return nil, err
	}

	pages, err := fs.Glob(opts.Templates, "pages/*.gohtml")
	if err != nil {
		return nil, fmt.Errorf("render: listing pages: %w", err)
	}

	if len(pages) == 0 {
		return nil, fmt.Errorf("render: no pages found under pages/")
	}

	renderer := &Renderer{
		pages: make(map[string]*template.Template, len(pages)),
		pool:  sync.Pool{New: func() any { return new(bytes.Buffer) }},
	}

	// One template set per page rather than one for everything: pages all define
	// "content", and a single set would have them overwrite each other.
	for _, page := range pages {
		name := pageName(page)

		parsed, err := template.New(name).
			Funcs(opts.Funcs).
			ParseFS(opts.Templates, append(append([]string{}, shared...), page)...)
		if err != nil {
			return nil, fmt.Errorf("render: parsing %s: %w", page, err)
		}

		renderer.pages[name] = parsed
	}

	return renderer, nil
}

// sharedPatterns returns the directories that actually contain something.
// ParseFS treats a pattern matching no file as an error, so a directory that
// does not exist yet must not be handed to it.
func sharedPatterns(fsys fs.FS) ([]string, error) {
	var patterns []string

	for _, dir := range []string{"layouts", "partials"} {
		pattern := dir + "/*.gohtml"

		matches, err := fs.Glob(fsys, pattern)
		if err != nil {
			return nil, fmt.Errorf("render: listing %s: %w", dir, err)
		}

		if len(matches) > 0 {
			patterns = append(patterns, pattern)
		}
	}

	return patterns, nil
}

func pageName(file string) string {
	base := path.Base(file)

	return base[:len(base)-len(path.Ext(base))]
}

// Page renders into a buffer before touching the response. Template execution
// can fail halfway — a nil map, a method returning an error — and writing
// directly would leave a 200 already sent with half a document under it. The
// JSON writer takes the same precaution for the same reason.
func (r *Renderer) Page(w http.ResponseWriter, status int, name string, data any) error {
	parsed, ok := r.pages[name]
	if !ok {
		return fmt.Errorf("render: unknown page %q", name)
	}

	buf, _ := r.pool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		r.pool.Put(buf)
	}()

	if err := parsed.ExecuteTemplate(buf, layoutName, data); err != nil {
		return fmt.Errorf("render: executing %q: %w", name, err)
	}

	writeHeaders(w, status)

	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("render: writing %q: %w", name, err)
	}

	return nil
}

// Fallback is the floor under the error path. Whoever discovers that the error
// template failed is already answering an error, and calling the renderer again
// would recurse.
func Fallback(w http.ResponseWriter, status int) {
	writeHeaders(w, status)

	_, _ = io.WriteString(w, fallbackDocument)
}

func writeHeaders(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cacheControl)
	w.WriteHeader(status)
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test -race ./internal/http/render/ -v`
Expected: PASS, all nine tests. `TestPageEscapesContextually` is the one that matters most — if it fails, the template is not being parsed by `html/template`.

- [ ] **Step 5: Format and vet**

Run: `make fmt && go vet ./internal/http/render/...`

---

### Task 4: Security headers, and a recoverer that knows its surface

**Files:**
- Create: `internal/http/middleware/security_headers.go`
- Modify: `internal/http/middleware/recoverer.go`
- Modify: `internal/http/response/response.go` (add `ServerError`)
- Modify: `internal/http/middleware/middleware_test.go` (two call sites, plus new cases)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `middleware.ContentSecurityPolicy` constant
  - `middleware.SecurityHeaders(policy string) Middleware`
  - `middleware.ErrorWriter func(http.ResponseWriter, *http.Request)`
  - `middleware.Recoverer(logger *log.Logger, write ErrorWriter) Middleware` — **signature change**
  - `response.ServerError(w http.ResponseWriter, r *http.Request)`

- [ ] **Step 1: Write the failing tests**

Append to `internal/http/middleware/middleware_test.go`:

```go
func TestSecurityHeadersSendsThePolicy(t *testing.T) {
	handler := middleware.SecurityHeaders(middleware.ContentSecurityPolicy)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	// Written out by hand on purpose. Comparing the constant to itself would
	// pass whatever the constant said, including a directive silently dropped
	// by a refactor — the same failure the canary tests for the forced database
	// parameters exist to prevent.
	const want = "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

	if got := recorder.Header().Get("Content-Security-Policy"); got != want {
		t.Errorf("Content-Security-Policy =\n  %q\nwant\n  %q", got, want)
	}
}

func TestSecurityHeadersAlwaysSendsTheOtherThree(t *testing.T) {
	// An empty policy is what a disabled csp section produces. The rest have no
	// switch, because no deployment needs them off.
	handler := middleware.SecurityHeaders("")(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := recorder.Header().Get("Content-Security-Policy"); got != "" {
		t.Errorf("an empty policy must send no header, got %q", got)
	}

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestRecovererUsesTheWriterItWasGiven(t *testing.T) {
	called := false

	handler := middleware.Recoverer(discardLogger(), func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html>boom</html>"))
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Fatal("the recoverer did not use the supplied error writer")
	}

	if recorder.Body.String() != "<html>boom</html>" {
		t.Errorf("body = %q", recorder.Body.String())
	}
}
```

Then change the two existing `middleware.Recoverer(discardLogger())` calls in that file to `middleware.Recoverer(discardLogger(), response.ServerError)`, importing `"github.com/rootless-dev/aegis/internal/http/response"`.

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./internal/http/middleware/ -v`
Expected: build failure — `undefined: middleware.SecurityHeaders`, `undefined: middleware.ContentSecurityPolicy`, and `too many arguments in call to middleware.Recoverer`.

- [ ] **Step 3: Create the security headers middleware**

Create `internal/http/middleware/security_headers.go`:

```go
package middleware

import "net/http"

// ContentSecurityPolicy is what every HTML page is served under.
//
// default-src 'none' rather than 'self': every source type has to be named
// deliberately, so a fetch nobody designed fails loudly instead of inheriting a
// permission.
//
// Absent on purpose: script-src, connect-src and font-src. Nothing here ships
// JavaScript or a web font yet, and granting a permission nothing uses is the
// thing this policy exists to avoid. The first two arrive with HTMX, which needs
// connect-src or every request it makes fails.
//
// frame-ancestors stops a login screen being framed and overlaid, base-uri stops
// an injected <base> repointing every relative URL, and form-action stops an
// injection retargeting a password form at another host.
const ContentSecurityPolicy = "default-src 'none'; " +
	"style-src 'self'; " +
	"img-src 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'"

const (
	ContentSecurityPolicyHeader = "Content-Security-Policy"
	ContentTypeOptionsHeader    = "X-Content-Type-Options"
	ReferrerPolicyHeader        = "Referrer-Policy"
	FrameOptionsHeader          = "X-Frame-Options"
)

// SecurityHeaders guards the page surface. An empty policy sends no CSP, which
// is what a disabled csp section produces; the other three have no switch,
// because no deployment needs them off.
func SecurityHeaders(policy string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if policy != "" {
				w.Header().Set(ContentSecurityPolicyHeader, policy)
			}

			w.Header().Set(ContentTypeOptionsHeader, "nosniff")

			// The login URLs this service will serve carry state and
			// redirect_uri in the query string, and Referer would hand them to
			// every host a page links to.
			w.Header().Set(ReferrerPolicyHeader, "no-referrer")

			// Redundant with frame-ancestors on current browsers, and one line
			// for the ones that are not.
			w.Header().Set(FrameOptionsHeader, "DENY")

			next.ServeHTTP(w, r)
		})
	}
}
```

- [ ] **Step 4: Give the recoverer its writer**

In `internal/http/middleware/recoverer.go`, replace the signature and the final write. The whole file becomes:

```go
package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/phuslu/log"
)

// ErrorWriter answers a request that failed. Each surface supplies its own: the
// JSON one for the API and the probes, an HTML one for the pages. The choice
// comes from the route group and never from the Accept header, which the caller
// controls and which would make the response format negotiable on the one path
// that must not be.
type ErrorWriter func(w http.ResponseWriter, r *http.Request)

// Recoverer turns a panic into a controlled 500. Without it net/http drops the
// connection with no response, and the client sees a network error instead of
// a server error.
func Recoverer(logger *log.Logger, write ErrorWriter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}

				// ErrAbortHandler is net/http asking to abort silently.
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}

				logger.Error().
					Str("request_id", RequestIDFrom(r.Context())).
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Interface("panic", recovered).
					Bytes("stack", debug.Stack()).
					Msg("panic recovered while serving request")

				if recorder, ok := w.(HeaderRecorder); ok && recorder.WroteHeader() {
					return
				}

				write(w, r)
			}()

			next.ServeHTTP(w, r)
		})
	}
}
```

Note the `internal/http/response` import is gone: the middleware no longer decides the format, which also removes a dependency it had no business carrying.

- [ ] **Step 5: Add the JSON adapter**

In `internal/http/response/response.go`, after `WriteServerError`:

```go
// ServerError is WriteServerError in the shape the recoverer takes. The request
// is unused; it is in the signature because the HTML writer needs it.
func ServerError(w http.ResponseWriter, _ *http.Request) {
	WriteServerError(w)
}
```

- [ ] **Step 6: Fix the assembly so the tree compiles**

`internal/application/router.go` has two `middleware.Recoverer(app.logger)` calls that no longer compile. Give both `response.ServerError` for now — task 8 changes the page one to HTML:

```go
	router.Use(middleware.Recoverer(app.logger, response.ServerError))
```

and inside the group:

```go
			middleware.Recoverer(app.logger, response.ServerError),
```

Add the import `"github.com/rootless-dev/aegis/internal/http/response"`.

- [ ] **Step 7: Run the tests and confirm they pass**

Run: `go build ./... && go test -race ./internal/http/... ./internal/application/... -v`
Expected: PASS. The existing recoverer tests still pass because `response.ServerError` writes exactly what they already assert.

- [ ] **Step 8: Format and vet**

Run: `make fmt && go vet ./...`

---

### Task 5: The templates package

**Files:**
- Create: `internal/templates/templates.go`
- Create: `internal/templates/templates_test.go`
- Create: `internal/templates/layouts/base.gohtml`
- Create: `internal/templates/pages/landing.gohtml`
- Create: `internal/templates/pages/error.gohtml`
- Create: `internal/templates/assets/favicon.svg`

**Interfaces:**
- Consumes: `render.New`, `assets.New` (in the test, to prove the real files parse).
- Produces:
  - `templates.Templates() fs.FS`
  - `templates.Assets() (fs.FS, error)`

- [ ] **Step 1: Create the favicon**

Create `internal/templates/assets/favicon.svg`. Any valid SVG; this one is a placeholder shield the design can replace later without touching code:

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" width="32" height="32">
  <path d="M16 2 4 7v9c0 7 5 12 12 14 7-2 12-7 12-14V7L16 2z" fill="#1f2937"/>
  <path d="M16 9v14c4-1.5 7-4.8 7-9.3V10l-7-3z" fill="#60a5fa"/>
</svg>
```

- [ ] **Step 2: Create the layout**

Create `internal/templates/layouts/base.gohtml`:

```gohtml
{{define "base"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="icon" href="{{asset "favicon.svg"}}" type="image/svg+xml">
<link rel="stylesheet" href="{{asset "css/app.css"}}">
</head>
<body>
{{template "content" .}}
</body>
</html>
{{end}}
```

No `<style>` block, no inline handler, no inline script — the policy from task 4 forbids all three, and a template written against the slack is what makes a policy impossible to tighten later.

- [ ] **Step 3: Create the two pages**

Create `internal/templates/pages/landing.gohtml`:

```gohtml
{{define "content"}}
<main>
  <h1>{{.Title}}</h1>
  <p>{{.Tagline}}</p>
</main>
{{end}}
```

Create `internal/templates/pages/error.gohtml`:

```gohtml
{{define "content"}}
<main>
  <h1>{{.Status}}</h1>
  <p>{{.Message}}</p>
</main>
{{end}}
```

Both define `content`, which is fine: each page is parsed into its own template set, so they never see each other.

The error page renders a status and a fixed message, never the underlying error. A panic message or a wrapped database error on a public page is an information leak — the same distinction `health` already draws with `RevealErrors`.

- [ ] **Step 4: Write the failing test**

Create `internal/templates/templates_test.go`:

```go
package templates_test

import (
	"html/template"
	"io/fs"
	"testing"

	"github.com/rootless-dev/aegis/internal/http/assets"
	"github.com/rootless-dev/aegis/internal/http/render"
	"github.com/rootless-dev/aegis/internal/templates"
)

// The canary: it walks pages/ rather than listing names, so a page added
// without a test still has to parse and execute.
func TestEveryPageParsesAndExecutes(t *testing.T) {
	assetFS, err := templates.Assets()
	if err != nil {
		t.Fatalf("Assets: %v", err)
	}

	server, err := assets.New(assetFS)
	if err != nil {
		t.Fatalf("assets.New: %v", err)
	}

	renderer, err := render.New(render.Options{
		Templates: templates.Templates(),
		Funcs:     template.FuncMap{"asset": server.URL},
	})
	if err != nil {
		t.Fatalf("render.New on the real templates: %v", err)
	}

	pages, err := fs.Glob(templates.Templates(), "pages/*.gohtml")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	if len(pages) == 0 {
		t.Fatal("no pages were embedded")
	}

	_ = renderer
}

func TestTheAssetEmbedIsNotEmpty(t *testing.T) {
	assetFS, err := templates.Assets()
	if err != nil {
		t.Fatalf("Assets: %v", err)
	}

	// go:embed fails on a directory matching no files, and the stylesheet is
	// generated rather than committed. favicon.svg is what keeps the directory
	// non-empty; if it is ever removed with nothing replacing it, the build
	// breaks for everyone who has not run `make assets`.
	if _, err := fs.Stat(assetFS, assets.Favicon); err != nil {
		t.Errorf("%s must be committed: it is what keeps the asset embed valid (%v)", assets.Favicon, err)
	}
}

func TestTemplatesAreNotServedAsAssets(t *testing.T) {
	assetFS, err := templates.Assets()
	if err != nil {
		t.Fatalf("Assets: %v", err)
	}

	// Two separate filesystems, or the file server hands out layouts/base.gohtml
	// as text.
	if _, err := fs.Stat(assetFS, "layouts/base.gohtml"); err == nil {
		t.Error("a template is reachable through the asset filesystem")
	}

	// And the Tailwind input is not a served asset either.
	if _, err := fs.Stat(assetFS, "input.css"); err == nil {
		t.Error("the tailwind input must live outside the served tree")
	}
}
```

- [ ] **Step 5: Run the test and confirm it fails**

Run: `go test ./internal/templates/ -v`
Expected: build failure — the package does not exist.

- [ ] **Step 6: Create the package**

Create `internal/templates/templates.go`:

```go
// Package templates owns the HTML templates and the static assets built into
// the binary. It declares the embeds and nothing else: what renders them and
// what serves them take a filesystem as a parameter.
package templates

import (
	"embed"
	"io/fs"
)

//go:embed layouts pages
var templateFiles embed.FS

// The stylesheet under assets/css is generated by `make assets` and is not in
// the repository. What keeps this embed valid is favicon.svg being committed:
// go:embed fails on a directory matching no files, and it skips names starting
// with a dot, so a .gitkeep would not have worked. Do not remove the favicon
// without committing something else in its place.
//
//go:embed assets
var assetFiles embed.FS

// Templates carries the layouts/ and pages/ prefixes the renderer globs on, so
// it is returned as embedded.
func Templates() fs.FS {
	return templateFiles
}

// Assets is rooted at the asset directory itself, because the paths in a
// template ("css/app.css") are what a browser asks for and must not carry a
// directory that only exists in this repository.
func Assets() (fs.FS, error) {
	return fs.Sub(assetFiles, "assets")
}
```

- [ ] **Step 7: Run the test**

Run: `go test ./internal/templates/ -v`
Expected: `TestTheAssetEmbedIsNotEmpty` and `TestTemplatesAreNotServedAsAssets` PASS. `TestEveryPageParsesAndExecutes` PASS too — `assets.New` succeeds even without the stylesheet, and `render.New` only fails if a template is malformed.

Note the pages are not executed yet: doing so needs the models, which live with the handlers. Task 7 completes this test.

- [ ] **Step 8: Format and vet**

Run: `make fmt && go vet ./internal/templates/...`

---

### Task 6: Tailwind in the build

**Files:**
- Create: `internal/templates/tailwind/input.css`
- Create: `tailwind.sha256`
- Modify: `Makefile`, `.gitignore`, `.dockerignore`, `.air.toml`, `Tiltfile`
- Modify: `docker/Dockerfile.production`, `docker/Dockerfile.development`
- Modify: `.github/workflows/ci.yml`, `sonar-project.properties`
- Modify: `docs/development.md`

**Interfaces:**
- Consumes: `internal/templates/` from task 5.
- Produces: `internal/templates/assets/css/app.css` on disk, and a `make assets` target every other target depends on.

- [ ] **Step 1: Ignore the generated stylesheet, in both places**

In `.gitignore`, after the build output block:

```
# Generated by `make assets`. Not committed: it would produce a diff on every
# class change. The build regenerates it and the boot refuses to start without
# it outside development.
/internal/templates/assets/css/app.css
```

In `.dockerignore`, alongside the other exclusions:

```
# Generated at build time by the assets stage. Excluding it keeps the image
# reproducible: otherwise COPY . . carries whatever the developer has on disk.
internal/templates/assets/css/app.css
```

The `.dockerignore` line is not optional. Without it a stale local build silently overrides what the image generated.

- [ ] **Step 2: Write the Tailwind input**

Create `internal/templates/tailwind/input.css`:

```css
/* Tailwind v4 is CSS-first: no tailwind.config.js, and the sources to scan for
   class names are declared here. This file is the CLI's input and lives outside
   assets/ so it is never served. */
@import "tailwindcss";

@source "../layouts/**/*.gohtml";
@source "../pages/**/*.gohtml";
```

**Verify this against the pinned version before moving on.** The `@source` directive and the `@import "tailwindcss"` form are v4; v3 used `@tailwind` directives and a config file. If the pinned CLI rejects this file, the version and the syntax must be reconciled — do not work around it by pinning v3, since the plan assumes the standalone v4 binary.

- [ ] **Step 3: Add the Makefile targets**

Add the variables near the other tool pins, beside `GOSEC`:

```makefile
# A tool version is a dependency and gets pinned like one. The checksums live
# in tailwind.sha256 because the binary is downloaded rather than built.
TAILWIND_VERSION := v4.1.11
TAILWIND_BIN     := bin/tailwindcss
TAILWIND_INPUT   := internal/templates/tailwind/input.css
TAILWIND_OUTPUT  := internal/templates/assets/css/app.css
```

Add the targets under `##@ Development`:

```makefile
.PHONY: assets
assets: $(TAILWIND_BIN) ## Generate the stylesheet
	@mkdir -p $(dir $(TAILWIND_OUTPUT))
	$(TAILWIND_BIN) -i $(TAILWIND_INPUT) -o $(TAILWIND_OUTPUT) --minify

# Downloaded rather than vendored, and verified rather than trusted: fetching a
# binary at build time is a supply chain surface, so the checksum is not
# optional.
$(TAILWIND_BIN):
	@mkdir -p bin
	@os=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	arch=$$(uname -m); \
	case "$$os" in darwin) os=macos ;; esac; \
	case "$$arch" in x86_64|amd64) arch=x64 ;; aarch64|arm64) arch=arm64 ;; esac; \
	name="tailwindcss-$$os-$$arch"; \
	echo "downloading $$name $(TAILWIND_VERSION)"; \
	curl -sSfL "https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/$$name" -o $(TAILWIND_BIN); \
	expected=$$(awk -v n="$$name" '$$2 == n {print $$1}' tailwind.sha256); \
	if [ -z "$$expected" ]; then echo "no checksum recorded for $$name in tailwind.sha256"; rm -f $(TAILWIND_BIN); exit 1; fi; \
	actual=$$(shasum -a 256 $(TAILWIND_BIN) | awk '{print $$1}'); \
	if [ "$$expected" != "$$actual" ]; then echo "checksum mismatch for $$name"; rm -f $(TAILWIND_BIN); exit 1; fi; \
	chmod +x $(TAILWIND_BIN)
```

Make `build`, `test`, `test-integration` and `ci` depend on it:

```makefile
build: assets ## Compile the server binary
test: assets ## Run the unit tests with the race detector
test-integration: assets ## Run the integration tests, against sqlite by default
ci: assets fmt-check vet test test-integration gosec ## Run everything the pipeline runs
```

Add the generated file to `clean`:

```makefile
clean: ## Remove build and coverage artifacts
	rm -rf bin $(COVERAGE) coverage.html $(TAILWIND_OUTPUT)
```

- [ ] **Step 4: Record the checksums**

Create `tailwind.sha256` with one line per platform, in `shasum` format — `<sha256>  <asset-name>`:

```
<sha256>  tailwindcss-linux-x64
<sha256>  tailwindcss-linux-arm64
<sha256>  tailwindcss-macos-x64
<sha256>  tailwindcss-macos-arm64
```

Fill each digest from the release you pinned. The upstream release publishes them; if it does not, download each asset once and record `shasum -a 256` of what you got, then state in a comment at the top of the file which release they came from.

- [ ] **Step 5: Run it and confirm the stylesheet appears**

Run: `make assets && ls -l internal/templates/assets/css/app.css`
Expected: the binary downloads, the checksum verifies, and a non-empty CSS file exists.

Then confirm the guard works: `rm bin/tailwindcss`, corrupt a digest in `tailwind.sha256`, run `make assets` again, and check that it fails with "checksum mismatch" and leaves no binary behind. Restore the digest.

- [ ] **Step 6: Teach air about templates**

In `.air.toml`:

```toml
  cmd = "make assets && go build -gcflags='all=-N -l' -o ./tmp/aegisd ./cmd/server"
  include_ext = ["go", "toml", "env", "gohtml", "css"]
  exclude_dir = ["tmp", "bin", "docs", ".git", ".github"]
  exclude_regex = ["_test\\.go", "internal/templates/assets/css/app\\.css"]
```

The `exclude_regex` entry is required, not tidy: the build writes that file, and watching it would retrigger the build that wrote it.

The development image needs the CLI available. In `docker/Dockerfile.development`, after the `apk add` line:

```dockerfile
RUN apk add --no-cache git make curl
```

`make assets` inside the container downloads the Linux binary into the mounted `bin/`, so it is fetched once per environment.

- [ ] **Step 7: Teach Tilt the same, and avoid the loop**

In `Tiltfile`, change the compile resource:

```python
local_resource(
    'compile',
    'make assets && CGO_ENABLED=0 GOOS=linux GOARCH=%s go build -o bin/aegisd ./cmd/server' % GOARCH,
    deps=['cmd', 'internal', 'go.mod', 'go.sum'],
    ignore=['internal/templates/assets/css/app.css'],
    labels=['build'],
)
```

Without `ignore`, this resource writes a file inside `internal`, which is one of its own deps, and Tilt rebuilds forever.

`docker/Dockerfile.tilt` does not change: it compiles nothing and receives a binary that already has the assets embedded.

- [ ] **Step 8: Add the production image stage**

In `docker/Dockerfile.production`, before the builder stage:

```dockerfile
ARG ALPINE_VERSION=3.24

# Generating the stylesheet needs no Go toolchain and no target architecture:
# the output is plain CSS, so this runs once on the build platform and is shared
# by every target.
FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS assets

WORKDIR /src

RUN apk add --no-cache make curl

COPY Makefile tailwind.sha256 ./
COPY internal/templates ./internal/templates

RUN make assets
```

And in the builder stage, **after** the existing `COPY . .`:

```dockerfile
COPY . .
COPY --from=assets /src/internal/templates/assets/css/app.css ./internal/templates/assets/css/app.css
```

The order is load-bearing: `COPY . .` after this line would overwrite the generated file with nothing.

- [ ] **Step 9: Add the CI step**

In `.github/workflows/ci.yml`, in both the `test` job and the `engines` job, insert before the first Go command that compiles:

```yaml
      - name: Generate assets
        run: make assets
```

- [ ] **Step 10: Keep the analysis honest**

In `sonar-project.properties`:

```properties
sonar.exclusions=**/*_test.go,**/vendor/**,internal/templates/assets/css/app.css,**/*.min.js
```

and append to the coverage exclusions:

```properties
sonar.coverage.exclusions=cmd/**,internal/application/application.go,internal/application/resources.go,internal/templates/templates.go
```

`templates.go` is two embed directives and two accessors with no branch in them, which is exactly the criterion already stated in that file. The `*.min.js` pattern is there before the first minified bundle arrives: the pipeline fails the build on a failed quality gate, so an un-excluded bundle is a red build no code change can fix.

- [ ] **Step 11: Verify the whole pipeline**

Run: `make clean && make ci`
Expected: assets generate first, then everything passes.

Then confirm the image builds: `make image`
Expected: success, with the `assets` stage visible in the output.

- [ ] **Step 12: Document it**

In `docs/development.md`, under `## Running`, add that `make assets` generates the stylesheet, that `make build`/`make test`/`make ci` run it for you, and that calling `go build` directly does not — in which case every profile, development included, refuses to boot with an error naming `make assets`. There is no unstyled-page fallback: the layout resolves the stylesheet through the asset function, which fails on an unknown logical path by design. Under `## Hot reload and debugger`, note that air now watches `.gohtml` and `.css` and regenerates the stylesheet on each rebuild.

---

### Task 7: The page handlers

**Files:**
- Create: `internal/handler/page/page.go`
- Create: `internal/handler/page/page_test.go`
- Modify: `internal/templates/templates_test.go` (complete the canary now that the models exist)

**Interfaces:**
- Consumes: `render.Renderer`, `render.Fallback`, `phuslu/log`.
- Produces:
  - `page.New(renderer *render.Renderer, logger *log.Logger) *page.Handler`
  - `(*Handler) Landing(w http.ResponseWriter, r *http.Request)`
  - `(*Handler) NotFound(w http.ResponseWriter, r *http.Request)`
  - `(*Handler) MethodNotAllowed(w http.ResponseWriter, r *http.Request)`
  - `(*Handler) ServerError(w http.ResponseWriter, r *http.Request)` — satisfies `middleware.ErrorWriter`

Handlers live at `internal/handler/`, not under `internal/http/`. `middleware`, `response` and `server` are mechanism — transport, with no business rule and no dependency on `domain` or `service`. A handler is a layer: it is where a request becomes a use case call. Keeping it at the root of `internal` keeps the four layers side by side once `domain`, `service` and `repository` exist, which is what makes the dependency rule readable from the tree.

- [ ] **Step 1: Write the failing tests**

Create `internal/handler/page/page_test.go`:

```go
package page_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/handler/page"
	"github.com/rootless-dev/aegis/internal/http/render"
)

func discardLogger() *log.Logger {
	return &log.Logger{Writer: log.IOWriter{Writer: io.Discard}}
}

func workingTemplates() fstest.MapFS {
	return fstest.MapFS{
		"layouts/base.gohtml": &fstest.MapFile{Data: []byte(
			`{{define "base"}}<!doctype html><html><head><title>{{.Title}}</title></head><body>{{template "content" .}}</body></html>{{end}}`,
		)},
		"pages/landing.gohtml": &fstest.MapFile{Data: []byte(
			`{{define "content"}}<h1>{{.Title}}</h1><p>{{.Tagline}}</p>{{end}}`,
		)},
		"pages/error.gohtml": &fstest.MapFile{Data: []byte(
			`{{define "content"}}<h1>{{.Status}}</h1><p>{{.Message}}</p>{{end}}`,
		)},
	}
}

func newHandler(t *testing.T, files fstest.MapFS) *page.Handler {
	t.Helper()

	renderer, err := render.New(render.Options{Templates: files})
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	return page.New(renderer, discardLogger())
}

func TestLandingRendersHTML(t *testing.T) {
	recorder := httptest.NewRecorder()

	newHandler(t, workingTemplates()).Landing(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}

	if !strings.Contains(recorder.Body.String(), "<!doctype html>") {
		t.Errorf("body is not a document:\n%s", recorder.Body.String())
	}
}

func TestNotFoundRendersHTMLWithTheRightStatus(t *testing.T) {
	recorder := httptest.NewRecorder()

	newHandler(t, workingTemplates()).NotFound(recorder, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}

	if !strings.Contains(recorder.Body.String(), "404") {
		t.Errorf("the page must show the status:\n%s", recorder.Body.String())
	}
}

func TestMethodNotAllowedRendersHTML(t *testing.T) {
	recorder := httptest.NewRecorder()

	newHandler(t, workingTemplates()).MethodNotAllowed(recorder, httptest.NewRequest(http.MethodPost, "/", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

func TestServerErrorNeverLeaksTheCause(t *testing.T) {
	recorder := httptest.NewRecorder()

	newHandler(t, workingTemplates()).ServerError(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}

	body := recorder.Body.String()

	// A panic message or a wrapped database error on a public page is an
	// information leak. The page shows a status and a fixed sentence.
	if strings.Contains(body, "panic") || strings.Contains(body, "sql") {
		t.Errorf("the error page leaked internals:\n%s", body)
	}
}

// The floor: whoever discovers the error template failed is already answering
// an error, so calling the renderer again would recurse.
func TestFallsBackWhenTheErrorTemplateItselfFails(t *testing.T) {
	broken := workingTemplates()
	// Executes fine at parse time and fails at execution: calling a method that
	// does not exist on the model.
	broken["pages/error.gohtml"] = &fstest.MapFile{Data: []byte(
		`{{define "content"}}{{ .Missing.Field }}{{end}}`,
	)}

	recorder := httptest.NewRecorder()

	newHandler(t, broken).ServerError(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}

	if !strings.Contains(recorder.Body.String(), "<!doctype html>") {
		t.Errorf("the fallback document was not written:\n%s", recorder.Body.String())
	}
}

func TestFallsBackWhenTheErrorPageIsAbsent(t *testing.T) {
	missing := workingTemplates()
	delete(missing, "pages/error.gohtml")

	recorder := httptest.NewRecorder()

	newHandler(t, missing).NotFound(recorder, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}

	if !strings.Contains(recorder.Body.String(), "<!doctype html>") {
		t.Errorf("the fallback document was not written:\n%s", recorder.Body.String())
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./internal/handler/page/ -v`
Expected: build failure — the package does not exist.

- [ ] **Step 3: Implement the handlers**

Create `internal/handler/page/page.go`:

```go
// Package page serves the HTML surface: the pages a browser reaches directly,
// as opposed to the JSON endpoints a client calls.
package page

import (
	"net/http"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/http/render"
)

const (
	landingTemplate = "landing"
	errorTemplate   = "error"

	notFoundMessage = "That page does not exist."
	// Deliberately says nothing about the failure. A panic message or a wrapped
	// database error on a public page is an information leak, which is the same
	// distinction health draws with RevealErrors.
	serverErrorMessage      = "Something went wrong. Please try again."
	methodNotAllowedMessage = "That method is not allowed here."
)

type Handler struct {
	renderer *render.Renderer
	logger   *log.Logger
}

func New(renderer *render.Renderer, logger *log.Logger) *Handler {
	return &Handler{renderer: renderer, logger: logger}
}

type landingModel struct {
	Title   string
	Tagline string
}

type errorModel struct {
	Title   string
	Status  int
	Message string
}

func (h *Handler) Landing(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, landingTemplate, landingModel{
		Title:   "Aegis",
		Tagline: "Identity for every tenant.",
	})
}

func (h *Handler) NotFound(w http.ResponseWriter, r *http.Request) {
	h.renderError(w, r, http.StatusNotFound, notFoundMessage)
}

func (h *Handler) MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	h.renderError(w, r, http.StatusMethodNotAllowed, methodNotAllowedMessage)
}

// ServerError satisfies middleware.ErrorWriter, which is how the page surface
// answers a panic in HTML while the API surface still answers JSON.
func (h *Handler) ServerError(w http.ResponseWriter, r *http.Request) {
	h.renderError(w, r, http.StatusInternalServerError, serverErrorMessage)
}

func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	h.render(w, r, status, errorTemplate, errorModel{
		Title:   http.StatusText(status),
		Status:  status,
		Message: message,
	})
}

// render is where the floor lives. Rendering can fail, and the handler that
// discovers it may already be answering an error — so the failure path writes a
// fixed document rather than calling the renderer again and recursing.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	if err := h.renderer.Page(w, status, name, data); err != nil {
		h.logger.Error().
			Err(err).
			Str("template", name).
			Str("path", r.URL.Path).
			Msg("rendering a page failed")

		render.Fallback(w, status)
	}
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test -race ./internal/handler/page/ -v`
Expected: PASS, all six tests.

**If `TestFallsBackWhenTheErrorTemplateItselfFails` fails with a duplicated header write**, the cause is that `render.Page` wrote the status before execution failed. It cannot: `Page` executes into a buffer and only writes after. If the test shows otherwise, task 3 was implemented wrong — fix it there, not here.

- [ ] **Step 5: Complete the templates canary**

Now that the models exist, `internal/templates/templates_test.go` can execute the real pages. Replace the body of `TestEveryPageParsesAndExecutes` so it renders each page with a model that has every field the templates use:

```go
	for _, file := range pages {
		name := strings.TrimSuffix(filepath.Base(file), ".gohtml")

		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()

			// A model carrying every field any page reads. Executing with it
			// proves the template references nothing that does not exist.
			data := struct {
				Title   string
				Tagline string
				Status  int
				Message string
			}{Title: "Aegis", Tagline: "tagline", Status: 404, Message: "message"}

			if err := renderer.Page(recorder, 200, name, data); err != nil {
				t.Fatalf("executing %s: %v", file, err)
			}

			if !strings.Contains(recorder.Body.String(), "<!doctype html>") {
				t.Errorf("%s did not produce a document:\n%s", file, recorder.Body.String())
			}
		})
	}
```

Add the imports it needs: `net/http/httptest`, `path/filepath`, `strings`.

This is what makes a page added without a test still have to parse and execute. When a future page needs a field this struct lacks, the failure is here and the fix is to add the field.

- [ ] **Step 6: Run the templates test**

Run: `go test ./internal/templates/ -v`
Expected: PASS, with a subtest per page. If `landing` fails on the `asset` function, the test is building the renderer without the `FuncMap` — it must pass `template.FuncMap{"asset": server.URL}` as written in task 5.

- [ ] **Step 7: Format and vet**

Run: `make fmt && go vet ./...`

---

### Task 8: Assembly — wiring and the third surface

**Files:**
- Modify: `internal/application/application.go` (fields, one new step)
- Modify: `internal/application/wiring.go` (`setWeb`)
- Modify: `internal/application/router.go` (shared chain, third group, mounts, root handlers)
- Modify: `internal/application/router_test.go`

**Interfaces:**
- Consumes: everything from tasks 1 through 7.
- Produces: a running server answering `/` with HTML.

- [ ] **Step 1: Write the failing tests**

`internal/application/router_test.go` is in package `application`, not `application_test`, so it reaches `app.router`, `app.surfaces` and the new `app.pages` directly — nothing needs exporting.

Its helper is `newTestApplication(t *testing.T, logs *bytes.Buffer)`, and it builds the `Application` by hand and calls `setGraceful`, `setHealth` and `setRouter` in order. Two changes to the helper first:

```go
			HSTS:     &configs.HSTS{},
			CSP:      &configs.CSP{Enabled: true},
```

and a `setWeb` call before `setRouter`, matching the order in `New`:

```go
	if err := app.setWeb(); err != nil {
		t.Fatalf("web: %v", err)
	}

	if err := app.setRouter(); err != nil {
		t.Fatalf("router: %v", err)
	}
```

The helper's profile is `ProfileProd`, so `setWeb` will refuse to start without the stylesheet — which means **`make assets` has to have run before these tests**, exactly as the Makefile now enforces. That is the behaviour under test, not an obstacle to it.

Then add the cases:

```go
func TestLandingPageAnswersHTML(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	recorder := httptest.NewRecorder()
	app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	if recorder.Header().Get("Content-Security-Policy") == "" {
		t.Error("the page surface must carry a content security policy")
	}
}

func TestUnknownPathAnswersHTML(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	recorder := httptest.NewRecorder()
	app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/no-such-page", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}

	// The root is the browser-facing surface: an unregistered path is far more
	// likely a person with a typo than a client calling a missing endpoint.
	if !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %q, want html", recorder.Header().Get("Content-Type"))
	}
}

func TestFaviconDoesNotRenderAPage(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	recorder := httptest.NewRecorder()
	app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))

	// Browsers ask for it whatever the document says. Falling through to the
	// HTML 404 would render a whole page in answer to an icon request.
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
}

func TestProbesStillAnswerJSON(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	for _, path := range []string{health.LivenessPath, health.ReadinessPath} {
		recorder := httptest.NewRecorder()
		app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Errorf("%s: Content-Type = %q, want json", path, got)
		}

		if recorder.Header().Get("Content-Security-Policy") != "" {
			t.Errorf("%s: probes must not carry the page security headers", path)
		}
	}
}

// The case that pins the whole error design: the two surfaces must recover
// differently, or the split was pointless.
func TestEachSurfaceRecoversInItsOwnFormat(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	app.pages.Get("/boom-html", func(http.ResponseWriter, *http.Request) { panic("boom") })
	app.surfaces.Get("/boom-json", func(http.ResponseWriter, *http.Request) { panic("boom") })

	html := httptest.NewRecorder()
	app.router.ServeHTTP(html, httptest.NewRequest(http.MethodGet, "/boom-html", nil))

	if html.Code != http.StatusInternalServerError {
		t.Fatalf("html surface: status = %d, want 500", html.Code)
	}

	if !strings.Contains(html.Header().Get("Content-Type"), "text/html") {
		t.Errorf("html surface answered %q; a browser must not get JSON", html.Header().Get("Content-Type"))
	}

	json := httptest.NewRecorder()
	app.router.ServeHTTP(json, httptest.NewRequest(http.MethodGet, "/boom-json", nil))

	if !strings.Contains(json.Header().Get("Content-Type"), "application/json") {
		t.Errorf("api surface answered %q; a client must not get HTML", json.Header().Get("Content-Type"))
	}
}
```

- [ ] **Step 2: Run and confirm they fail**

Run: `go test ./internal/application/ -run 'TestLanding|TestUnknown|TestFavicon|TestProbes|TestEachSurface' -v`
Expected: failures — no landing route, 404 answers whatever chi's default is, no page group.

- [ ] **Step 3: Add the fields and the step**

In `internal/application/application.go`, add to the struct after `surfaces`:

```go
	// pages is the surface a browser reaches: the same base chain as the API
	// group, plus the security headers, and a recoverer that answers HTML.
	pages chi.Router

	assets   *assets.Server
	renderer *render.Renderer
	page     *page.Handler
```

And add the step **before** `setRouter` — the router mounts the asset server and the landing handler needs the renderer, so both have to exist first:

```go
	steps := []func() error{
		instance.setLogger,
		instance.setGraceful,
		instance.setHealth,
		instance.setDatabase,
		instance.setCertificates,
		instance.setWeb,
		instance.setRouter,
		instance.setHttpServer,
	}
```

- [ ] **Step 4: Write the wiring step**

In `internal/application/wiring.go`, after `setCertificates`:

```go
// setWeb builds what the HTML surface needs, in the only order that works: the
// asset server first, because the renderer's asset function comes from it, and
// the renderer before the handlers that use it.
func (app *Application) setWeb() error {
	assetFS, err := templates.Assets()
	if err != nil {
		return err
	}

	server, err := assets.New(assetFS)
	if err != nil {
		return err
	}

	// The stylesheet is generated by `make assets` and is not in the repository.
	// Every profile refuses to start without it, development included: the
	// unstyled page a warning would buy does not exist, because the layout
	// resolves the stylesheet through the asset function, which fails on an
	// unknown logical path by design.
	if err := server.Verify(assets.Stylesheet); err != nil {
		return fmt.Errorf("%w: run `make assets` before building", err)
	}

	renderer, err := render.New(render.Options{
		Templates: templates.Templates(),
		Funcs:     template.FuncMap{"asset": server.URL},
	})
	if err != nil {
		return err
	}

	app.assets = server
	app.renderer = renderer
	app.page = page.New(renderer, app.logger)

	return nil
}
```

Add the imports: `fmt`, `html/template`, and the four internal packages.

- [ ] **Step 5: Restructure the router**

Rewrite `setRouter` in `internal/application/router.go`:

```go
func (app *Application) setRouter() error {
	trustedProxies, err := app.cfg.Proxy.Networks()
	if err != nil {
		return err
	}

	router := chi.NewRouter()

	// Global, so a panic answers a status even on a probe instead of dropping
	// the connection. JSON, because what is mounted bare answers JSON.
	router.Use(middleware.Recoverer(app.logger, response.ServerError))

	app.health.Mount(router)

	// Assets carry nosniff of their own and want none of the rest: no request
	// timeout tuned for handlers, and no log line per stylesheet.
	app.assets.Mount(router)

	base := app.baseChain(trustedProxies)

	router.Group(func(group chi.Router) {
		group.Use(base...)
		group.Use(middleware.Recoverer(app.logger, response.ServerError))

		app.surfaces = group
	})

	router.Group(func(group chi.Router) {
		group.Use(base...)
		group.Use(middleware.Recoverer(app.logger, app.page.ServerError))
		group.Use(middleware.SecurityHeaders(app.contentSecurityPolicy()))

		group.Get("/", app.page.Landing)

		app.pages = group
	})

	// The root answers HTML: an unregistered path is far more likely a person
	// with a typo than a client calling an endpoint that does not exist. API
	// branches get their own when they are mounted under a prefix.
	router.NotFound(app.page.NotFound)
	router.MethodNotAllowed(app.page.MethodNotAllowed)

	app.router = router

	return nil
}

// baseChain is what both surfaces share. Extracted so the next middleware added
// to one of them cannot go silently missing from the other.
func (app *Application) baseChain(trustedProxies []netip.Prefix) []func(http.Handler) http.Handler {
	chain := []func(http.Handler) http.Handler{
		middleware.RequestID(),
		// Ahead of everything that reads the client address or the scheme: this
		// is where the forwarded headers are either trusted or removed, and no
		// handler downstream should have to make that call again.
		middleware.Proxy(middleware.ProxyOptions{
			TrustForwardedHeaders: app.cfg.TLS.TrustsForwardedHeaders(),
			TrustedProxies:        trustedProxies,
			Headers:               forwardedHeaders(app.cfg.Proxy.Headers),
			Scheme:                app.cfg.PublicScheme(),
		}),
		middleware.RequestLogger(app.logger),
	}

	if app.cfg.HSTS.Enabled {
		chain = append(chain, middleware.HSTS(app.cfg.HSTS.HeaderValue()))
	}

	return append(chain, middleware.Timeout(app.cfg.HttpServer.RequestTimeout))
}

func (app *Application) contentSecurityPolicy() string {
	if app.cfg.CSP == nil || !app.cfg.CSP.Enabled {
		return ""
	}

	return middleware.ContentSecurityPolicy
}
```

`Networks()` returns `[]netip.Prefix`, so the import is `net/netip`, not `net`.

Note the recoverer now comes after `RequestLogger` in each group, as it did before: it is repeated inside the logger so a panic also shows up as status 500 on the request line, which the outer one cannot do. Keep that comment.

- [ ] **Step 6: Run the tests and confirm they pass**

Run: `go test -race ./internal/application/ -v`
Expected: PASS. `TestEachSurfaceRecoversInItsOwnFormat` is the one that proves the design; if both surfaces answer the same format, the two `Recoverer` calls received the same writer.

- [ ] **Step 7: Run the whole suite**

Run: `make ci`
Expected: everything green.

- [ ] **Step 8: Look at it in a browser**

**Ask Carlos before starting anything.** His environment is usually already up under Tilt, and a second process on the same port is his problem to discover, not yours. If he says go ahead:

Run: `make assets && make run`
Then open `http://localhost:7500/` and confirm: the page renders, the stylesheet loads from a fingerprinted URL, the favicon appears, and the browser console reports no CSP violation. A violation in the console means a template broke the no-inline rule.

Also confirm `http://localhost:7500/no-such-page` renders the HTML 404 rather than a JSON body.

- [ ] **Step 9: Confirm the production posture**

Run: `rm internal/templates/assets/css/app.css && go build -o bin/aegisd ./cmd/server && ./bin/aegisd`
Expected: the boot **fails**, naming the missing stylesheet and `make assets`.

Then: `./bin/aegisd --dev`
Expected: it **also fails**, with the same message. Development gets no
exception — there is no unstyled page to fall back to.

Restore with `make assets`.

- [ ] **Step 10: Format and vet**

Run: `make fmt && go vet ./... && go vet -tags=integration ./internal/... ./test/...`

---

### Task 9: Document the architecture

**Files:**
- Modify: `docs/architecture.md`

**Interfaces:**
- Consumes: everything.
- Produces: nothing code depends on.

- [ ] **Step 1: Extend the layout section**

In `docs/architecture.md`, under `## Layout`, add the new packages with one line each, matching the density of the entries already there:

- `internal/templates` — owns the embedded templates and assets; declares two filesystems and no behaviour
- `internal/http/render` — composes layouts with pages and writes HTML, over an injected filesystem
- `internal/http/assets` — fingerprints and serves the static files
- `internal/handler/page` — the handlers for the HTML surface

Add a sentence on why handlers sit at `internal/handler` rather than under `internal/http`: the latter is mechanism, a handler is a layer, and keeping the layers at one level is what makes the dependency rule readable from the tree.

- [ ] **Step 2: Extend the HTTP section**

Under `## HTTP`, describe the three surfaces and what separates them:

- probes, mounted bare, so the orchestrator does not drown the request log
- the API surface, answering JSON, recovering as JSON
- the page surface, answering HTML, recovering as HTML, carrying the security headers
- assets, outside all of it, carrying `nosniff` and a year of caching keyed by content hash

State the rule explicitly, because it is the part someone will otherwise undo: **the response format comes from the route group, never from the `Accept` header.** Negotiating on the error path would make the format depend on something the caller controls.

Note the CSP forbids `unsafe-inline` and `unsafe-eval`, so no template may carry a `<style>` block, an inline handler or an inline script — and that this is why the policy landed with the first page rather than after the console.

- [ ] **Step 3: Verify the docs match the code**

Re-read both sections against the files as they now stand. Every package named must exist; every claim about middleware order must match `router.go`.

---

## Self-Review

**Spec coverage.** Walked each section of the spec against the tasks:

| Spec section | Task |
|---|---|
| Package layout, the `internal/handler` decision | 2, 3, 7 (decision restated in 7) |
| Directory layout, no empty directories, `input.css` outside `assets/` | 5, 6 |
| Composition, writing through a buffer, response headers | 3 |
| When the error page itself fails | 3 (`Fallback`), 7 (the caller) |
| Signatures, no `Fragment` in this slice | 3 |
| No disk reload path | 6 (air watches `.gohtml`) |
| Fingerprinting, serving, hash verification, `/favicon.ico` | 2 |
| Security headers, the CSP, the absent directives | 4 |
| Configuration | 1 |
| Router, shared chain, per-surface recoverer, 404/405 | 8 |
| Boot order and the stylesheet check | 2 (`Verify`), 8 (`setWeb`) |
| Build pipeline, four mechanisms, the CLI, air, Tilt, Sonar, CI | 6 |
| Test strategy | spread across every task; the canary is 5 and 7 |
| Files this touches | every file in that table appears in a task |

**Placeholders.** One deliberate blank remains: the four digests in `tailwind.sha256`, which cannot be known before the version is pinned. Step 4 of task 6 says exactly how to obtain them and what to record. Everything else carries real content.

**Type consistency.** Checked across tasks: `assets.Server`, `assets.Stylesheet`, `assets.Favicon`, `(*Server).URL`, `(*Server).Verify`, `(*Server).Mount`, `assets.Router`; `render.Options`, `render.Renderer`, `(*Renderer).Page`, `render.Fallback`; `middleware.ErrorWriter`, `middleware.Recoverer`, `middleware.SecurityHeaders`, `middleware.ContentSecurityPolicy`; `response.ServerError`; `templates.Templates`, `templates.Assets`; `page.Handler`, `page.New`, and its four methods. `(*Server).URL` returns `(string, error)`, which is the shape `html/template` accepts in a `FuncMap`, and is used that way in tasks 5, 7 and 8.

**One thing the executor must verify rather than assume:** whether chi's `NotFound` on the root propagates into inline groups. The spec flags this as an unverified claim about a dependency, and task 8's tests are what settle it. If the root `NotFound` turns out not to reach a path inside a group, the fix is to register it on the group too — not to redesign the surfaces.
