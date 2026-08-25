package application

import (
	"bytes"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/http/assets"
)

// serverWith stands in for a build tree missing whatever is not named: assets.New
// reads a filesystem, so one that never contained a file is indistinguishable
// from the real embed missing it.
func serverWith(t *testing.T, present ...string) *assets.Server {
	t.Helper()

	files := fstest.MapFS{}

	for _, logical := range present {
		files[logical] = &fstest.MapFile{Data: []byte("content")}
	}

	server, err := assets.New(files)
	if err != nil {
		t.Fatalf("assets.New: %v", err)
	}

	return server
}

func appForProfile(profile configs.Profile) *Application {
	var logs bytes.Buffer

	return &Application{
		cfg:    &configs.Application{Profile: profile},
		logger: &log.Logger{Writer: log.IOWriter{Writer: &logs}},
	}
}

// Both profiles are named, so reintroducing a per-profile split — dev warning
// and continuing — fails here instead of shipping a service that answers the
// fallback error document on every route.
func TestVerifyAssetsRefusesInEveryProfile(t *testing.T) {
	profiles := map[string]configs.Profile{
		"production":  configs.ProfileProd,
		"development": configs.ProfileDev,
	}

	for name, profile := range profiles {
		t.Run(name, func(t *testing.T) {
			err := appForProfile(profile).verifyAssets(serverWith(t, assets.Favicon))
			if err == nil {
				t.Fatal("want an error when the stylesheet is missing, got nil")
			}

			if !strings.Contains(err.Error(), assets.Stylesheet) {
				t.Errorf("error = %q, want it to name %q", err.Error(), assets.Stylesheet)
			}
		})
	}
}

// Every asset the layout resolves, not only the stylesheet: the asset function
// fails the same way on either one.
func TestVerifyAssetsRefusesAnyMissingLayoutAsset(t *testing.T) {
	cases := map[string]struct {
		present []string
		missing string
		// The stylesheet is generated and the favicon is committed, so only one
		// of them has `make assets` as its answer.
		wantHint bool
	}{
		"stylesheet missing": {present: []string{assets.Favicon}, missing: assets.Stylesheet, wantHint: true},
		"favicon missing":    {present: []string{assets.Stylesheet}, missing: assets.Favicon, wantHint: false},
		"both missing":       {present: nil, missing: assets.Favicon, wantHint: true},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			err := appForProfile(configs.ProfileDev).verifyAssets(serverWith(t, testCase.present...))
			if err == nil {
				t.Fatalf("want an error when %q is missing, got nil", testCase.missing)
			}

			if !strings.Contains(err.Error(), testCase.missing) {
				t.Errorf("error = %q, want it to name %q", err.Error(), testCase.missing)
			}

			if hinted := strings.Contains(err.Error(), "make assets"); hinted != testCase.wantHint {
				t.Errorf("error = %q, want `make assets` mentioned = %v", err.Error(), testCase.wantHint)
			}
		})
	}
}

// The other half: without this, the tests above would also hold for a function
// that always fails.
func TestVerifyAssetsAcceptsACompleteAssetTree(t *testing.T) {
	server := serverWith(t, assets.Stylesheet, assets.Favicon)

	if err := appForProfile(configs.ProfileDev).verifyAssets(server); err != nil {
		t.Fatalf("want no error when every asset is present, got %v", err)
	}
}
