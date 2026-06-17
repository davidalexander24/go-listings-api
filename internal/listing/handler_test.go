package listing

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// newTestServer builds the full handler stack on top of an in-memory repo, so
// these tests exercise routing, JSON decoding, the service, and the error
// mapping together, without needing a running database.
func newTestServer() *http.ServeMux {
	svc := NewService(NewMemoryRepository(), nil)
	h := NewHandler(svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func TestCreateAndGetListing(t *testing.T) {
	mux := newTestServer()

	body := `{"title":"iPhone 13","category":"phone","condition":"good","price":7000000,"trade_in_value":4500000}`
	rec := do(mux, http.MethodPost, "/listings", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d (%s)", rec.Code, rec.Body.String())
	}

	var created Listing
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.ID == 0 {
		t.Fatalf("expected a generated id, got 0")
	}
	if created.Status != StatusActive {
		t.Fatalf("new listing should be active, got %q", created.Status)
	}

	getRec := do(mux, http.MethodGet, "/listings/"+strconv.FormatInt(created.ID, 10), "")
	if getRec.Code != http.StatusOK {
		t.Fatalf("get: want 200, got %d", getRec.Code)
	}
}

func TestCreateListingValidation(t *testing.T) {
	mux := newTestServer()
	// Missing title (and a bad condition / negative price) must be rejected.
	rec := do(mux, http.MethodPost, "/listings", `{"condition":"mint","price":-5}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetMissingListing(t *testing.T) {
	mux := newTestServer()
	rec := do(mux, http.MethodGet, "/listings/999", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// do is a tiny request helper to keep the tests readable.
func do(mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, bytes.NewBufferString(body))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}
