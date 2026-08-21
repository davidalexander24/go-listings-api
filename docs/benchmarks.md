# Benchmarks

Every performance claim in the README points here. If a number is not in this
file, the README does not get to make the claim.

**Status: baseline not yet captured.** The tables below are the shape the
results take; they are filled in from a real run, never estimated.

## Test rig

| | |
|---|---|
| Machine | _TBD — CPU, RAM, OS_ |
| Postgres | `postgres:16-alpine` (Docker) |
| Redis | `redis:7-alpine` (Docker) |
| Go | _TBD — `go version`_ |
| Dataset | 1,000,000 listings, seeded with `go run ./cmd/seed` (fixed RNG seed 42) |
| Distribution | 8 categories, 85% `active` / 15% `sold`, condition weighted 5/25/50/20 |
| Load generator | k6 (`grafana/k6` image), 50 VUs, 30s |

Single-machine numbers with the database, cache, API and load generator all on
one box. They are useful for **before/after comparison of a code change**, which
is what they are used for here. They are not a capacity estimate for a real
deployment, and are not presented as one.

## Method

Reproducible from a clean checkout:

```bash
# 1. Bring up Postgres, Redis and the API
docker compose up --build -d

# 2. Seed 1M rows (fixed seed, so re-runs are comparable) and ANALYZE
go run ./cmd/seed -truncate

# 3. Cold cache: flush Redis immediately before the run
docker compose exec -T redis redis-cli FLUSHALL

# 4. List endpoint
docker run --rm -i -v "${PWD}/loadtest:/scripts" --add-host=host.docker.internal:host-gateway \
  grafana/k6 run /scripts/list.js

# 5. Warm cache: run the same script again without flushing
docker run --rm -i -v "${PWD}/loadtest:/scripts" --add-host=host.docker.internal:host-gateway \
  grafana/k6 run /scripts/list.js

# 6. Single-item reads
docker run --rm -i -v "${PWD}/loadtest:/scripts" --add-host=host.docker.internal:host-gateway \
  grafana/k6 run /scripts/get.js
```

### Counting queries per request

The query count is the headline number, so it is measured rather than reasoned
about. Postgres is asked to log every statement, one request is issued, and the
statements are counted:

```bash
docker compose exec -T db psql -U listings -d listings \
  -c "ALTER SYSTEM SET log_statement='all';" -c "SELECT pg_reload_conf();"

docker compose exec -T redis redis-cli FLUSHALL
curl -s 'localhost:8080/listings?status=active&category=smartphone&limit=50' > /dev/null

docker compose logs db --since 15s | grep -c 'statement: SELECT'

# put it back
docker compose exec -T db psql -U listings -d listings \
  -c "ALTER SYSTEM RESET log_statement;" -c "SELECT pg_reload_conf();"
```

### EXPLAIN

```bash
docker compose exec -T db psql -U listings -d listings -c \
  "EXPLAIN (ANALYZE, BUFFERS) SELECT id FROM listings
   WHERE status = 'active' AND category = 'smartphone'
   ORDER BY id DESC LIMIT 50;"
```

---

## Results

### `GET /listings?status=active&category=smartphone&limit=50`

| Run | SQL queries / request | Redis round trips / request | Throughput (req/s) | p50 | p95 | p99 |
|---|---|---|---|---|---|---|
| Baseline, cold cache | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ |
| Baseline, warm cache | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ |
| Batch read, cold cache | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ |
| Batch read, warm cache | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ |
| Batch read + index, cold | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ |
| Batch read + index, warm | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ | _TBD_ |

### `GET /listings/{id}` (80% of reads against a 1,000-id hot set)

| Run | Throughput (req/s) | p50 | p95 | p99 |
|---|---|---|---|---|
| Baseline, warm cache | _TBD_ | _TBD_ | _TBD_ | _TBD_ |
| After changes, warm cache | _TBD_ | _TBD_ | _TBD_ | _TBD_ |

This endpoint is not expected to move much — the cache-aside path on a single
`GET` is unchanged by this work. It is measured to confirm the list-path change
did not regress it.

### Index selection

Two candidates for the filtered, ordered list query. Both measured on the same
1M-row table; the losing option is recorded rather than deleted, because the
choice only means something if the alternative is visible.

```sql
-- A: composite
CREATE INDEX listings_status_category_id_desc_idx ON listings (status, category, id DESC);

-- B: partial, targeting the dominant access pattern
CREATE INDEX listings_active_category_id_desc_idx ON listings (category, id DESC) WHERE status = 'active';
```

| Index | Plan | Execution time | Shared buffers hit/read | Index size |
|---|---|---|---|---|
| None (baseline) | _TBD_ | _TBD_ | _TBD_ | — |
| A — composite | _TBD_ | _TBD_ | _TBD_ | _TBD_ |
| B — partial | _TBD_ | _TBD_ | _TBD_ | _TBD_ |

**Chosen:** _TBD_ — _reasoning once measured._

<details>
<summary>EXPLAIN (ANALYZE, BUFFERS) — before</summary>

```
TBD
```

</details>

<details>
<summary>EXPLAIN (ANALYZE, BUFFERS) — after</summary>

```
TBD
```

</details>
