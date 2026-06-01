package listing

import "context"

// Service holds the business rules for listings. It sits between the HTTP
// handler and the repository: handlers speak JSON/HTTP, the service speaks
// domain logic, the repository speaks SQL. Keeping them separate is what makes
// each piece small and independently testable.
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
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
	return s.repo.List(ctx, f)
}

func (s *Service) Get(ctx context.Context, id int64) (*Listing, error) {
	return s.repo.Get(ctx, id)
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
	return s.repo.Update(ctx, current)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.repo.Delete(ctx, id)
}

// ValidationError marks an error as caused by bad input. The handler maps it to
// HTTP 400, keeping the "which error is which status" decision in one place.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }
