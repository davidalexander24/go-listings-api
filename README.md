# 🚀 Listings API: High-Performance Recommerce Backend

A highly concurrent REST API for a second-hand marketplace, written in Go. Items have a **condition** and a **trade-in value**, modeling the recommerce / trade-in flow that platforms like Laku6, Back Market, and Carousell run on.

This project is built to demonstrate **scalable backend engineering**. It handles high-volume read traffic efficiently by combining a standard-library Go web server with **Redis caching**, **keyset pagination**, and **goroutine worker-pools**.

![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?style=for-the-badge&logo=go)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-v5-4169E1?style=for-the-badge&logo=postgresql)
![Redis](https://img.shields.io/badge/Redis-v9-DC382D?style=for-the-badge&logo=redis)
![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=for-the-badge&logo=docker)

## ✨ Key Features

- **Robust REST API**: Complete CRUD operations for marketplace listings.
- **High-Performance Reads**: Implements a Redis cache-aside layer, dramatically reducing database load for frequently accessed items.
- **Scalable Listing Retrieval**: Utilizes keyset pagination (cursor-based) to efficiently query massive datasets without the performance penalties of `OFFSET`.
- **Concurrent Processing**: Fan-out goroutine worker-pools asynchronously assemble response payloads, maximizing throughput.
- **Data Integrity**: Enforces strict input validation at the API layer and `CHECK` constraints at the database level. Financial values are safely stored as integer minor units (avoiding floating-point precision loss).
- **Test-Driven Reliability**: Comprehensive test suite encompassing unit tests against in-memory fakes and functional integration tests against real Dockerized infrastructure.

## 🎥 Demo

![Demo Gif](docs/demo.gif)

## 🏗 Architecture

The application strictly adheres to a **handler → service → repository** architecture, ensuring a clean separation of concerns and excellent testability.

```mermaid
flowchart LR
    Client -->|HTTP / JSON| Mux["net/http ServeMux<br/>(method + path routing)"]
    Mux --> Handler["Handler<br/>(Decode & Encode)"]
    Handler --> Service["Service<br/>(Business Logic & Worker-Pool)"]
    Service <--> Redis[("Redis<br/>(Cache-Aside)")]
    Service --> Repo{{"Repository<br/>(Interface)"}}
    Repo --> PG["PostgresRepository<br/>(pgx)"]
    Repo -. tests .-> Mem["MemoryRepository<br/>(In-Memory Fake)"]
    PG --> DB[("PostgreSQL")]
```

Each layer has one job. The service depends on the `Repository` interface rather than Postgres directly. That seam lets unit tests run the full stack against an in-memory fake, while functional integration tests run against real Dockerized infrastructure.

## 🚀 Getting Started (Run it locally)

The entire stack is containerized for a zero-friction developer experience.

```bash
# Start PostgreSQL, Redis, and the API container
docker compose up --build -d
```

The API will be available at `http://localhost:8080`. 
Check its health: `curl http://localhost:8080/healthz`.

### Testing

Run blazing-fast unit tests locally (no database needed):
```bash
go test ./...
```

Run functional integration tests against the Dockerized infrastructure:
```bash
# Ensure containers are running, then:
DATABASE_URL="postgres://listings:listings@localhost:5432/listings?sslmode=disable" REDIS_URL="redis://localhost:6379/0" go test -v ./...
```
*(On Windows PowerShell, set `$env:DATABASE_URL` and `$env:REDIS_URL` before running `go test`)*

## 📡 API Endpoints

| Method | Path             | Purpose                                  |
|--------|------------------|------------------------------------------|
| POST   | `/listings`      | Create a new listing                         |
| GET    | `/listings`      | Fetch listings (`?status=`, `?category=`, `?cursor=`, `?limit=`) |
| GET    | `/listings/{id}` | Get a specific listing (Hits Redis cache)        |
| PATCH  | `/listings/{id}` | Partial update (Invalidates cache automatically)       |
| DELETE | `/listings/{id}` | Delete a listing (Invalidates cache automatically)     |

## 💡 Example Usage

*Note: `price` and `trade_in_value` are represented in whole rupiah (`int64`).*

**1. Create a listing**

```bash
curl -s localhost:8080/listings -X POST -H 'Content-Type: application/json' -d '{
  "title": "iPhone 13 128GB",
  "description": "Minor scratches, battery 89%",
  "category": "smartphone",
  "condition": "good",
  "price": 7000000,
  "trade_in_value": 4500000
}'
```

**2. List active smartphones (with Keyset Pagination)**

```bash
curl -s 'localhost:8080/listings?status=active&category=smartphone&limit=10'
```

**3. Mark it sold** (partial update)

```bash
curl -s localhost:8080/listings/1 -X PATCH -H 'Content-Type: application/json' -d '{"status":"sold"}'
```

## 🧠 Design Decisions (The "Why")

I built this project to demonstrate deep technical understanding of backend systems, far beyond a simple CRUD app.

- **Redis Cache-Aside Layer:** Ensures ultra-low read latency for individual items (`GET /listings/{id}`). Automatic cache invalidation ensures absolute data consistency whenever a resource is mutated (`PATCH`, `DELETE`).
- **Keyset Pagination:** The `GET /listings` query fetches pages using a `cursor` (the `id`) instead of standard `OFFSET/LIMIT`. This guarantees consistent, predictable query performance (`O(1)` offset skipping) even when the database scales to millions of rows.
- **Goroutine Worker-Pools:** To handle massive throughput, the list endpoint utilizes a bounded fan-out worker-pool (`errgroup`). The database fetches a lightweight list of keys, which the worker pool concurrently resolves against Redis and the Database to assemble the payload, optimizing I/O.
- **Standard-Library Routing:** Uses Go 1.22's enhanced `ServeMux` for method and path matching, eliminating the need for heavy third-party web frameworks while maintaining clean, idiomatic code.
- **Money as `int64`, never `float`:** Floating-point numbers cannot represent currency exactly; rounding drift is a critical bug in financial systems. Using whole-integer minor units prevents this entirely.
- **Two-Tier Validation:** The Go service layer rejects bad input early with clear user-friendly messages, while the Postgres database utilizes `CHECK` constraints as an impenetrable last line of defense.
- **Parameterized SQL Only:** All dynamic queries are built using `$N` placeholders, guaranteeing 100% protection against SQL injection attacks.
- **Graceful Shutdown:** Intercepts `SIGINT`/`SIGTERM` signals to stop accepting new connections while allowing in-flight requests to complete safely before exiting.

## 🚧 Future Roadmap (Production Readiness)

- **Database Migrations:** Implement a proper migration tool (`golang-migrate` or `goose`) to manage schema evolution safely over time, replacing the current idempotent bootstrap script.
- **Optimistic Concurrency:** Implement ETag or version-based optimistic locking on `PATCH` routes to prevent "last-writer-wins" race conditions during simultaneous edits.
- **Observability:** Integrate structured request logging (method, path, status, and latency) and distributed tracing (e.g., OpenTelemetry) for deep production insights.
