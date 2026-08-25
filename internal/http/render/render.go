// Package render turns templates into HTML responses. The filesystem is a
// parameter, the same boundary the migration runner and CertificateSource draw.
package render

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sync"
)

// ErrResponseWritten marks the one failure Page reports after the status and
// the body already went out. Nothing can be answered after it; a fallback would
// set headers on a committed response and append a second document.
var ErrResponseWritten = errors.New("render: the response was already written")

const (
	// Pages supply "content"; the layout supplies the document around it.
	layoutName = "base"

	contentType = "text/html; charset=utf-8"
	// Pages here carry CSRF tokens and user data, and an allowlist of cacheable
	// ones is a decision someone eventually gets wrong. Caching is what the
	// fingerprinted assets are for.
	cacheControl = "no-store"

	// A page that renders large once would otherwise keep its buffer alive in
	// the pool for the rest of the process.
	maxPooledBuffer = 64 << 10
)

// fallbackDocument answers when rendering the error page itself failed, so it
// depends on no template, asset or data.
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
// than the first request.
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
		return nil, errors.New("render: no pages found under pages/")
	}

	renderer := &Renderer{
		pages: make(map[string]*template.Template, len(pages)),
		pool:  sync.Pool{New: func() any { return new(bytes.Buffer) }},
	}

	// One set per page: pages all define "content", and a single set would have
	// them overwrite each other.
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

// sharedPatterns skips the directories that are empty or absent: ParseFS treats
// a pattern matching no file as an error.
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

// Page renders into a buffer before touching the response: execution can fail
// halfway, and writing directly would leave a 200 with half a document under
// it. Every error but ErrResponseWritten happens before the response is
// touched, so the caller is still free to answer.
func (r *Renderer) Page(w http.ResponseWriter, status int, name string, data any) error {
	parsed, ok := r.pages[name]
	if !ok {
		return fmt.Errorf("render: unknown page %q", name)
	}

	buf := r.pool.Get().(*bytes.Buffer)
	defer func() {
		if buf.Cap() > maxPooledBuffer {
			return
		}

		buf.Reset()
		r.pool.Put(buf)
	}()

	if err := parsed.ExecuteTemplate(buf, layoutName, data); err != nil {
		return fmt.Errorf("render: executing %q: %w", name, err)
	}

	writeHeaders(w, status)

	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("%w: %q: %w", ErrResponseWritten, name, err)
	}

	return nil
}

// Fallback is the floor under the error path: whoever finds that the error
// template failed is already answering an error, so rendering again would
// recurse.
func Fallback(w http.ResponseWriter, status int) {
	writeHeaders(w, status)

	_, _ = io.WriteString(w, fallbackDocument)
}

func writeHeaders(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cacheControl)
	w.WriteHeader(status)
}
