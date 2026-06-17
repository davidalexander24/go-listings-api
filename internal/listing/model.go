package listing

import (
	"errors"
	"strings"
	"time"
)

// Condition reflects how worn a second-hand item is. The set is fixed because
// trade-in pricing downstream depends on a known, closed vocabulary.
type Condition string

const (
	ConditionNew     Condition = "new"
	ConditionLikeNew Condition = "like_new"
	ConditionGood    Condition = "good"
	ConditionFair    Condition = "fair"
)

func (c Condition) valid() bool {
	switch c {
	case ConditionNew, ConditionLikeNew, ConditionGood, ConditionFair:
		return true
	default:
		return false
	}
}

// Status is the lifecycle of a listing.
type Status string

const (
	StatusActive Status = "active"
	StatusSold   Status = "sold"
)

func (s Status) valid() bool {
	return s == StatusActive || s == StatusSold
}

// Listing is a single second-hand item offered for sale or trade-in.
//
// Price and TradeInValue are whole-rupiah int64 (see db.schemaDDL for why money
// is never a float here). The struct tags control the JSON field names.
type Listing struct {
	ID           int64     `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Category     string    `json:"category"`
	Condition    Condition `json:"condition"`
	Price        int64     `json:"price"`
	TradeInValue int64     `json:"trade_in_value"`
	Status       Status    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

// CreateListingRequest is the accepted body for POST /listings.
type CreateListingRequest struct {
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Category     string    `json:"category"`
	Condition    Condition `json:"condition"`
	Price        int64     `json:"price"`
	TradeInValue int64     `json:"trade_in_value"`
}

// Validate enforces the same invariants the database guards, but earlier and
// with friendlier messages, so a bad request never reaches the DB.
func (r CreateListingRequest) Validate() error {
	if strings.TrimSpace(r.Title) == "" {
		return errors.New("title is required")
	}
	if !r.Condition.valid() {
		return errors.New("condition must be one of: new, like_new, good, fair")
	}
	if r.Price < 0 {
		return errors.New("price must be >= 0")
	}
	if r.TradeInValue < 0 {
		return errors.New("trade_in_value must be >= 0")
	}
	return nil
}

// UpdateListingRequest is the accepted body for PATCH /listings/{id}. Every
// field is a pointer and therefore optional: a nil pointer means "leave this
// field unchanged". Pointers are how we tell "field omitted" apart from a real
// zero value (for example, price 0 vs. price not sent).
type UpdateListingRequest struct {
	Title        *string    `json:"title"`
	Description  *string    `json:"description"`
	Category     *string    `json:"category"`
	Condition    *Condition `json:"condition"`
	Price        *int64     `json:"price"`
	TradeInValue *int64     `json:"trade_in_value"`
	Status       *Status    `json:"status"`
}

func (r UpdateListingRequest) Validate() error {
	if r.Title != nil && strings.TrimSpace(*r.Title) == "" {
		return errors.New("title cannot be blank")
	}
	if r.Condition != nil && !r.Condition.valid() {
		return errors.New("condition must be one of: new, like_new, good, fair")
	}
	if r.Status != nil && !r.Status.valid() {
		return errors.New("status must be one of: active, sold")
	}
	if r.Price != nil && *r.Price < 0 {
		return errors.New("price must be >= 0")
	}
	if r.TradeInValue != nil && *r.TradeInValue < 0 {
		return errors.New("trade_in_value must be >= 0")
	}
	return nil
}

// ListFilter holds the optional query filters for GET /listings.
type ListFilter struct {
	Status   string
	Category string
	Cursor   int64 // Exclusive cursor for keyset pagination
	Limit    int   // Maximum number of items to return
}
