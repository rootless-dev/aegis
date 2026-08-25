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

	// Every page, not only the sensitive ones: a shared proxy caching a login
	// form hands it to the next visitor.
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// The property html/template was chosen for: escaping that depends on where in
// the document the value lands.
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
	// partials/ does not exist here, and ParseFS errors on a pattern matching
	// nothing.
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

	// It answers when the error template itself failed, so it cannot depend on
	// anything that could fail.
	if strings.Contains(body, "/assets/") {
		t.Error("the fallback must not reference an asset")
	}

	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
}
