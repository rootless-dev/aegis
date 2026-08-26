package page_test

import (
	"bytes"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/handler/page"
	"github.com/rootless-dev/aegis/internal/http/assets"
	"github.com/rootless-dev/aegis/internal/http/render"
	"github.com/rootless-dev/aegis/internal/templates"
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

// fallbackMarker is a sentence only render.Fallback writes. It produces the
// same status and content type as a real page, so every test below has to name
// something only its own page produces.
const fallbackMarker = "The request could not be completed."

// pageHandlers names the handler that renders each embedded page. The canary
// below walks pages/ rather than reading this map, so a page added without an
// entry fails rather than going unexecuted.
//
// Method expressions rather than a table of models: every page has to be
// executed against the model its handler actually builds, since a superset
// struct would satisfy a template referencing a field no real model has.
var pageHandlers = map[string]func(*page.Handler, http.ResponseWriter, *http.Request){
	"landing": (*page.Handler).Landing,
	"error":   (*page.Handler).NotFound,
}

// Runs against the real embedded templates and the real asset server, which is
// what catches a layout referencing an asset that does not exist: `asset` fails
// on an unknown logical path and the handler falls back.
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

	handler := page.New(renderer, discardLogger())

	pages, err := fs.Glob(templates.Templates(), "pages/*.gohtml")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}

	if len(pages) == 0 {
		t.Fatal("no pages were embedded")
	}

	for _, file := range pages {
		name := strings.TrimSuffix(path.Base(file), ".gohtml")

		t.Run(name, func(t *testing.T) {
			serve, ok := pageHandlers[name]
			if !ok {
				t.Fatalf("%s has no handler registered in pageHandlers: every page is executed here against the model its handler builds, so a new page needs an entry", file)
			}

			recorder := httptest.NewRecorder()
			serve(handler, recorder, httptest.NewRequest(http.MethodGet, "/", nil))

			body := recorder.Body.String()

			if !strings.Contains(body, "<!doctype html>") {
				t.Errorf("%s did not produce a document:\n%s", file, body)
			}

			// The fallback is a document too, so the check above passes against
			// a render that failed.
			if strings.Contains(body, fallbackMarker) {
				t.Errorf("%s rendered the fallback document instead of the page:\n%s", file, body)
			}
		})
	}
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

	body := recorder.Body.String()

	if strings.Contains(body, fallbackMarker) {
		t.Fatalf("the landing handler answered with the fallback document:\n%s", body)
	}

	// The landing model's own fields, rendered through the landing template.
	for _, want := range []string{"<h1>Aegis</h1>", "Identity for every realm."} {
		if !strings.Contains(body, want) {
			t.Errorf("the landing page is missing %q:\n%s", want, body)
		}
	}
}

func TestNotFoundRendersHTMLWithTheRightStatus(t *testing.T) {
	recorder := httptest.NewRecorder()

	newHandler(t, workingTemplates()).NotFound(recorder, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}

	body := recorder.Body.String()

	if strings.Contains(body, fallbackMarker) {
		t.Fatalf("the 404 answered with the fallback document:\n%s", body)
	}

	for _, want := range []string{"<h1>404</h1>", "That page does not exist."} {
		if !strings.Contains(body, want) {
			t.Errorf("the error page is missing %q:\n%s", want, body)
		}
	}
}

func TestMethodNotAllowedRendersHTML(t *testing.T) {
	recorder := httptest.NewRecorder()

	newHandler(t, workingTemplates()).MethodNotAllowed(recorder, httptest.NewRequest(http.MethodPost, "/", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}

	body := recorder.Body.String()

	if strings.Contains(body, fallbackMarker) {
		t.Fatalf("the 405 answered with the fallback document:\n%s", body)
	}

	for _, want := range []string{"<h1>405</h1>", "That method is not allowed here."} {
		if !strings.Contains(body, want) {
			t.Errorf("the error page is missing %q:\n%s", want, body)
		}
	}
}

func TestServerErrorNeverLeaksTheCause(t *testing.T) {
	recorder := httptest.NewRecorder()

	newHandler(t, workingTemplates()).ServerError(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}

	body := recorder.Body.String()

	// A panic message on a public page is an information leak.
	if strings.Contains(body, "panic") || strings.Contains(body, "sql") {
		t.Errorf("the error page leaked internals:\n%s", body)
	}

	if strings.Contains(body, fallbackMarker) {
		t.Fatalf("the 500 answered with the fallback document:\n%s", body)
	}

	if !strings.Contains(body, "<h1>500</h1>") {
		t.Errorf("the error page is missing its status:\n%s", body)
	}
}

// The floor: whoever finds the error template failed is already answering an
// error, so rendering again would recurse.
func TestFallsBackWhenTheErrorTemplateItselfFails(t *testing.T) {
	broken := workingTemplates()
	// Parses fine and fails at execution: a method the model does not have.
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

// brokenPipe is a client that went away mid-response. httptest.ResponseRecorder
// cannot fail a write, so the failure has to be injected.
type brokenPipe struct {
	header http.Header
	status int
	writes int
}

func (b *brokenPipe) Header() http.Header {
	if b.header == nil {
		b.header = make(http.Header)
	}

	return b.header
}

func (b *brokenPipe) WriteHeader(status int) { b.status = status }

func (b *brokenPipe) Write([]byte) (int, error) {
	b.writes++

	return 0, errors.New("connection reset by peer")
}

// The one failure the fallback must not repair: the status is already out, so
// falling back would set headers on a committed response and append a second
// document under the first.
func TestAWriteFailureIsNotAnsweredTwice(t *testing.T) {
	var logs bytes.Buffer

	renderer, err := render.New(render.Options{Templates: workingTemplates()})
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	handler := page.New(renderer, &log.Logger{Writer: log.IOWriter{Writer: &logs}})

	writer := &brokenPipe{}
	handler.Landing(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	// A second attempt means Fallback ran over a committed response.
	if writer.writes != 1 {
		t.Errorf("the response was written %d times, want 1", writer.writes)
	}

	// A page nobody received is not a page that succeeded.
	if !strings.Contains(logs.String(), "rendering a page failed") {
		t.Errorf("the write failure was not logged, got: %s", logs.String())
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

// A failed render answers with the failure document, so a success status would
// tell a monitor the landing page is healthy.
func TestAFailedSuccessPageFallsBackWithAnErrorStatus(t *testing.T) {
	broken := workingTemplates()
	// Parses fine and fails at execution: a field the landing model does not have.
	broken["pages/landing.gohtml"] = &fstest.MapFile{Data: []byte(
		`{{define "content"}}{{ .Missing.Field }}{{end}}`,
	)}

	recorder := httptest.NewRecorder()

	newHandler(t, broken).Landing(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: a landing page that failed to render must not answer as a success", recorder.Code)
	}

	if !strings.Contains(recorder.Body.String(), fallbackMarker) {
		t.Errorf("the fallback document was not written:\n%s", recorder.Body.String())
	}
}
