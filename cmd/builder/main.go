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

	"example.com/temporal-go/internal/builder"
	"example.com/temporal-go/internal/sqliteutil"
)

func main() {
	var (
		dbPath = flag.String("db", "builder.db", "path to the builder sqlite database file")
		addr   = flag.String("addr", ":8081", "HTTP listen address for the builder API")
	)
	flag.Parse()

	ctx := context.Background()
	db, err := sqliteutil.Open(*dbPath)
	if err != nil {
		log.Fatalf("open builder db: %v", err)
	}
	defer db.Close()

	store := builder.NewStore(db)
	if err := store.Init(ctx); err != nil {
		log.Fatalf("init builder schema: %v", err)
	}

	server := &http.Server{
		Addr:    *addr,
		Handler: builder.NewServer(store).Router(),
	}

	// The builder service is a long running HTTP server; add a short comment describing the workflow for clarity.
	// 1. Accept admin requests to seed sites/users/orders in the mock builder database.
	// 2. Guard worker-facing endpoints with access-key authentication.
	// 3. Serve paginated data to the worker so sync jobs can exercise pagination logic.

	go func() {
		log.Printf("builder API listening on %s (db: %s)", *addr, *dbPath)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("builder server error: %v", err)
		}
	}()

	waitForShutdown(server)
}

func waitForShutdown(server *http.Server) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	<-sigCh

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "graceful shutdown failed: %v\n", err)
	}
}
