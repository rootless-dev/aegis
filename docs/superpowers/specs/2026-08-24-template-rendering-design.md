# Template rendering

Design for the HTML surface: the package that owns the templates and assets, the
renderer, the security headers that constrain every template written after this,
and the first page. Written 2026-08-24.

Today `internal/http/response` is the only way this service writes a response and
it only speaks JSON. Nothing renders HTML, and no route serves a browser. This
document covers the machinery that changes that, plus one page — the landing at
`/` — so the machinery ships with a consumer instead of an abstraction guessed
in advance.

## Scope

In: the `internal/templates` package that owns the embedded files, the
`internal/http/render` renderer, the `internal/http/assets` fingerprinting server,
the security headers middleware and its configuration, the page surface in the
router with its own recoverer and error pages, the landing page, and the build
pipeline changes Tailwind forces.

Out, and listed under Deferred decisions: CSRF, form decoding, form validation,
HTMX itself, the admin console, login screens, themes, i18n, and every route
that is not `/`.

**Why the landing page and not the console.** The renderer needs a consumer or
its shape is a guess, and the landing is the only page that depends on nothing:
no realm in the database, no session, no form. It exercises parsing, embedding,
asset fingerprinting, the CSP, the error pages and the build pipeline end to
end, and it does it without pulling in CSRF or authentication.

## Why server-rendered at all

Decided in conversation before this document. The console is the reason an IdP is
usable, so it is a phase 1 slice rather than backlog — but a console is a client
of the Admin API, never a substitute for it, so the API still comes first.

A SPA was considered twice and dropped twice. Vue from a CDN fails outright:
on-premise installations in closed networks reach no CDN, so the login screen
would not load at all; the full Vue build compiles templates at runtime through
`new Function()` and would force `unsafe-eval` on the page where passwords are
typed; and third-party JavaScript loaded at runtime means whoever controls the
CDN controls that page. Vue with a build step solves those but brings Node and
makes the console a SPA, which contradicts the choice. A hybrid — server-rendered
public pages, Vue console — is what Keycloak does and was rejected as two stacks
for one product.

The cost accepted: the console does not become the Admin API's first client, so
the API loses that completeness proof. Contract tests replace it.

HTMX rather than Alpine for interactivity, when interactivity arrives. Alpine's
default build evaluates expressions at runtime and needs `unsafe-eval`; its CSP
build forbids expressions in directives, which pushes all logic into registered
objects — so JavaScript gets written either way, and what the framework adds on
top is reactivity the console does not yet need. The decision is asymmetric:
adding Alpine later leaves hand-written JavaScript working beside it, while
removing it means unpicking `x-` attributes from every template.

## Package layout

Four packages, each with one job:

| Package | Owns | Depends on |
|---|---|---|
| `internal/templates` | The embedded `.gohtml` files and the embedded assets. Declares two `fs.FS` and nothing else. | `embed`, `io/fs` |
| `internal/http/render` | Parsing, composition, and writing a page or fragment to a response. | `html/template`, `io/fs` |
| `internal/http/assets` | Walking the asset tree, fingerprinting it, serving it, and resolving a logical path to a fingerprinted URL. | `io/fs`, `net/http` |
| `internal/handler/page` | The handlers for the HTML surface. Today: the landing page. | `render` |

`internal/templates` sits at the same height as the planned `internal/migrations`
for the same reason: both are owners of embedded files, not infrastructure with
behaviour. Neither imports the package that consumes it.

**Handlers go in `internal/handler/<surface>`, not under `internal/http/`.**
Settled 2026-08-24, and worth recording because the repository invites the other
answer: `middleware`, `response` and `server` already live in `internal/http/`,
so a handler package looks like it belongs beside them.

It does not. Those three are **mechanism** — transport, with no business rule and
no dependency on `domain` or `service`. A handler is a **layer**: it is where a
request becomes a use case call, and by the dependency rule it imports `domain`
and `service`. Putting the entry layer inside the mechanism package leaves three
layers at the root of `internal` and the fourth one level down, mixed in with
things of a different nature — and the value of that layout was always the
dependency rule rather than the names, so a tree that hides the rule loses what
it was for.

`internal/http/` stays mechanism only. `handler/page` serves HTML now;
`handler/oidc` and `handler/admin` follow.

This is also why it is decided now: renaming one package holding one file is
free, and renaming it once there are OIDC, Admin API and console handlers is
dozens of files and every import — at which point nobody does it.

`render` and `assets` both take an `fs.FS` rather than reaching for the embed
themselves. The package knows how to render; where the files live is the
consumer's knowledge. This is the same boundary already drawn by
`Migrate(ctx, source fs.FS, opts)` and by `CertificateSource` in
`application/ports.go`.

### Directory layout

```
internal/templates/
  templates.go            the two //go:embed declarations
  tailwind/input.css      Tailwind source, committed, NOT embedded
  layouts/base.gohtml     the document shell
  pages/landing.gohtml    a full document
  pages/error.gohtml      404 and 500
  assets/
    css/app.css           Tailwind output, NOT committed
    favicon.svg           committed
```

**No empty directories.** `fragments/` and `partials/` are not created in this
slice, and neither is a placeholder `app.js`. `//go:embed` fails on a directory
that matches no files — the same rule that makes `.gitkeep` useless here — and
`template.ParseFS` returns an error when a pattern matches nothing. A directory
kept "ready" would break the build it was meant to prepare for.

The renderer globs before parsing and only passes patterns that match, so adding
`partials/` later is a new directory and nothing else.

**`input.css` lives outside `assets/`.** It is the Tailwind CLI's input, not
something browsers ask for. Inside the served tree it would be a public copy of
the build source, fingerprinted and cached for a year for no reason.

`.gohtml` rather than `.html`: GoLand recognises it as a Go template, so the
`{{ }}` actions get editor support without losing HTML highlighting.

**Templates and assets are two separate `fs.FS`.** They have opposite rules — a
template is executed and never served raw, an asset is served raw and never
executed. One filesystem serving both means the file server can hand out
`layouts/base.gohtml` as text. The package exports `Templates()` and `Assets()`,
each already narrowed with `fs.Sub`, and never the root.

## Rendering

### Composition

Layouts and pages compose; fragments stand alone, and are parsed differently.

For each file under `pages/`, the renderer builds one `*template.Template`
containing every layout, every partial, and that one page. Pages therefore cannot
collide on block names with each other, and a page that forgets to define
`content` fails at parse time rather than rendering a hole.

The convention: `layouts/base.gohtml` declares `{{define "base"}}`, each page
declares `{{define "content"}}`, and the renderer executes `"base"`.

Phase 1 has exactly one layout. When a second arrives — the login screens will
want one — choosing between them becomes part of that design. Building the
selection mechanism now would be inventing scope.

Fragments, when they arrive, parse one file at a time, with partials and no
layouts, and execute by their own name. None exist yet.

### Writing the response

The renderer executes into a buffer and only then writes the status and the body.

This is not a preference: `html/template` can fail halfway through execution — a
nil map, a method returning an error, a missing field — and rendering straight
into the `http.ResponseWriter` means a 200 already went out with half a document
under it. The same reasoning already governs `response.WriteJSON`, which
serialises before writing the status "so a failure to encode answers 500 instead
of a truncated body under a success status."

Buffers come from a `sync.Pool`; page rendering is on the hot path for every
browser request and a per-request allocation of a full document is avoidable.

### Response headers

Every rendered page carries:

- `Content-Type: text/html; charset=utf-8`.
- `Cache-Control: no-store`.

`no-store` on **every** HTML page, not only the sensitive ones. Most pages this
service will serve are sensitive by nature — a login form carries a CSRF token,
an authenticated page carries the user's data — and a shared proxy caching one
of them hands it to the next visitor. The landing page could safely be cached
and is not, because an allowlist of cacheable pages is a decision someone
eventually gets wrong. Caching is what the fingerprinted assets are for.

### When the error page itself fails

The error template can fail to execute like any other, and the handler that
discovers it is the one already answering an error. Calling the renderer again
recurses.

So the error path has a floor: if rendering `error` fails, a fixed HTML document
built into the binary is written directly, with the same status. No template, no
asset reference, no data — a title and a line of text.

This mirrors what `response.WriteJSON` already does when marshalling fails,
writing its body inline rather than calling `WriteServerError`, "which would come
back here and could recurse."

The same floor guards the recoverer's `ErrorWriter`: a panic inside template
execution must not produce a second panic on the way out.

### Signatures

```go
package render

type Options struct {
    Templates fs.FS
    Funcs     template.FuncMap
}

func New(opts Options) (*Renderer, error)

func (r *Renderer) Page(w http.ResponseWriter, status int, name string, data any) error
```

**There is no `Fragment` method in this slice.** Nothing serves a fragment yet,
and an exported method with no caller is a shape guessed in advance — the exact
thing the landing page exists to avoid. The composition rule above already
records how fragments will parse when the first one arrives.

`New` parses everything and returns an error, so a broken template fails the boot
rather than the first request that touches it. There is no `Must` variant: this
assembly already reports failures up through `New`, and `application.New` runs its
steps in order precisely so a failure is a returned error and not a panic.

`Page` returns an error because the write to the socket can fail after the
buffer is built. The caller logs it; it cannot answer twice.

**No disk reload path.** An earlier sketch had dev reading templates from disk
per request so a change could be seen without recompiling. The repository already
solves this: `air` rebuilds on change, and a rebuild regenerates the embed. The
work is one line in `.air.toml` — adding `gohtml` and `css` to `include_ext` —
instead of a second code path that diverges from production, depends on the
working directory, and only exists in one profile. Someone running `go run`
directly restarts to see a template change, which is the same as any other
source file.

## Assets

### Fingerprinting

`assets.New` walks the filesystem once at boot, hashes each file, and builds a
map from logical path (`css/app.css`) to fingerprinted path
(`/assets/<hash>/css/app.css`).

Per-file hashes rather than one build-wide hash. With two files it makes no
difference; it matters once fonts arrive, since they are the heaviest asset and
never change, and one global hash would make every deploy re-download them.
Choosing per-file now costs nothing and avoids a migration later.

Templates reach this through one function in the `FuncMap`:

```gohtml
<link rel="stylesheet" href="{{ asset "css/app.css" }}">
```

An unknown logical path makes `asset` fail rather than emit a broken URL. Since
templates are executed during tests, a typo is caught by the test suite instead
of by a browser.

### Serving

Mounted with the same shape `health` already uses — a local `Router` interface
declaring only what it needs, so the package carries no dependency on chi:

```go
type Router interface {
    Get(pattern string, handler http.HandlerFunc)
}

func (s *Server) Mount(router Router)
```

The handler splits the hash segment from the path and **verifies it matches the
hash of the file requested**. A mismatch is a 404, not a redirect: without the
check, any string in that position serves the file and the immutable caching
promise becomes a lie.

Responses carry `Cache-Control: public, max-age=31536000, immutable` and an
`ETag` built from the hash. `http.ServeContent` handles `If-None-Match` and
ranges from there. Content type comes from the file extension.

Path traversal is not reachable: lookups go through the map of known logical
paths, so anything not walked at boot does not exist.

### `/favicon.ico`

Browsers request it from the root on their own, whatever the document says. With
no route registered it falls through to the HTML 404 — a full rendered page in
response to an icon request, on every first visit.

The layout points at the fingerprinted `favicon.svg` with a `<link rel="icon">`,
and `/favicon.ico` gets its own route serving the same bytes from the asset
store — unfingerprinted, since the browser asks for that exact path and cannot be
told otherwise. `Mount` registers both, so the router does not learn a filename.

It answers with a short `max-age` rather than `immutable`: the path is fixed, so
a year-long cache on it could not be invalidated by a deploy.

### Where assets are mounted

Outside the page group, beside the probes: no request timeout tuned for
handlers, no HTML recoverer, and no request log line per stylesheet. The bare
`Recoverer` already on the root router still covers a panic here.

They carry `X-Content-Type-Options: nosniff`, which matters most precisely here —
it is what stops a browser from deciding a stylesheet is really a script.

**Corrected 2026-08-24, after the whole-branch review.** This originally read
"and nothing else", on the grounds that a CSP governs documents and has no
meaning on a `.css` response. True of the stylesheet, false of `/favicon.ico`:
that route answers `image/svg+xml`, and an SVG navigated to directly *is* a
document, one that can carry script. Harmless for an icon committed here and the
wrong default the day a tenant logo is served from the same handler, so asset
responses now also carry `Content-Security-Policy: default-src 'none'`.

## Security headers

One middleware sends four headers on the page surface.

**Content-Security-Policy:**

```
default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'
```

`default-src 'none'` rather than `'self'`: every source type must be named
deliberately, so a fetch nobody designed fails loudly instead of inheriting a
permission.

- **`script-src`, `connect-src` and `font-src` are absent**, so
  `default-src 'none'` denies all three. This slice ships no JavaScript and no
  web font, so there is nothing to allow. The first two arrive with HTMX — which
  needs `connect-src` or every `hx-get` fails — and `font-src` arrives with the
  first `@font-face`. Each is a one-line change to the constant and to the test
  that asserts it. Granting a permission nothing uses is what the reasoning under
  `img-src` below rejects.
- `frame-ancestors 'none'` — clickjacking defence. On a login screen an attacker
  framing the page and overlaying it is the classic attack; this is the header
  that stops it.
- `base-uri 'none'` — a `<base>` injected into the document would repoint every
  relative URL, the stylesheet reference included.
- `form-action 'self'` — an injection cannot retarget the form to another host,
  which on a password form is the difference between a defaced page and stolen
  credentials.
- `img-src 'self'` without `data:` — deliberately the strict form. Relaxing a CSP
  later is a one-line change; tightening one after templates depend on the slack
  is not. The same reasoning governs the three directives left out entirely.

**No `unsafe-inline` and no `unsafe-eval`.** This is the constraint that shapes
every template written from here: no `<style>` blocks, no `onclick=`, no inline
`<script>`. Retrofitting CSP after templates exist means rewriting all of them,
which is why it lands with the first page rather than after the console.

No nonce mechanism. Nonces exist to allow inline scripts; there are none, so a
nonce would be machinery threaded through every render for no gain.

**`X-Content-Type-Options: nosniff`** — stops a browser from re-interpreting a
response as a type it was not sent as.

**`Referrer-Policy: no-referrer`** — the login URLs this service will serve carry
`state` and `redirect_uri` in the query string, and the `Referer` header would
hand them to every host a page links to.

`X-Frame-Options: DENY` is also sent. It is redundant with `frame-ancestors` on
current browsers and costs one line for the ones that are not.

Deferred: `Cross-Origin-Opener-Policy` and `Cross-Origin-Resource-Policy`. They
matter once there are popups or cross-origin embedding, and neither exists yet.

## Configuration

A `csp` section, modelled on the existing `hsts` one.

| Key | Env | Type | Default | Validation |
|---|---|---|---|---|
| `csp.enabled` | `CSP_ENABLED` | bool | `true` | none |

The section is shaped like `Banner`: one `Enabled bool`, a `defaultCSP()`, and a
`Validate()` that returns nil because there is nothing a bool can get wrong.

**The directives themselves are not configurable**, for the reason already
written into `tlsConfig`: "a list pinned in configuration ages into the weakest
thing this service still accepts." An operator who can weaken the CSP of an
identity provider from a YAML file is a downgrade attack with a config file.

`nosniff`, `Referrer-Policy` and `X-Frame-Options` have no switch at all. There
is no legitimate deployment that needs them off, so there is no knob to get
wrong.

`enabled` exists only so a developer chasing something can turn it off locally.
It defaults on in every profile, including production, and there is no
`report_only`: nothing is deployed yet, so there is no rollout to stage.

## Router

`setRouter` currently builds two surfaces: the probes, mounted bare so the
orchestrator does not drown the request log, and `surfaces`, the group carrying
the full chain. This adds a third.

```
router (bare)          Recoverer(JSON) — already global, guards everything below
├── health.Mount       probes, nothing else
├── assets.Mount       nosniff only
├── group: API         RequestID, Proxy, RequestLogger, Recoverer(JSON), Timeout, HSTS
└── group: pages       RequestID, Proxy, RequestLogger, Recoverer(HTML), Timeout, HSTS, SecurityHeaders
```

The two groups share five middlewares and differ in two. The shared part is
extracted into one function that returns the common chain, and each group appends
what makes it itself — otherwise the next middleware added to one surface is
silently missing from the other.

### The recoverer has to know which surface it is on

`middleware.Recoverer` calls `response.WriteServerError`, which writes JSON. An
HTML surface that panics and answers `{"error":"server_error"}` to a browser is a
broken page with the wrong content type.

The signature grows a writer:

```go
type ErrorWriter func(w http.ResponseWriter, r *http.Request)

func Recoverer(logger *log.Logger, write ErrorWriter) Middleware
```

Two call sites in `router.go` change, plus two in `middleware_test.go`. The bare
recoverer on the root router keeps the JSON writer: it guards the probes, which
are JSON, and it is the last line for anything mounted outside a group.

**The choice comes from the route group, never from sniffing `Accept`.** A
browser and an API client send different `Accept` headers, and content
negotiation on the error path means the response format depends on a header the
caller controls — which is exactly the ambiguity to keep out of error handling.

### 404 and 405

The root `NotFound` and `MethodNotAllowed` answer **HTML**, because the root is
the browser-facing surface. A path nobody registered is far more likely to be a
person with a typo than a client calling an endpoint that does not exist.

The intent was that API branches would get their own JSON `NotFound` simply by
being mounted under a prefix with `chi.Mount` rather than declared with `Group`.

**That was a claim about chi's behaviour, it was flagged unverified here, and it
is now verified false.** chi v5 `mux.go`: `NotFound` stores the handler and then
walks every subrouter already registered, installing it on each one whose own
`notFoundHandler` is nil; `Mount` does the same for a subrouter added
afterwards. So a future `chi.Mount("/oauth2", …)` **inherits** the HTML 404 and
answers an unknown path under that prefix with a rendered page — through the
pages chain a second time, which means two request ids, two log lines, and the
API recoverer wrapping the HTML one.

The consequence for the next phase: an API branch must call `NotFound` and
`MethodNotAllowed` on its own subrouter explicitly. A prefix alone buys nothing.
This is recorded in a comment at the registration site, since that is where
someone adding the branch will be standing. Nothing in this slice depends on it:
every unknown path renders the HTML 404, and that much is tested.

## Boot

One new step in the wiring, **before `setRouter`** — the router mounts the asset
server and the landing handler needs the renderer, so both have to exist first.
It goes between `setCertificates` and `setRouter`:

1. `templates.Templates()` and `templates.Assets()` are read.
2. `assets.New` walks and fingerprints. A walk failure fails the boot.
3. **The generated stylesheet is checked.** If `css/app.css` is absent the boot
   fails, in **every** profile, with a message naming the make target.
4. `render.New` parses. A broken template fails the boot.

This is the same posture already taken for `ssl_mode` outside dev and chosen for
the issuer: refusing to start beats serving something wrong in silence. There is
no path where the service is up without its stylesheet.

**Corrected 2026-08-24, after the whole-branch review.** This section originally
had `dev` log a warning and continue, "so an unstyled page is still a working
page while someone is mid-loop". That page never existed. `layouts/base.gohtml`
resolves the stylesheet through `{{asset "css/app.css"}}`, and `asset` fails on
an unknown logical path by design — which is the property that turns a typo in a
template into a test failure. So in the exact case the warning existed to
permit, every render failed and every route answered the fallback error
document, `GET /` included. The alternative fix, making the layout tolerate a
missing stylesheet, was rejected: it would discard the typo detection as well,
and what it preserved was broken anyway.

The order matters — `render.New` needs the `asset` function, which needs the
fingerprint map, which needs the walk.

## Build pipeline

Tailwind's output is a build artifact, and `//go:embed` requires the file to
exist when `go build` runs. Left unhandled, `go build` and `go test ./...` break
for anyone who has not run Tailwind first — CI included.

Four mechanisms, each covering a different failure:

**1. The asset directory is never empty on its own.** `favicon.svg` is a source
and is committed, so `//go:embed assets` always matches at least one file and the
build never fails for the missing stylesheet alone. (A `.gitkeep` would not have
worked: `go:embed` skips names beginning with `.`, so the directory would still
count as empty.)

This is a real constraint on the layout, not a happy accident: if `favicon.svg`
is ever removed and nothing committed replaces it, the build breaks for everyone
who has not run Tailwind. Worth a comment beside the embed directive.

**2. The boot check**, above. No profile can run without the stylesheet.

**3. The production image generates it.** A separate `assets` stage in
`docker/Dockerfile.production` downloads the standalone CLI and runs it; the
builder stage copies the result in before compiling. The stage runs on
`BUILDPLATFORM` and its output is architecture-independent, so it is built once
and shared across target platforms.

Two details here are load-bearing:

- **`.dockerignore` must exclude the generated stylesheet.** It does not today,
  and `COPY . .` would therefore carry whatever a developer happens to have on
  disk into the image — a stale build, or a modified one, silently overriding
  what the `assets` stage produced. Adding it makes the image reproducible from
  the source alone.
- **The `COPY --from=assets` comes after `COPY . .`**, or the broad copy
  overwrites the generated file with nothing.

**4. `make` targets depend on it.** `build`, `test`, `test-integration` and `ci`
all depend on an `assets` target. Anyone following the Makefile never meets the
problem; anyone calling `go build` directly meets mechanism 2.

### The Tailwind CLI

The standalone CLI is a binary, which is what keeps Node out of the pipeline. It
has to be obtained:

- `make assets` downloads it into `bin/` (already git-ignored) when absent, at a
  **pinned version with a pinned checksum**, mapping `uname -s`/`uname -m` to the
  release asset name. Note `.dockerignore` keeps `bin/*` out of the build context
  except `bin/aegisd`, so a locally downloaded CLI never reaches an image. Same discipline as `GOSEC := ...@v2.28.0` in the Makefile
  today: a tool version is a dependency and gets pinned like one.
- The image does the same in its `assets` stage, on `alpine` pinned the way
  `Dockerfile.tilt` already pins `ALPINE_VERSION`, with `curl` added for the
  download. It needs no Go toolchain.

Downloading a binary at build time is a supply-chain surface, which is why the
checksum is not optional. The alternative — committing the generated CSS — was
considered and rejected: it produces a diff on every class change.

**Version to pin:** Tailwind v4, exact patch to be fixed at implementation time
against what is current. v4 is CSS-first — `@import "tailwindcss"` and `@theme`
in `input.css`, with no `tailwind.config.js`. This differs from v3 and the
pinned version must be verified rather than assumed.

### Development loop with air

`.air.toml` changes in two places: `include_ext` gains `gohtml` and `css`, and
`build.cmd` runs the Tailwind CLI before `go build`. One rebuild covers a
template change and the stylesheet it implies.

`exclude_dir` already lists `tmp`, `bin`, `docs`, `.git`, `.github`; the
generated stylesheet lives under `internal/templates/assets/css/` and would
retrigger the build that produced it. It goes in `exclude_regex`.

### Static analysis

`sonar-project.properties` declares `sonar.sources=.`, so everything in the tree
is analysed. Two new kinds of file must be excluded from analysis, not merely
from coverage:

- the generated stylesheet, which is machine output nobody edits;
- third-party JavaScript such as `htmx.min.js` when it arrives, which is
  minified and would report smells by the hundred.

This is not cosmetic. The pipeline **fails the build on a failed quality gate**,
so an un-excluded minified bundle does not produce noise — it produces a red
build that no code change can fix.

Separately, `internal/templates/templates.go` joins `sonar.coverage.exclusions`.
It is two `//go:embed` directives and two accessors with no branch in them, which
is exactly the criterion the file already states: "Anything with a decision in it
stays measured." Everything else added here — the renderer, the asset server, the
middleware, the handlers — has decisions and stays measured.

### Development loop with Tilt

`Tiltfile` compiles on the host with `local_resource('compile', ...)` and
`deps=['cmd', 'internal', ...]`.

Two changes, and the second is a trap:

1. The compile command runs the Tailwind CLI before `go build`.
2. **The generated stylesheet must be excluded from that resource's deps.** It
   lives under `internal/templates/assets/css/`, which is inside `internal` —
   so writing it retriggers the very resource that wrote it, and Tilt rebuilds
   in a loop forever. `local_resource` takes `ignore=` for this.

`docker/Dockerfile.tilt` does **not** change: it compiles nothing, receiving a
binary built on the host and synced by `live_update`. The assets it needs are
already embedded in that binary.

### CI

Every job that compiles — `test`, and the `engines` matrix which runs
`go build` — gains an assets step before it. The Tailwind download is cached by
version.

## Test strategy

**`render`** — a table over every file in `pages/`, asserting each parses and
executes against its real model. This is the canary that catches a page someone
adds without a test: it walks the filesystem rather than listing names.
Separately, contextual escaping is asserted directly — a model carrying
`<script>` and `"` rendered into element text, an attribute and an `href`, each
checked for the right escaping — because that property is the reason
`html/template` was chosen over anything else.

**`assets`** — fingerprint stability (same content, same hash), a mismatched hash
segment returning 404, `immutable` and `ETag` present, `If-None-Match` returning
304, an unknown logical path making `asset` fail.

**`middleware`** — the CSP header value asserted **literally**, and the literal
**written out in the test rather than imported from the constant**. A test that
compares the constant to itself passes no matter what the constant says, which
is the failure mode the canary tests for the forced database parameters were
written to avoid. A directive silently dropped by a refactor is the kind of
regression that only surfaces as a successful attack, so the whole string is
compared against a copy someone had to type.

`nosniff`, `Referrer-Policy` and `X-Frame-Options` likewise. Plus: with
`csp.enabled` false, the CSP header is absent and the other three are still
sent.

**Router** — `/` answers 200 with `text/html` and `no-store`; an unknown path
answers 404 with `text/html`; `/favicon.ico` answers 200 with the icon and not a
rendered 404; a handler that panics on the page surface answers 500 with
`text/html` and not JSON; the probes still answer JSON and still bypass the
request log. And the case that pins the whole error design: a panic on the API
surface still answers JSON, so the two recoverers are proven to be different.

**Boot** — and this one shapes the code. "Booting without the stylesheet fails"
cannot be tested through `application.New`, because the embed is fixed at
compile time and a test cannot remove a file from it.

So the check is a pure function over an `fs.FS` — filesystem in, error out —
exercised with `fstest.MapFS` for present and absent, over both profiles, so a
reintroduced per-profile split fails the test. The wiring calls it with the real embed. This is the same move already
made when the forty `.sql` fixtures became `fstest.MapFS` in the database
package: testable because the filesystem is a parameter.

What still goes through `application.New`: a broken template fails the boot in
both profiles, since that one needs no filesystem surgery to provoke.

**Not covered by tests, deliberately:** the checksum of the downloaded Tailwind
CLI. It is verified by the build, and asserting it again in Go would test the
Makefile from the wrong side.

## Files this touches

| File | Change |
|---|---|
| `internal/templates/templates.go` | New. The two embeds. |
| `internal/templates/layouts/base.gohtml` | New. |
| `internal/templates/pages/landing.gohtml` | New. |
| `internal/templates/pages/error.gohtml` | New. 404 and 500. |
| `internal/templates/tailwind/input.css` | New. Tailwind source, outside the served tree. |
| `internal/templates/assets/favicon.svg` | New. The committed file that keeps the asset embed non-empty. |
| `internal/http/render/render.go` | New. |
| `internal/http/assets/assets.go` | New. |
| `internal/handler/page/page.go` | New. Landing and error handlers. |
| `internal/http/middleware/security_headers.go` | New. |
| `internal/http/middleware/recoverer.go` | Signature grows an `ErrorWriter`. |
| `internal/configs/csp.go` | New. `enabled`, default true. |
| `internal/configs/application.go` | `CSP *CSP` field, `defaultCSP()` in `Default()`, and an entry in `sections()` — the three places a section is registered. |
| `internal/infra/configbuilder/env_source.go` | `applyCSP`, called after `applyHSTS` to match file order. Nil check included, as every section there has. |
| `internal/application/application.go` | New fields: renderer, assets. New wiring step. |
| `internal/application/wiring.go` | `setRenderer`, and the stylesheet check. |
| `internal/application/router.go` | Third group, shared chain extracted, assets and favicon mounted, root 404/405. |
| `Makefile` | `assets` target, CLI download with pinned version and checksum, `build`/`test`/`test-integration`/`ci` depend on it. |
| `docker/Dockerfile.production` | `assets` stage; builder copies its output. |
| `docker/Dockerfile.development` | Tailwind CLI available to air. |
| `Tiltfile` | Tailwind in the compile command; generated stylesheet in `ignore=`. |
| `.air.toml` | `include_ext`, `build.cmd`, `exclude_regex`. |
| `.github/workflows/ci.yml` | Assets step in `test` and `engines`. |
| `sonar-project.properties` | `sonar.exclusions` for generated CSS and third-party JS; `sonar.coverage.exclusions` for `templates.go`. |
| `.gitignore` | `internal/templates/assets/css/app.css`. |
| `.dockerignore` | Same path, so the image never inherits a local build. |
| `internal/http/middleware/middleware_test.go` | Two `Recoverer` call sites take the new argument; new cases for the security headers. |
| `internal/http/render/render_test.go` | New. Page table, escaping, error fallback. |
| `internal/http/assets/assets_test.go` | New. Fingerprints, cache headers, hash mismatch. |
| `internal/application/router_test.go` | New cases for the page surface, the HTML 404 and the HTML panic. |
| `internal/configs/csp_test.go` | New. Defaults and the header value. |
| `aegis.example.yaml` | `csp` section. |
| `.env.example` | `CSP_ENABLED`. |
| `docs/configuration.md` | The `csp` section. |
| `docs/architecture.md` | `## Layout` gains the new packages; `## HTTP` gains the third surface and the security headers. |
| `docs/development.md` | `## Running` and `## Hot reload` gain `make assets`, and that every profile refuses to boot without the stylesheet. |

### What deliberately does not change

- **`deploy/k8s`.** No new port, no new probe, no new volume. Templates and
  assets are inside the binary, so the read-only root filesystem the base
  hardening sets stays valid.
- **`docker/Dockerfile.tilt`**, for the reason above.
- **`internal/http/response`.** The JSON writers are untouched; the HTML surface
  gets its own package rather than growing a second personality inside that one.
- **`test/integration`.** The landing page is covered by handler and router
  tests; nothing here needs a real engine.

## Deferred decisions

- **CSRF, form decoding, form validation.** They arrive with the first form, and
  the landing page has no POST. Decided already: `justinas/nosurf` or
  `gorilla/csrf` after checking the state of that project, or a double-submit
  HMAC of roughly a hundred lines over the crypto already here.
- **HTMX and its extensions.** `response-targets` and `idiomorph` are chosen;
  nothing on the landing page issues a request, so shipping the library now would
  be a script tag with no caller.
- **The delegated listener.** No `app.js` at all. The design — one `document`
  listener routing on `data-*` attributes — is settled, but there is no
  `data-toggle` on a landing page, and an empty file would be a request per page
  view for nothing.
- **A second layout**, and how a page selects one. Arrives with the login screens.
- **Fragments.** No directory, no `Fragment` method, no parse path — only the
  rule for how they will compose when one exists.
- **`Cross-Origin-Opener-Policy`, `Cross-Origin-Resource-Policy`.**
- **What the landing page says.** Copy and visual design are not architecture.

One thing that is not deferred: `error.gohtml` serves both 404 and 500 and takes
a model carrying the status code and a short message. It never renders the error
itself — a panic message or a wrapped database error on a public page is an
information leak, and the `RevealErrors` distinction `health` already draws
exists for exactly this reason.
