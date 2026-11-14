// Command browser-platform runs the private Platform API.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/mutex-ai/mutexai-browser-platform/src/internal/config"
	"github.com/mutex-ai/mutexai-browser-platform/src/internal/keycloak"
	"github.com/mutex-ai/mutexai-browser-platform/src/partners"
	"github.com/mutex-ai/mutexai-browser-platform/src/server"
	"github.com/mutex-ai/mutexai-browser-platform/src/storage/postgres"
	"github.com/mutex-ai/mutexai-browser-platform/src/storage/schema"
	api "github.com/mutex-ai/mutexai-browser-platform/src/web_api"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	if len(os.Args) > 2 || (len(os.Args) == 2 && os.Args[1] != "migrate") {
		return errors.New("usage: browser-platform [migrate]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	level, err := zapcore.ParseLevel(cfg.LogLevel)
	if err != nil {
		return errors.New("invalid LOG_LEVEL")
	}
	logConfig := zap.NewProductionConfig()
	logConfig.Level = zap.NewAtomicLevelAt(level)
	log, err := logConfig.Build()
	if err != nil {
		return fmt.Errorf("initialize logger: %w", err)
	}
	defer func() { _ = log.Sync() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	db, err := postgres.Open(connectCtx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		return err
	}
	// Deferred before server shutdown so active HTTP requests drain first.
	defer func() { runErr = errors.Join(runErr, db.Close()) }()
	migrationCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err = schema.Migrate(migrationCtx, db)
	cancel()
	if err != nil {
		return err
	}
	if len(os.Args) == 2 {
		log.Info("PostgreSQL migrations complete")
		return nil
	}
	kcConfig, err := config.LoadKeycloak()
	if err != nil {
		return err
	}
	kc, err := keycloak.New(kcConfig)
	if err != nil {
		return err
	}
	defer kc.CloseIdleConnections()
	provisioner, err := partners.NewProvisioner(kc, config.LoadPartnerSetup())
	if err != nil {
		return err
	}
	store := partners.NewStore(db)
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); partners.NewWorker(store, provisioner, log).Run(workerCtx) }()
	defer func() { stopWorker(); <-workerDone }()
	srv, err := server.New(server.Options{Cfg: cfg.Server, Handler: api.NewRouter(db, log, store), Log: log})
	if err != nil {
		return err
	}
	srv.Start()
	defer func() { runErr = errors.Join(runErr, srv.Stop()) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-srv.Err():
		return err
	}
}
