package response_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rootless-dev/aegis/internal/http/response"
)

func TestWriteBytesUsesTheGivenContentType(t *testing.T) {
	recorder := httptest.NewRecorder()

	response.WriteBytes(recorder, http.StatusOK, "application/jwk-set+json", []byte(`{"keys":[]}`))

	if got := recorder.Header().Get("Content-Type"); got != "application/jwk-set+json" {
		t.Errorf("want the given content type, got %q", got)
	}

	if got := recorder.Body.String(); got != `{"keys":[]}` {
		t.Errorf("want the body written verbatim, got %q", got)
	}
}

func TestWriteBytesSetsNoCacheHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()

	response.WriteBytes(recorder, http.StatusOK, "text/plain", []byte("body"))

	if got := recorder.Header().Get("Cache-Control"); got != "" {
		t.Errorf("want no cache header, got %q", got)
	}
}
