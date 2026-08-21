# Benchmarks

Every performance claim in the README points here. If a number is not in this
file, the README does not get to make the claim.

Measured 2026-08-22 against 1,000,000 seeded rows.

## Test rig

| | |
|---|---|
| CPU | AMD Ryzen 7 4800HS (8C/16T), 16 CPUs visible to Docker |
| Memory | 8 GB allocated to the Docker VM |
| Host | Windows 11, Docker Desktop 29.3.1 (Linux containers) |
| Postgres | 16.15 (`postgres:16-alpine`), default configuration |
| Redis | `redis:7-alpine`, default configuration |
| Go | 1.26.3 |
| Dataset | 1,000,000 listings via `go run ./cmd/seed` (fixed RNG seed 42), 173 MB heap |
| Distribution | 8 categories uniform, 85% `active` / 15% `sold`, condition weighted 5/25/50/20 |
| Load generator | k6, 50 VUs, 30s, in a container on the same Docker network |

Database, cache, API and load generator all share one machine. These numbers are
useful for **before/after comparison of a code change**, which is the only thing
they are used for here. They are not a capacity estimate for a real deployment
and should not be read as one.

## Method

Reproducible from a clean checkout:

```bash
docker compose up --build -d
go run ./cmd/seed -truncate          # 1M rows via COPY, then ANALYZE

docker compose exec -T redis redis-cli FLUSHALL          # cold cache
docker run --rm -i --network go-listings-api_default \
  -v "$PWD/loadtest:/scripts" -e BASE_URL=http://api:8080 \
  -e VUS=50 -e DURATION=30s grafana/k6 run /scripts/list.js
```

### Counting queries per request

The query count is the headline number, so it is measured rather than reasoned
about. Note that pgx uses the extended query protocol, so statements appear in
the Postgres log as `execute <stmtcache_...>:`, not as `statement:`.

```bash
docker compose exec -T db psql -U listings -d listings \
  -c "ALTER SYSTEM SET log_statement='all';" -c "SELECT pg_reload_conf();"

count() { docker compose logs db | grep -cE 'LOG: +execute [^:]*: *SELECT'; }
docker compose exec -T redis redis-cli FLUSHALL
BEFORE=$(count)
curl -s 'localhost:8080/listings?status=active&category=smartphone&limit=50' -o /dev/null
sleep 3; echo "queries: $(( $(count) - BEFORE ))"

docker compose exec -T db psql -U listings -d listings \
  -c "ALTER SYSTEM RESET log_statement;" -c "SELECT pg_reload_conf();"
```

---

## Headline: the list endpoint was doing 51 queries to serve 50 items

`GET /listings` fetched a page of ids in one query, then resolved each id
individually through a bounded goroutine pool: one Redis GET and, on a miss, one
single-row `SELECT` per item. The worker pool made the fan-out concurrent; it
did not make it smaller.

Replacing it with one `MGET` for the page and one `WHERE id = ANY($1)` for the
misses:

| | Before | After | |
|---|---|---|---|
| SQL queries, cold cache (`limit=50`) | **51** | **2** | -96% |
| SQL queries, warm cache | 1 | 1 | unchanged |
| Redis round trips, cold cache | 50 | 1 | -98% |

## GET /listings?status=active&category=smartphone&limit=50

Single-request latency, 20 samples, Redis flushed before each cold sample:

| Stage | Cold p50 | Cold p95 | Warm p50 | Warm p95 |
|---|---|---|---|---|
| Baseline (per-id fan-out) | 13.2 ms | 15.2 ms | 7.1 ms | 7.7 ms |
| Batch read | 6.1 ms | 7.9 ms | 5.2 ms | 5.4 ms |
| Batch read + index | **6.0 ms** | **6.9 ms** | **4.9 ms** | 6.4 ms |

Under sustained load, 50 VUs for 30s, cursor-walking 5 pages per iteration:

| Stage | Throughput | p50 | p95 | p99 | Failures |
|---|---|---|---|---|---|
| Baseline (per-id fan-out) | 680 req/s | 70.8 ms | 84.0 ms | 127.1 ms | 0 |
| Batch read | 3,407 req/s | 11.3 ms | 26.3 ms | 34.7 ms | 0 |
| Batch read + index | **3,559 req/s** | **10.6 ms** | **25.5 ms** | **34.1 ms** | 0 |

**5.2x throughput, p95 down 70%.** Almost all of it comes from the batch read;
the index adds about 4% here because, as the next section explains, this query
was never the one the index rescues.

## GET /listings/{id} — regression check

80% of reads against a 1,000-id hot set, 50 VUs for 30s. This path was not
changed; it is measured to confirm the list work did not regress it.

| Stage | Throughput | p50 | p95 | p99 |
|---|---|---|---|---|
| Baseline | 14,394 req/s | 2.75 ms | 6.84 ms | 10.0 ms |
| After changes | 13,923 req/s | 2.87 ms | 7.13 ms | 10.0 ms |

Within run-to-run variance on a single-box rig. Treat this as unchanged rather
than as a 3% regression: the difference is smaller than the spread between
repeat runs.

## Index selection

The assumption going in was that the filtered list query was a sequential scan
crying out for an index. **It was not**, and measuring first is the only reason
that was caught:

```
Limit (actual time=0.042..0.148 rows=50 loops=1)
  -> Index Scan Backward using listings_pkey on listings
     Filter: ((status = 'active') AND (category = 'smartphone'))
     Rows Removed by Filter: 432
     Buffers: shared hit=18
Execution Time: 0.178 ms
```

With `smartphone` at 1/8 of rows and `active` at 85%, the planner walks the
primary key backward — which already supplies `ORDER BY id DESC` — and finds 50
matches after discarding 432 rows. There is nothing to fix on this path.

The case that *does* fall over is a filter matching nothing recent, which on a
real marketplace means a category with no current stock, a newly added category,
or a typo:

```
-- WHERE status='active' AND category='drone'   (no such category)
Limit (actual time=71.426..78.705 rows=0 loops=1)
  -> Parallel Seq Scan on listings
     Rows Removed by Filter: 333333 (per worker; 2 workers plus leader)
     Buffers: shared hit=13330 read=6037
Execution Time: 78.808 ms
```

Every such request reads the entire 173 MB table, 6,037 buffers of it from disk.
It is also trivially repeatable by any client, which makes it a denial-of-service
vector and not merely a slow query.

Two candidate indexes, both measured on the same 1M-row table:

| | A — composite | B — partial |
|---|---|---|
| Definition | `(status, category, id DESC)` | `(category, id DESC) WHERE status='active'` |
| Size | 41 MB | 28 MB |
| Common filter | Index Only Scan, 0.127 ms, 4 buffers | Index Only Scan, 0.192 ms, 5 buffers |
| No-match filter | Index Only Scan, 0.083 ms, 3 buffers | Index Only Scan, 0.111 ms, 3 buffers |
| `status='sold'` filter | Index Only Scan | **falls back to PK scan**, 0.706 ms, 76 buffers |

**Chosen: A.** B is 13 MB smaller and would be tempting if `active` were the
only status ever queried, but it leaves every `status='sold'` query on the
primary-key scan path — and that path degrades over time, because sold listings
accumulate at the old end of the table, where a backward scan reaches them last.
Paying 13 MB to keep both statuses on an index is the right trade.

Both candidates produce **Index Only Scans**: the list query selects only `id`,
so the heap is never touched (`Heap Fetches: 0`).

After index A, on the query that took 78.8 ms:

```
Limit (actual time=0.043..0.044 rows=0 loops=1)
  -> Index Only Scan using listings_status_category_id_desc_idx
     Index Cond: ((status = 'active') AND (category = 'drone'))
     Heap Fetches: 0
     Buffers: shared read=3
Execution Time: 0.081 ms
```

**78.8 ms to 0.081 ms; 19,367 buffers to 3.** That is what the index is for, and
it is not what it was originally reached for.

## Page bounds

`limit` had no default and no maximum, and the repository emitted a `LIMIT`
clause only when `limit > 0`. So the default request — `GET /listings` with no
parameters — read every row in the table and then assembled all of them.

| Request | Before | After |
|---|---|---|
| `GET /listings` | entire table | 20 items (`DefaultLimit`) |
| `GET /listings?limit=abc` | HTTP 200, entire table | HTTP 400 |
| `GET /listings?limit=5000` | 5,000 items | 100 items (`MaxLimit`) |
