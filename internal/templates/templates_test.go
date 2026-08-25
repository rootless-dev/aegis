package templates_test

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/rootless-dev/aegis/internal/http/assets"
	"github.com/rootless-dev/aegis/internal/templates"
)

// inlineOffences are what the policy forbids by refusing unsafe-inline. The CSP
// does not fail the build, it fails the page in a browser, quietly.
//
// Matched against the template source rather than rendered output, so the scan
// also covers pages that have no handler yet.
var inlineOffences = []struct {
	pattern *regexp.Regexp
	offence string
}{
	{regexp.MustCompile(`(?i)<style[\s>]`), "an inline <style> block"},
	{regexp.MustCompile(`(?i)<script[\s>]`), "an inline <script>"},
	{regexp.MustCompile(`(?i)\sstyle\s*=`), "a style= attribute"},
	// Event handler attributes. The whitespace anchor is what keeps data-on-…
	// from matching.
	{regexp.MustCompile(`(?i)\son[a-z]{3,}\s*=`), "an inline event handler"},
}

// The canary for the constraint the CSP puts on every template written from
// here: retrofitting one after the templates exist means rewriting all of them.
func TestNoTemplateBreaksTheContentSecurityPolicy(t *testing.T) {
	files := templates.Templates()

	var scanned int

	err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || filepath.Ext(name) != ".gohtml" {
			return nil
		}

		content, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}

		scanned++

		for _, offence := range inlineOffences {
			if match := offence.pattern.Find(content); match != nil {
				t.Errorf("%s carries %s (%q); the policy sends no unsafe-inline, so it would be blocked in a browser", name, offence.offence, match)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walking the templates: %v", err)
	}

	// A walk that matched nothing would pass silently.
	if scanned == 0 {
		t.Fatal("no templates were scanned")
	}
}

func TestTheAssetEmbedIsNotEmpty(t *testing.T) {
	assetFS, err := templates.Assets()
	if err != nil {
		t.Fatalf("Assets: %v", err)
	}

	// go:embed fails on a directory matching no files, and the stylesheet is
	// generated rather than committed, so favicon.svg is what keeps assets/
	// non-empty.
	if _, err := fs.Stat(assetFS, assets.Favicon); err != nil {
		t.Errorf("%s must be committed: it is what keeps the asset embed valid (%v)", assets.Favicon, err)
	}
}

func TestTemplatesAreNotServedAsAssets(t *testing.T) {
	assetFS, err := templates.Assets()
	if err != nil {
		t.Fatalf("Assets: %v", err)
	}

	// Two filesystems, or the file server hands out layouts/base.gohtml as text.
	if _, err := fs.Stat(assetFS, "layouts/base.gohtml"); err == nil {
		t.Error("a template is reachable through the asset filesystem")
	}

	// And the Tailwind input is not a served asset either.
	if _, err := fs.Stat(assetFS, "input.css"); err == nil {
		t.Error("the tailwind input must live outside the served tree")
	}
}

// The embed directive lists its directories by hand, and render globs one —
// partials — that nothing satisfies yet. A partial added on disk without being
// added to the directive is invisible in every check that exists: render skips
// a pattern matching no file, the boot passes, the scan above never reads it,
// and the page fails at execute on the first request that reaches it.
func TestEveryTemplateOnDiskIsEmbedded(t *testing.T) {
	files := templates.Templates()

	var found int

	// The working directory of a test is its package directory, which is what
	// the embed paths are relative to.
	err := filepath.WalkDir(".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || filepath.Ext(name) != ".gohtml" {
			return nil
		}

		found++
		embedded := filepath.ToSlash(name)

		if _, err := fs.Stat(files, embedded); err != nil {
			t.Errorf("%s is not reachable through the embed: add its directory to the go:embed directive in templates.go (%v)", embedded, err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walking the package directory: %v", err)
	}

	if found == 0 {
		t.Fatal("no templates were found on disk")
	}
}
