package listing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a listing id does not exist.
var ErrNotFound = errors.New("listing not found")

// Repository is the persistence port for listings. The service depends on this
// interface rather than on Postgres directly, which is what lets the tests swap
// in an in-memory fake (see repository_memory.go) and run without a database.
//
// Reads are split into ListIDs (which ids are on this page) and GetByIDs (fetch
// these ids in one round trip). That split is what lets the cache sit in the
// middle: ids come from Postgres, the bodies come from Redis where possible,
// and only the misses reach the database.
type Repository interface {
	Create(ctx context.Context, l *Listing) (*Listing, error)
	ListIDs(ctx context.Context, f ListFilter) ([]int64, error)
	Get(ctx context.Context, id int64) (*Listing, error)
	GetByIDs(ctx context.Context, ids []int64) ([]Listing, error)
	Update(ctx context.Context, l *Listing) (*Listing, error)
	Delete(ctx context.Context, id int64) error
}

// PostgresRepository is the production Repository, backed by a pgx pool.
type PostgresRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

// columns lists the selected fields once so every query stays in sync.
const columns = `id, title, description, category, condition, price, trade_in_value, status, created_at`

func scanListing(row pgx.Row) (*Listing, error) {
	var l Listing
	err := row.Scan(&l.ID, &l.Title, &l.Description, &l.Category, &l.Condition,
		&l.Price, &l.TradeInValue, &l.Status, &l.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *PostgresRepository) Create(ctx context.Context, l *Listing) (*Listing, error) {
	const q = `
INSERT INTO listings (title, description, category, condition, price, trade_in_value, status)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING ` + columns
	row := r.pool.QueryRow(ctx, q, l.Title, l.Description, l.Category, l.Condition, l.Price, l.TradeInValue, l.Status)
	return scanListing(row)
}

// ListIDs returns the ids on one keyset page, newest first.
//
// Only ids are selected: the bodies are resolved separately so the cache gets a
// chance at them first. The WHERE clause is built dynamically, but only ever
// with $N placeholders — user input travels in args, never in the string, so
// this is safe from SQL injection.
func (r *PostgresRepository) ListIDs(ctx context.Context, f ListFilter) ([]int64, error) {
	f.Normalize()

	q := `SELECT id FROM listings`
	var args []any
	var conds []string
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.Category != "" {
		args = append(args, f.Category)
		conds = append(conds, fmt.Sprintf("category = $%d", len(args)))
	}
	if f.Cursor > 0 {
		args = append(args, f.Cursor)
		conds = append(conds, fmt.Sprintf("id < $%d", len(args)))
	}

	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY id DESC"

	// Always bounded. Normalize guarantees a positive limit, so there is no
	// path here that emits an unlimited query.
	args = append(args, f.Limit)
	q += fmt.Sprintf(" LIMIT $%d", len(args))

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]int64, 0, f.Limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *PostgresRepository) Get(ctx context.Context, id int64) (*Listing, error) {
	const q = `SELECT ` + columns + ` FROM listings WHERE id = $1`
	l, err := scanListing(r.pool.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

// GetByIDs fetches many listings in a single round trip via `= ANY($1)`.
//
// Ids that do not exist are simply absent from the result; the caller decides
// what a missing row means. Order is not guaranteed and callers must not rely
// on it — the service reassembles pages from the id list it already has.
func (r *PostgresRepository) GetByIDs(ctx context.Context, ids []int64) ([]Listing, error) {
	if len(ids) == 0 {
		return []Listing{}, nil
	}
	const q = `SELECT ` + columns + ` FROM listings WHERE id = ANY($1)`
	rows, err := r.pool.Query(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Listing, 0, len(ids))
	for rows.Next() {
		var l Listing
		if err := rows.Scan(&l.ID, &l.Title, &l.Description, &l.Category, &l.Condition,
			&l.Price, &l.TradeInValue, &l.Status, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) Update(ctx context.Context, l *Listing) (*Listing, error) {
	const q = `
UPDATE listings
SET title = $1, description = $2, category = $3, condition = $4, price = $5, trade_in_value = $6, status = $7
WHERE id = $8
RETURNING ` + columns
	updated, err := scanListing(r.pool.QueryRow(ctx, q,
		l.Title, l.Description, l.Category, l.Condition, l.Price, l.TradeInValue, l.Status, l.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return updated, err
}

func (r *PostgresRepository) Delete(ctx context.Context, id int64) error {
	const q = `DELETE FROM listings WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
