package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"

	"example.com/temporal-go/internal/logging"
	"example.com/temporal-go/internal/sqliteutil"
	workersvc "example.com/temporal-go/internal/worker"
)

func main() {
	var (
		dbPath          = flag.String("db", "events.db", "path to the worker sqlite database file")
		addr            = flag.String("addr", ":8082", "HTTP listen address for the worker API")
		temporalAddress = flag.String("temporal", os.Getenv("TEMPORAL_ADDRESS"), "Temporal service address")
	)
	flag.Parse()

	baseLogger := logging.New()
	logger := baseLogger.With("component", "worker.bootstrap")

	db, err := sqliteutil.Open(*dbPath)
	if err != nil {
		logger.Error("open worker db failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	store := workersvc.NewStore(db)
	if err := store.Init(context.Background()); err != nil {
		logger.Error("init worker schema failed", "error", err)
		os.Exit(1)
	}

	builderClient := workersvc.NewBuilderClient()

	temporalHostPort := *temporalAddress
	if temporalHostPort == "" {
		temporalHostPort = client.DefaultHostPort
	}
	temporalClient, err := client.NewClient(client.Options{HostPort: temporalHostPort})
	if err != nil {
		logger.Error("connect temporal failed", "host", temporalHostPort, "error", err)
		os.Exit(1)
	}

	serverLogger := baseLogger.With("component", "worker.http")
	orchestrator := workersvc.NewTemporalOrchestrator(temporalClient, baseLogger)
	workerServer := workersvc.NewServer(store, builderClient, orchestrator, serverLogger)
	server := &http.Server{
		Addr:    *addr,
		Handler: workerServer.Router(),
	}

	syncWorker := workersvc.RegisterSyncWorker(temporalClient, workerServer, baseLogger)

	appCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	workerServer.StartAutoSync(appCtx, 10*time.Minute)

	go func() {
		serverLogger.Info("worker API listening", "addr", *addr, "db", *dbPath, "temporal", temporalHostPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverLogger.Error("worker server error", "error", err)
		}
	}()

	go func() {
		logger.Info("temporal sync worker starting", "task_queue", workersvc.SyncTaskQueue())
		if err := syncWorker.Run(temporalworker.InterruptCh()); err != nil {
			logger.Error("temporal sync worker stopped", "error", err)
		}
	}()

	waitForShutdown(appCtx, server, temporalClient, baseLogger)
}

func waitForShutdown(ctx context.Context, server *http.Server, temporalClient client.Client, logger *slog.Logger) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	} else {
		logger.Info("worker server stopped")
	}
	temporalClient.Close()
}
