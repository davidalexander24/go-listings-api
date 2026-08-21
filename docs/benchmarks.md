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

**Measurement conditions.** Every timing below was taken with Postgres statement
logging **off**, and each before/after pair was measured on the same rig in the
same session, with the "before" side obtained by checking out the pre-change
commit and dropping the index rather than by reasoning about it. The query
*counts* in the next section necessarily required `log_statement='all'`, which
is why they are reported as counts and never as timings: an early draft of this
file mixed the two and had to be re-measured.

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
| Baseline (per-id fan-out, no index) | 12.7 ms | 15.0 ms | 7.0 ms | 8.2 ms |
| Batch read + index | **5.6 ms** | **7.6 ms** | **4.9 ms** | **5.3 ms** |

The cold gap is the N+1: 50 sequential cache misses, each costing a round trip
to Redis and then to Postgres. Warm, both versions serve the page from cache and
the difference narrows to the 50-GET-vs-1-MGET round-trip cost.

Under sustained load, 50 VUs for 30s, cursor-walking 5 pages per iteration:

| Stage | Throughput | p50 | p95 | p99 | Failures |
|---|---|---|---|---|---|
| Baseline (per-id fan-out, no index) | 702 req/s | 68.4 ms | 82.8 ms | 109.8 ms | 0 |
| Batch read, no index | 3,538 req/s | 10.8 ms | 25.1 ms | 33.4 ms | 0 |
| Batch read + index | **4,054 req/s** | **9.3 ms** | **22.1 ms** | **29.5 ms** | 0 |

**5.8x throughput, p95 down 73%.** Most of it is the batch read; the index adds
a further 15% on this query, which is a bonus rather than its purpose. As the
next section explains, this particular filter was never the one the index
rescues.

## GET /listings/{id} — regression check

80% of reads against a 1,000-id hot set, 50 VUs for 30s. This path was not
changed; it is measured to confirm the list work did not regress it.

| Stage | Throughput | p50 | p95 | p99 |
|---|---|---|---|---|
| Baseline | 14,394 req/s | 2.75 ms | 6.84 ms | 10.0 ms |
| After changes | 14,238 req/s | 2.83 ms | 6.82 ms | 9.6 ms |

Unchanged within run-to-run variance, which is the result this check was looking
for. (The baseline row here was captured while statement logging was still on.
The single-item path issues one query either way, so logging barely touches it,
and the two figures agree to within 1% — but the asymmetry is noted rather than
hidden.)

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
