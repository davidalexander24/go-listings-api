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
type Repository interface {
	Create(ctx context.Context, l *Listing) (*Listing, error)
	List(ctx context.Context, f ListFilter) ([]Listing, error)
	ListIDs(ctx context.Context, f ListFilter) ([]int64, error)
	Get(ctx context.Context, id int64) (*Listing, error)
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

func (r *PostgresRepository) List(ctx context.Context, f ListFilter) ([]Listing, error) {
	// Build the WHERE clause dynamically, but only ever with $N placeholders.
	// User input goes through args, never string concatenation, so this is
	// safe from SQL injection.
	q := `SELECT ` + columns + ` FROM listings`
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
	
	if f.Limit > 0 {
		args = append(args, f.Limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Start from an empty (non-nil) slice so the JSON response is [] not null.
	listings := make([]Listing, 0)
	for rows.Next() {
		var l Listing
		if err := rows.Scan(&l.ID, &l.Title, &l.Description, &l.Category, &l.Condition,
			&l.Price, &l.TradeInValue, &l.Status, &l.CreatedAt); err != nil {
			return nil, err
		}
		listings = append(listings, l)
	}
	return listings, rows.Err()
}

func (r *PostgresRepository) ListIDs(ctx context.Context, f ListFilter) ([]int64, error) {
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
	
	if f.Limit > 0 {
		args = append(args, f.Limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]int64, 0)
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
