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

// TestListPaginationWalksEveryRowExactlyOnce is the property that matters for
// keyset pagination and was previously untested: walking the cursor to
// exhaustion must yield every row once, in descending id order, with no
// overlap across the page boundary and nothing skipped.
func TestListPaginationWalksEveryRowExactlyOnce(t *testing.T) {
	mux := newTestServer()

	const total = 7
	for i := 0; i < total; i++ {
		rec := do(mux, http.MethodPost, "/listings",
			`{"title":"item","category":"phone","condition":"good","price":100,"trade_in_value":50}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed %d: want 201, got %d", i, rec.Code)
		}
	}

	seen := make([]int64, 0, total)
	cursor := int64(0)
	for page := 0; page < total+2; page++ { // +2 guards against an infinite walk
		target := "/listings?limit=2"
		if cursor > 0 {
			target += "&cursor=" + strconv.FormatInt(cursor, 10)
		}
		rec := do(mux, http.MethodGet, target, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: want 200, got %d", page, rec.Code)
		}

		var got listResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("page %d: decode: %v", page, err)
		}
		for _, l := range got.Data {
			seen = append(seen, l.ID)
		}
		if got.NextCursor == nil {
			break
		}
		cursor = *got.NextCursor
	}

	if len(seen) != total {
		t.Fatalf("walked %d rows, want %d (%v)", len(seen), total, seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] >= seen[i-1] {
			t.Fatalf("ids not strictly descending at %d: %v", i, seen)
		}
	}
}

// TestListEmptyResultHasNoCursor pins the end-of-pagination signal: an empty
// page must not hand back a cursor that would restart the walk.
func TestListEmptyResultHasNoCursor(t *testing.T) {
	mux := newTestServer()

	rec := do(mux, http.MethodGet, "/listings?category=nothing-matches-this", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var got listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data) != 0 {
		t.Fatalf("want no items, got %d", len(got.Data))
	}
	if got.NextCursor != nil {
		t.Fatalf("empty page must not carry a cursor, got %d", *got.NextCursor)
	}
	// Must serialize as [] and not null, so clients can iterate unconditionally.
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"data":[]`)) {
		t.Fatalf("empty data should encode as [], got %s", rec.Body.String())
	}
}

// TestListLimitBounds covers the page-size policy. The unbounded case is the
// one that mattered: before DefaultLimit existed, no limit meant no LIMIT
// clause, and the endpoint read the whole table.
func TestListLimitBounds(t *testing.T) {
	mux := newTestServer()
	for i := 0; i < 3; i++ {
		do(mux, http.MethodPost, "/listings",
			`{"title":"item","category":"phone","condition":"good","price":100,"trade_in_value":50}`)
	}

	t.Run("rejects a non-numeric limit", func(t *testing.T) {
		rec := do(mux, http.MethodGet, "/listings?limit=abc", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("rejects a zero or negative limit", func(t *testing.T) {
		for _, v := range []string{"0", "-1"} {
			rec := do(mux, http.MethodGet, "/listings?limit="+v, "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("limit=%s: want 400, got %d", v, rec.Code)
			}
		}
	})

	t.Run("rejects a bad cursor", func(t *testing.T) {
		rec := do(mux, http.MethodGet, "/listings?cursor=abc", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("clamps an oversized limit to MaxLimit", func(t *testing.T) {
		f := ListFilter{Limit: MaxLimit * 10}
		f.Normalize()
		if f.Limit != MaxLimit {
			t.Fatalf("want limit clamped to %d, got %d", MaxLimit, f.Limit)
		}
	})

	t.Run("applies DefaultLimit when none is given", func(t *testing.T) {
		var f ListFilter
		f.Normalize()
		if f.Limit != DefaultLimit {
			t.Fatalf("want default %d, got %d", DefaultLimit, f.Limit)
		}
	})
}
