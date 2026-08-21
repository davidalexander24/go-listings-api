package listing

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// cacheTTL is how long a cached listing stays valid without being touched.
// Mutations delete the key outright, so this bound only matters for the stale
// window described in the README's known limitations.
const cacheTTL = 15 * time.Minute

// Service holds the business rules for listings. It sits between the HTTP
// handler and the repository: handlers speak JSON/HTTP, the service speaks
// domain logic, the repository speaks SQL. Keeping them separate is what makes
// each piece small and independently testable.
type Service struct {
	repo Repository
	rdb  *redis.Client
}

func NewService(repo Repository, rdb *redis.Client) *Service {
	return &Service{repo: repo, rdb: rdb}
}

func (s *Service) cacheKey(id int64) string {
	return "listing:" + strconv.FormatInt(id, 10)
}

// Page is one keyset page of listings plus the cursor to fetch the next one.
// NextCursor is nil when the page was not full, which is the only reliable
// signal that there is nothing after it.
type Page struct {
	Items      []Listing
	NextCursor *int64
}

// Create validates the request, applies defaults, and persists a new listing.
func (s *Service) Create(ctx context.Context, req CreateListingRequest) (*Listing, error) {
	if err := req.Validate(); err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	l := &Listing{
		Title:        req.Title,
		Description:  req.Description,
		Category:     req.Category,
		Condition:    req.Condition,
		Price:        req.Price,
		TradeInValue: req.TradeInValue,
		Status:       StatusActive, // a new listing always starts active
	}
	return s.repo.Create(ctx, l)
}

// List returns one keyset page.
//
// The page is assembled with a fixed number of round trips regardless of page
// size: one query for the ids, one MGET for the whole page, and one query for
// whatever the cache missed. An earlier version fanned out one Get per id
// through a bounded goroutine pool, which turned a 50-item page into 51 SQL
// queries on a cold cache — see docs/benchmarks.md. Concurrency was hiding the
// fan-out, not removing it; batching removes it.
func (s *Service) List(ctx context.Context, f ListFilter) (*Page, error) {
	// Normalize here as well as in the handler so a page is bounded no matter
	// which caller constructed the filter.
	f.Normalize()

	ids, err := s.repo.ListIDs(ctx, f)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return &Page{Items: []Listing{}}, nil
	}

	byID := make(map[int64]Listing, len(ids))
	misses := s.readThroughCache(ctx, ids, byID)

	if len(misses) > 0 {
		fetched, err := s.repo.GetByIDs(ctx, misses)
		if err != nil {
			return nil, err
		}
		for _, l := range fetched {
			byID[l.ID] = l
		}
		s.cacheAll(ctx, fetched)
	}

	// Reassemble in the order ListIDs returned. A row deleted between ListIDs
	// and the fetch simply drops out of the page, which is the same behaviour
	// the per-id version had.
	items := make([]Listing, 0, len(ids))
	for _, id := range ids {
		if l, ok := byID[id]; ok {
			items = append(items, l)
		}
	}

	page := &Page{Items: items}
	// Base the cursor on what the keyset query returned, not on what survived
	// the fetch: a full page of ids means there may be more rows, even if one
	// of them was deleted before we could read it.
	if len(ids) == f.Limit {
		last := ids[len(ids)-1]
		page.NextCursor = &last
	}
	return page, nil
}

// readThroughCache fills found from Redis in a single MGET and returns the ids
// it could not serve. A cache failure is not a request failure: every id falls
// through to the database.
func (s *Service) readThroughCache(ctx context.Context, ids []int64, found map[int64]Listing) []int64 {
	if s.rdb == nil {
		return ids
	}

	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = s.cacheKey(id)
	}

	vals, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil || len(vals) != len(ids) {
		return ids
	}

	misses := make([]int64, 0, len(ids))
	for i, v := range vals {
		raw, ok := v.(string) // a missing key comes back as nil
		if !ok {
			misses = append(misses, ids[i])
			continue
		}
		var l Listing
		if json.Unmarshal([]byte(raw), &l) != nil {
			// Corrupt or stale-format entry: treat it as a miss rather than
			// serving garbage.
			misses = append(misses, ids[i])
			continue
		}
		found[l.ID] = l
	}
	return misses
}

// cacheAll writes freshly-read listings back in one pipelined round trip
// instead of one SET per listing.
func (s *Service) cacheAll(ctx context.Context, ls []Listing) {
	if s.rdb == nil || len(ls) == 0 {
		return
	}
	_, _ = s.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for _, l := range ls {
			b, err := json.Marshal(l)
			if err != nil {
				continue
			}
			p.Set(ctx, s.cacheKey(l.ID), b, cacheTTL)
		}
		return nil
	})
}

func (s *Service) Get(ctx context.Context, id int64) (*Listing, error) {
	if s.rdb != nil {
		cached, err := s.rdb.Get(ctx, s.cacheKey(id)).Bytes()
		if err == nil {
			var l Listing
			if json.Unmarshal(cached, &l) == nil {
				return &l, nil
			}
		}
	}

	l, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if s.rdb != nil {
		if b, err := json.Marshal(l); err == nil {
			s.rdb.Set(ctx, s.cacheKey(id), b, cacheTTL)
		}
	}
	return l, nil
}

// Update applies a partial update. We load the current row, overlay only the
// fields present in the request (non-nil pointers), then persist. Read-modify-
// write keeps the SQL simple at the cost of one extra read; fine at this scale.
//
// The read goes through the repository rather than through Get, deliberately:
// updating from a cached copy would let a stale read overwrite fresher data.
func (s *Service) Update(ctx context.Context, id int64, req UpdateListingRequest) (*Listing, error) {
	if err := req.Validate(); err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.Title != nil {
		current.Title = *req.Title
	}
	if req.Description != nil {
		current.Description = *req.Description
	}
	if req.Category != nil {
		current.Category = *req.Category
	}
	if req.Condition != nil {
		current.Condition = *req.Condition
	}
	if req.Price != nil {
		current.Price = *req.Price
	}
	if req.TradeInValue != nil {
		current.TradeInValue = *req.TradeInValue
	}
	if req.Status != nil {
		current.Status = *req.Status
	}
	updated, err := s.repo.Update(ctx, current)
	if err == nil && s.rdb != nil {
		s.rdb.Del(ctx, s.cacheKey(id))
	}
	return updated, err
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	err := s.repo.Delete(ctx, id)
	if err == nil && s.rdb != nil {
		s.rdb.Del(ctx, s.cacheKey(id))
	}
	return err
}

// ValidationError marks an error as caused by bad input. The handler maps it to
// HTTP 400, keeping the "which error is which status" decision in one place.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }
