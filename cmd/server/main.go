// Command server starts the Listings API HTTP server.
//
// The wiring here is deliberately explicit (no framework, no DI container):
// main builds the dependency graph by hand, pool -> repository -> service ->
// handler, so the flow of control is easy to follow and easy to explain.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/davidalexander24/go-listings-api/internal/db"
	"github.com/davidalexander24/go-listings-api/internal/listing"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// signal.NotifyContext gives us a context that is cancelled on Ctrl+C or
	// SIGTERM (Docker stop), which we use to shut the server down gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := getenv("DATABASE_URL", "postgres://listings:listings@localhost:5432/listings?sslmode=disable")
	redisURL := getenv("REDIS_URL", "redis://localhost:6379/0")
	port := getenv("PORT", "8080")

	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.EnsureSchema(ctx, pool); err != nil {
		return err
	}

	rdb, err := db.ConnectRedis(ctx, redisURL)
	if err != nil {
		log.Printf("warning: could not connect to redis, caching disabled: %v", err)
		rdb = nil
	} else {
		defer rdb.Close()
	}

	// Build the dependency graph: repository -> service -> handler.
	repo := listing.NewPostgresRepository(pool)
	svc := listing.NewService(repo, rdb)
	h := listing.NewHandler(svc)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	h.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Start the server in a goroutine so it can block on either a startup error
	// or the shutdown signal, whichever comes first.
	errCh := make(chan error, 1)
	go func() {
		log.Printf("listings-api listening on :%s", port)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		log.Println("shutdown signal received, draining connections")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// getenv reads an environment variable with a fallback for local runs.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
