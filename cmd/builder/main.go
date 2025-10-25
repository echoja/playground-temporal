package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"example.com/temporal-go/internal/builder"
	"example.com/temporal-go/internal/logging"
	"example.com/temporal-go/internal/sqliteutil"
)

func main() {
	var (
		dbPath = flag.String("db", "builder.db", "path to the builder sqlite database file")
		addr   = flag.String("addr", ":8081", "HTTP listen address for the builder API")
	)
	flag.Parse()

	ctx := context.Background()
	logger := logging.New()

	db, err := sqliteutil.Open(*dbPath)
	if err != nil {
		logger.Error("open builder db failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	store := builder.NewStore(db)
	if err := store.Init(ctx); err != nil {
		logger.Error("init builder schema failed", "error", err)
		os.Exit(1)
	}

	serverLogger := logger.With("component", "builder.http")
	server := &http.Server{
		Addr:    *addr,
		Handler: builder.NewServer(store, serverLogger).Router(),
	}

	// The builder service is a long running HTTP server; add a short comment describing the workflow for clarity.
	// 1. Accept admin requests to seed sites/users/orders in the mock builder database.
	// 2. Guard worker-facing endpoints with access-key authentication.
	// 3. Serve paginated data to the worker so sync jobs can exercise pagination logic.

	go func() {
		serverLogger.Info("builder API listening", "addr", *addr, "db", *dbPath)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverLogger.Error("builder server error", "error", err)
		}
	}()

	waitForShutdown(serverLogger, server)
}

func waitForShutdown(logger *slog.Logger, server *http.Server) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	<-sigCh

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		return
	}
	logger.Info("builder server stopped")
}
