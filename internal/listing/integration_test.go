package listing

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/davidalexander24/go-listings-api/internal/db"
)

func TestIntegration_ListingAPI(t *testing.T) {
	// Only run if DATABASE_URL is provided, allowing it to easily run locally or in CI
	// against the Docker Compose setup.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("Skipping integration test: DATABASE_URL not set")
	}
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		// Use a local default if possible, or skip if you strictly require it
		redisURL = "redis://localhost:6379/0"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("failed to connect to db: %v", err)
	}
	defer pool.Close()

	if err := db.EnsureSchema(ctx, pool); err != nil {
		t.Fatalf("failed to ensure schema: %v", err)
	}

	rdb, err := db.ConnectRedis(ctx, redisURL)
	if err != nil {
		t.Logf("warning: redis connection failed, proceeding without cache: %v", err)
		rdb = nil
	} else {
		defer rdb.Close()
	}

	repo := NewPostgresRepository(pool)
	svc := NewService(repo, rdb)
	h := NewHandler(svc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Helper function for making requests
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		var req *http.Request
		if body != nil {
			b, _ := json.Marshal(body)
			req = httptest.NewRequest(method, path, bytes.NewReader(b))
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 1. Create a Listing
	createReq := CreateListingRequest{
		Title:        "Integration Test Item",
		Description:  "This is a test",
		Category:     "electronics",
		Condition:    ConditionNew,
		Price:        500000,
		TradeInValue: 300000,
	}
	rec := request(http.MethodPost, "/listings", createReq)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create failed: expected 201, got %d. Body: %s", rec.Code, rec.Body.String())
	}
	var created Listing
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("failed to unmarshal created listing: %v", err)
	}

	id := created.ID
	idStr := strconv.FormatInt(id, 10)

	// 2. Get the Listing (Cache Miss, writes to Cache)
	rec = request(http.MethodGet, "/listings/"+idStr, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("Get failed: expected 200, got %d", rec.Code)
	}

	// 3. Update the Listing
	newTitle := "Integration Test Item - Updated"
	updateReq := UpdateListingRequest{
		Title: &newTitle,
	}
	rec = request(http.MethodPatch, "/listings/"+idStr, updateReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("Update failed: expected 200, got %d. Body: %s", rec.Code, rec.Body.String())
	}

	// 4. Get the Listing again (Verify update)
	rec = request(http.MethodGet, "/listings/"+idStr, nil)
	var updated Listing
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Title != newTitle {
		t.Fatalf("expected title %q, got %q", newTitle, updated.Title)
	}

	// 5. List items with pagination (Test worker pool / keyset pagination)
	rec = request(http.MethodGet, "/listings?limit=5", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("List failed: expected 200, got %d", rec.Code)
	}
	var listResp []Listing
	json.Unmarshal(rec.Body.Bytes(), &listResp)
	if len(listResp) == 0 {
		t.Fatalf("expected at least one listing in list response")
	}

	// 6. Delete the Listing
	rec = request(http.MethodDelete, "/listings/"+idStr, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Delete failed: expected 204, got %d", rec.Code)
	}

	// 7. Get the Listing (Verify deletion)
	rec = request(http.MethodGet, "/listings/"+idStr, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after deletion, got %d", rec.Code)
	}
}
