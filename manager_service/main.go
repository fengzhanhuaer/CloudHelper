package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cloudhelper/manager_service/internal/adapter/node"
	"github.com/cloudhelper/manager_service/internal/api"
	"github.com/cloudhelper/manager_service/internal/auth"
	"github.com/cloudhelper/manager_service/internal/config"
	"github.com/cloudhelper/manager_service/internal/logging"
)

// BuildVersion is injected at build time via -ldflags.
var BuildVersion = "dev"

func main() {
	logging.Init()
	logging.Infof("manager_service starting, version=%s", BuildVersion)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	authSvc, err := auth.NewService(cfg.DataDir)
	if err != nil {
		log.Fatalf("failed to initialize auth service: %v", err)
	}

	nodeStore := node.NewStore(cfg.DataDir)
	logDir := resolveLogDir()

	router := api.NewRouter(api.RouterOptions{
		AuthSvc:      authSvc,
		NodeStore:    nodeStore,
		BuildVersion: BuildVersion,
		LogDir:       logDir,
	})

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// RQ-002: only 127.0.0.1:16033 is permitted (enforced in config.Load()).
	logging.Infof("listening on %s", cfg.ListenAddr)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logging.Errorf("server error: %v", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logging.Infof("shutting down manager_service...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logging.Errorf("graceful shutdown error: %v", err)
	}
	logging.Infof("manager_service stopped")
}

func resolveLogDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "log")
	}
	return filepath.Join(".", "log")
}

