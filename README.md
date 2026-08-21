# Listings API

A REST API for a second-hand marketplace, written in Go against PostgreSQL and
Redis. Items carry a **condition** and a **trade-in value**, modelling the
recommerce flow that platforms like Carousell, Laku6 and Back Market run on.

It is a small service — around 1,600 lines — built to be read. Every
performance claim below has a measurement behind it in
[`docs/benchmarks.md`](docs/benchmarks.md); nothing here is asserted from
intuition.

**Stack:** Go 1.26 (stdlib `net/http`, no framework) · PostgreSQL 16 via pgx ·
Redis 7 · Docker Compose · k6

## Architecture

```mermaid
flowchart LR
    Client -->|HTTP / JSON| Mux["net/http ServeMux<br/>(method + path routing)"]
    Mux --> Handler["Handler<br/>(decode, validate, encode)"]
    Handler --> Service["Service<br/>(business rules, cache orchestration)"]
    Service <--> Redis[("Redis<br/>cache-aside")]
    Service --> Repo{{"Repository<br/>(interface)"}}
    Repo --> PG["PostgresRepository<br/>(pgx)"]
    Repo -. tests .-> Mem["MemoryRepository<br/>(in-memory fake)"]
    PG --> DB[("PostgreSQL")]
```

Each layer has one job. The service depends on the `Repository` interface rather
than on Postgres directly, and that seam is what lets the handler tests exercise
the full stack — routing, decoding, validation, service logic, error mapping —
against an in-memory fake with no database running.

Reads are deliberately split into two repository methods:

- `ListIDs` answers *which ids are on this page*
- `GetByIDs` answers *fetch these ids in one round trip*

That split is what lets the cache sit in the middle. Ids always come from
Postgres, because they define the page; bodies come from Redis where possible,
and only the misses reach the database.

## Running it

```bash
docker compose up --build -d
curl localhost:8080/healthz
```

Seed data for benchmarking (1M rows in about 4 seconds, via the COPY protocol):

```bash
go run ./cmd/seed -truncate
```

### Tests

```bash
go test -race ./...        # unit tests, no database needed
```

Integration tests run against real infrastructure and **skip silently unless
`DATABASE_URL` is set** — worth knowing, because a green `go test ./...` on its
own does not mean they ran:

```bash
DATABASE_URL="postgres://listings:listings@localhost:5432/listings?sslmode=disable" \
REDIS_URL="redis://localhost:6379/0" go test -race -v ./internal/listing/
```

## API

| Method | Path | Notes |
|---|---|---|
| `POST` | `/listings` | Create. Unknown JSON fields are rejected. |
| `GET` | `/listings` | Keyset page. `?status=` `?category=` `?cursor=` `?limit=` |
| `GET` | `/listings/{id}` | Single listing, served from cache when warm |
| `PATCH` | `/listings/{id}` | Partial update; invalidates the cache entry |
| `DELETE` | `/listings/{id}` | Invalidates the cache entry |

`price` and `trade_in_value` are whole rupiah as `int64`.

A page response carries its own cursor, so a client never has to reconstruct one
from the last element:

```json
{
  "data": [ { "id": 1000000, "title": "iPhone 13 128GB", "price": 7000000 } ],
  "next_cursor": 999951
}
```

`next_cursor` is `null` when the page was not full, which is the only reliable
end-of-pagination signal. Page size defaults to 20 and is capped at 100.

```bash
# create
curl -s localhost:8080/listings -X POST -H 'Content-Type: application/json' -d '{
  "title": "iPhone 13 128GB", "description": "Minor scratches, battery 89%",
  "category": "smartphone", "condition": "good",
  "price": 7000000, "trade_in_value": 4500000
}'

# first page, then follow next_cursor
curl -s 'localhost:8080/listings?status=active&category=smartphone&limit=10'
curl -s 'localhost:8080/listings?status=active&category=smartphone&limit=10&cursor=999951'

# mark it sold
curl -s localhost:8080/listings/1 -X PATCH -H 'Content-Type: application/json' \
  -d '{"status":"sold"}'
```

## What I got wrong

The first version of this service had a Redis cache-aside layer, keyset
pagination and a bounded goroutine worker pool, and the README called it
high-performance. Then I load-tested it.

**The list endpoint was issuing 51 SQL queries to return 50 items.** It fetched
the page's ids in one query and then resolved each id individually — a Redis
`GET` and, on a miss, a single-row `SELECT` — fanned out across a worker pool
capped at 10. The concurrency made the fan-out *simultaneous*. It did not make
it *smaller*, and it was hiding a textbook N+1 behind something that looked like
an optimization. The single-query path it replaced was still sitting in the
repository, dead, called by nothing.

Replacing the fan-out with one `MGET` for the whole page and one
`WHERE id = ANY($1)` for the misses, on 1M rows:

| | Before | After |
|---|---|---|
| SQL queries per cold page | 51 | **2** |
| Redis round trips per cold page | 50 | **1** |
| Throughput, 50 VUs | 702 req/s | **4,054 req/s** |
| p95 under load | 82.8 ms | **22.1 ms** |
| p99 under load | 109.8 ms | **29.5 ms** |

The worker pool is gone. Nothing replaced it, because nothing needed to.

Two smaller things the same exercise turned up:

**`limit` had no default and no maximum.** The repository only emitted a `LIMIT`
clause when `limit > 0`, and the handler silently swallowed parse errors — so
`GET /listings` with no parameters, or `?limit=abc`, read the entire table and
then assembled every row of it. The default request was the pathological one.

**The index I expected to need was not the index I needed.** I assumed the
filtered list query was a sequential scan. It was not: with `active` at 85% of
rows, Postgres walked the primary key backward, which already supplies
`ORDER BY id DESC`, and found 50 matches after discarding 432 rows in 0.18 ms.
What *did* collapse was a filter matching nothing recent — a category with no
current stock — which fell back to a parallel sequential scan of all 1M rows:
**78.8 ms and 19,367 buffers, repeatable by any client at will.** The composite
index on `(status, category, id DESC)` takes that to **0.081 ms and 3 buffers**
as an Index Only Scan. Full workings, including the partial-index alternative I
measured and rejected, are in [`docs/benchmarks.md`](docs/benchmarks.md).

## Design decisions

- **Two repository reads, not one.** `ListIDs` + `GetByIDs` is what makes a
  cache layer possible without an N+1. A single `List` returning whole rows
  cannot consult a per-item cache; a per-item `Get` cannot batch.
- **Keyset pagination over `OFFSET`.** Pages are addressed by
  `WHERE id < cursor ORDER BY id DESC LIMIT n`, so page 10,000 costs the same as
  page 1, and rows inserted mid-walk cannot shift a reader into re-seeing or
  skipping items the way `OFFSET` does.
- **A composite index chosen against measurements**, with the rejected
  alternative recorded rather than deleted — a choice only means something if
  the option not taken is visible.
- **Money as `int64` minor units, never `float`.** Floating point cannot
  represent currency exactly, and rounding drift on money is a real bug, not a
  rounding detail.
- **Parameterized SQL only.** The list filter is built dynamically, but user
  input travels in `args` as `$N` placeholders and never enters the query
  string.
- **Validation in two places on purpose.** The service rejects bad input early
  with a readable message; `CHECK` constraints in the schema are the backstop
  for anything that bypasses it.
- **Standard-library routing.** Go 1.22's `ServeMux` does method and path
  matching with wildcards, which is all this service needs from a router.
- **Graceful shutdown.** `SIGINT`/`SIGTERM` stop new connections and let
  in-flight requests drain before exit.

## Known limitations

Real, and stated because a portfolio service that claims to have none is lying:

- **The cache-aside layer has a stale-set race.** A slow `GET` can read from
  Postgres, have a concurrent `PATCH` write and invalidate the key underneath
  it, and then write its now-stale value back — where it survives for the
  15-minute TTL. Fixing it properly needs versioned keys or single-flight; today
  it is documented rather than solved. `Update` at least reads through the
  repository rather than the cache, so a stale read cannot be written back into
  the database.
- **No optimistic concurrency.** `PATCH` is a read-modify-write with no version
  guard, so two concurrent edits to one listing will lose one of them.
- **No authentication or ownership.** There is no `seller_id`; any caller can
  modify or delete any listing.
- **No negative caching.** A repeatedly requested missing id reaches Postgres
  every time.
- **Schema bootstrap, not migrations.** `EnsureSchema` runs idempotent DDL at
  startup. Production wants `golang-migrate` or `goose` with versioned up/down
  files.
- **No structured logging or metrics.** Cache hit rate, in particular, is not
  observable at runtime — the numbers in `docs/benchmarks.md` were obtained by
  counting statements in the Postgres log.
- **The in-memory fake holds one mutex across its whole filter-and-sort**, so
  its concurrency behaviour does not match the Postgres path. It exists to keep
  tests simple, not to model the real repository.
- **Benchmarks are single-box.** Database, cache, API and load generator all
  share one machine, so the figures are sound for before/after comparison and
  are not a capacity estimate.

## Roadmap

In rough order of what I would do next: optimistic concurrency with a version
column and a `409` on conflict; CI running the integration suite against service
containers; `log/slog` with request ids and cache hit-rate metrics; idempotency
keys on `POST`; seller ownership and authorization.
