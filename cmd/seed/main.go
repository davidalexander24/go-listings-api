// Command seed loads the listings table with synthetic data for load testing.
//
// It exists so the benchmarks in docs/benchmarks.md are reproducible: the
// numbers there mean nothing without a stated row count and distribution.
//
// Rows are inserted with pgx's CopyFrom (the PostgreSQL COPY protocol) rather
// than a loop of INSERTs. COPY streams rows in a single round trip instead of
// one per row, which is the difference between seeding a million rows in
// seconds and in an afternoon. The row source is generated lazily via
// pgx.CopyFromSlice, so the process never holds the whole dataset in memory.
//
// Usage:
//
//	go run ./cmd/seed              # default: 1,000,000 rows
//	go run ./cmd/seed -rows 50000  # smaller run
//	go run ./cmd/seed -truncate    # empty the table first
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/davidalexander24/go-listings-api/internal/db"
	"github.com/jackc/pgx/v5"
)

// The generated data mirrors the shape a recommerce marketplace actually has:
// a handful of hot categories, most listings still active, and a condition mix
// weighted toward used goods. A uniform distribution would make every index
// look equally good and hide the selectivity differences that matter.
var categories = []string{
	"smartphone", "laptop", "tablet", "camera",
	"audio", "wearable", "console", "accessory",
}

var conditions = []string{"new", "like_new", "good", "fair"}

// conditionWeights biases toward used stock: new is rare on a second-hand
// marketplace, "good" is the bulk of it.
var conditionWeights = []int{5, 25, 50, 20}

var titleNouns = []string{
	"iPhone 13", "iPhone 14 Pro", "Galaxy S22", "Pixel 7", "MacBook Air M1",
	"ThinkPad X1", "iPad Air", "Sony A7 III", "AirPods Pro", "Galaxy Watch 5",
	"PlayStation 5", "Nintendo Switch", "Kindle Paperwhite", "GoPro Hero 11",
}

var titleQualifiers = []string{
	"128GB", "256GB", "512GB", "fullset", "second", "like new",
	"garansi resmi", "no minus", "batangan", "mulus",
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	rows := flag.Int("rows", 1_000_000, "number of listings to insert")
	truncate := flag.Bool("truncate", false, "truncate the listings table before seeding")
	batch := flag.Int("batch", 50_000, "rows per COPY batch")
	flag.Parse()

	if *rows <= 0 {
		return fmt.Errorf("-rows must be positive, got %d", *rows)
	}
	if *batch <= 0 {
		return fmt.Errorf("-batch must be positive, got %d", *batch)
	}

	dsn := getenv("DATABASE_URL", "postgres://listings:listings@localhost:5432/listings?sslmode=disable")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.EnsureSchema(ctx, pool); err != nil {
		return err
	}

	if *truncate {
		log.Println("truncating listings")
		if _, err := pool.Exec(ctx, `TRUNCATE listings RESTART IDENTITY`); err != nil {
			return fmt.Errorf("truncate: %w", err)
		}
	}

	// A fixed seed keeps successive runs comparable: the same -rows value
	// produces the same table, so a benchmark re-run measures the code change
	// and not a different dataset.
	rng := rand.New(rand.NewSource(42))

	columns := []string{"title", "description", "category", "condition", "price", "trade_in_value", "status", "created_at"}
	// Spread created_at over the last two years so the data looks like it
	// accumulated rather than arriving all at once.
	base := time.Now().Add(-2 * 365 * 24 * time.Hour)

	start := time.Now()
	var inserted int64

	for inserted < int64(*rows) {
		n := *batch
		if remaining := int64(*rows) - inserted; remaining < int64(n) {
			n = int(remaining)
		}

		src := pgx.CopyFromSlice(n, func(i int) ([]any, error) {
			return generateRow(rng, base, inserted+int64(i), int64(*rows)), nil
		})

		copied, err := pool.CopyFrom(ctx, pgx.Identifier{"listings"}, columns, src)
		if err != nil {
			return fmt.Errorf("copy batch at offset %d: %w", inserted, err)
		}
		inserted += copied
		log.Printf("seeded %d/%d rows (%.0f%%)", inserted, *rows, float64(inserted)/float64(*rows)*100)
	}

	elapsed := time.Since(start)
	log.Printf("done: %d rows in %s (%.0f rows/sec)", inserted, elapsed.Round(time.Millisecond),
		float64(inserted)/elapsed.Seconds())

	// ANALYZE so the planner has fresh statistics. Without it the first
	// EXPLAIN after a bulk load can pick a plan based on an empty table, which
	// would make the before/after comparison meaningless.
	log.Println("running ANALYZE so EXPLAIN reflects the real table")
	if _, err := pool.Exec(ctx, `ANALYZE listings`); err != nil {
		return fmt.Errorf("analyze: %w", err)
	}

	return nil
}

func generateRow(rng *rand.Rand, base time.Time, i, total int64) []any {
	category := categories[rng.Intn(len(categories))]
	condition := conditions[weightedIndex(rng, conditionWeights)]

	title := fmt.Sprintf("%s %s",
		titleNouns[rng.Intn(len(titleNouns))],
		titleQualifiers[rng.Intn(len(titleQualifiers))],
	)

	// Whole rupiah, int64 — same discipline as the rest of the codebase.
	// Range roughly Rp 200k to Rp 25M, which covers the catalogue.
	price := int64(200_000 + rng.Intn(24_800_000))
	// Trade-in is always below price; the spread widens as condition degrades.
	tradeIn := price * int64(40+rng.Intn(40)) / 100

	// 85% active, 15% sold: a filtered "active" query should still touch most
	// of the table, so the index has to earn its place on the ordering too.
	status := "active"
	if rng.Intn(100) < 15 {
		status = "sold"
	}

	// created_at advances with i so ids and timestamps agree on ordering,
	// the way they would if the rows had really been inserted over time.
	offset := time.Duration(float64(i) / float64(total) * float64(2*365*24*time.Hour))
	createdAt := base.Add(offset).Add(time.Duration(rng.Intn(3600)) * time.Second)

	return []any{
		title,
		"Seeded listing for load testing. Condition " + condition + ".",
		category,
		condition,
		price,
		tradeIn,
		status,
		createdAt,
	}
}

// weightedIndex picks an index from weights with probability proportional to
// each weight.
func weightedIndex(rng *rand.Rand, weights []int) int {
	total := 0
	for _, w := range weights {
		total += w
	}
	r := rng.Intn(total)
	for i, w := range weights {
		if r < w {
			return i
		}
		r -= w
	}
	return len(weights) - 1
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
