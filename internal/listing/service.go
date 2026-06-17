package listing

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

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

func (s *Service) List(ctx context.Context, f ListFilter) ([]Listing, error) {
	// Use ListIDs instead of List to fetch the IDs, then concurrently fetch
	// the full models utilizing the cache-aside layer and worker pool.
	ids, err := s.repo.ListIDs(ctx, f)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []Listing{}, nil
	}

	listings := make([]Listing, len(ids))
	g, gctx := errgroup.WithContext(ctx)
	// Bounded concurrency (worker pool pattern)
	g.SetLimit(10)

	for i, id := range ids {
		i, id := i, id
		g.Go(func() error {
			l, err := s.Get(gctx, id)
			if err != nil {
				// If a listing is deleted between ListIDs and Get, ignore it.
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			listings[i] = *l
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Filter out any empty items that were ignored
	var out []Listing
	for _, l := range listings {
		if l.ID != 0 {
			out = append(out, l)
		}
	}
	return out, nil
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
			// Cache for 15 minutes
			s.rdb.Set(ctx, s.cacheKey(id), b, 15*time.Minute)
		}
	}
	return l, nil
}

// Update applies a partial update. We load the current row, overlay only the
// fields present in the request (non-nil pointers), then persist. Read-modify-
// write keeps the SQL simple at the cost of one extra read; fine at this scale.
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
