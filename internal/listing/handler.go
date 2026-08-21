package listing

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Handler adapts HTTP requests to Service calls and writes JSON responses.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes wires the listing endpoints onto the mux. Go 1.22's ServeMux
// understands method + path patterns and path wildcards like {id}, so we get
// proper REST routing from the standard library, no third-party router needed.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /listings", h.create)
	mux.HandleFunc("GET /listings", h.list)
	mux.HandleFunc("GET /listings/{id}", h.get)
	mux.HandleFunc("PATCH /listings/{id}", h.update)
	mux.HandleFunc("DELETE /listings/{id}", h.delete)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req CreateListingRequest
	if !decode(w, r, &req) {
		return
	}
	created, err := h.svc.Create(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// listResponse is the wire shape of a page. The cursor is returned explicitly
// rather than left for the client to infer from the last element, which is what
// a bare array forced it to do.
type listResponse struct {
	Data       []Listing `json:"data"`
	NextCursor *int64    `json:"next_cursor"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := ListFilter{
		Status:   q.Get("status"),
		Category: q.Get("category"),
	}

	// Parse errors are rejected rather than swallowed. Silently treating
	// ?limit=abc as "unbounded" is how a typo turns into a full table read.
	if raw := q.Get("cursor"); raw != "" {
		cursor, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 0 {
			writeJSON(w, http.StatusBadRequest, errorBody{"cursor must be a non-negative integer"})
			return
		}
		f.Cursor = cursor
	}
	if raw := q.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 {
			writeJSON(w, http.StatusBadRequest, errorBody{"limit must be a positive integer"})
			return
		}
		// Above MaxLimit is clamped, not rejected: asking for too much is a
		// reasonable thing for a client to do, and the bound is documented.
		f.Limit = limit
	}

	page, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse{Data: page.Items, NextCursor: page.NextCursor})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	l, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req UpdateListingRequest
	if !decode(w, r, &req) {
		return
	}
	l, err := h.svc.Update(r.Context(), id, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- small HTTP helpers -----------------------------------------------------

// pathID parses the {id} wildcard. On a non-integer it writes 400 and returns
// ok=false so the caller can stop.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{"id must be an integer"})
		return 0, false
	}
	return id, true
}

// decode reads a JSON body into dst, rejecting unknown fields so typos in a
// request (for example "titel") fail loudly instead of being silently ignored.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{"invalid JSON body: " + err.Error()})
		return false
	}
	return true
}

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError is the one place that maps a domain error to an HTTP status code,
// so the handlers themselves stay tiny and consistent.
func writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, errorBody{ve.Msg})
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{"listing not found"})
	default:
		writeJSON(w, http.StatusInternalServerError, errorBody{"internal error"})
	}
}
