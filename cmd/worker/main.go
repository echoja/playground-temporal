package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"example.com/temporal-go/internal/sqliteutil"
	"example.com/temporal-go/internal/worker"
)

func main() {
	var (
		dbPath = flag.String("db", "events.db", "path to the worker sqlite database file")
		addr   = flag.String("addr", ":8082", "HTTP listen address for the worker API")
	)
	flag.Parse()

	ctx := context.Background()

	db, err := sqliteutil.Open(*dbPath)
	if err != nil {
		log.Fatalf("open worker db: %v", err)
	}
	defer db.Close()

	store := worker.NewStore(db)
	if err := store.Init(ctx); err != nil {
		log.Fatalf("init worker schema: %v", err)
	}

	builderClient := worker.NewBuilderClient()
	workerServer := worker.NewServer(store, builderClient)
	server := &http.Server{
		Addr:    *addr,
		Handler: workerServer.Router(),
	}

	// Describe the worker activity flow for clarity:
	// - Accept registrations from the builder and persist credentials locally.
	// - On sync requests, page through the builder API and insert append-only events with dedupe.
	// - Enrich events with attribution data derived from past user activity.

	appCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	workerServer.StartAutoSync(appCtx, 10*time.Minute)

	go func() {
		log.Printf("worker API listening on %s (db: %s)", *addr, *dbPath)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("worker server error: %v", err)
		}
	}()

	waitForShutdown(appCtx, server)
}

func waitForShutdown(ctx context.Context, server *http.Server) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "graceful shutdown failed: %v\n", err)
	}
}
