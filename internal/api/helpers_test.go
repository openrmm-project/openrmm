package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/p1ngu/rmm/internal/store"
)

func TestOneOf(t *testing.T) {
	if !oneOf("native", "native", "meshcentral") {
		t.Error("oneOf should accept a listed value")
	}
	if oneOf("vnc", "native", "meshcentral") {
		t.Error("oneOf should reject an unlisted value")
	}
	if oneOf("", "native") {
		t.Error("oneOf should reject empty when not listed")
	}
}

func TestHTTPStatusFor(t *testing.T) {
	if got := httpStatusFor(store.ErrNotFound); got != http.StatusNotFound {
		t.Errorf("not-found mapped to %d, want 404", got)
	}
	if got := httpStatusFor(errors.New("boom")); got != http.StatusBadRequest {
		t.Errorf("generic error mapped to %d, want 400", got)
	}
}
