package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cloudhelper/manager_service/internal/adapter/controller"
	"github.com/cloudhelper/manager_service/internal/adapter/netassist"
	"github.com/cloudhelper/manager_service/internal/adapter/node"
	"github.com/cloudhelper/manager_service/internal/api"
	"github.com/cloudhelper/manager_service/internal/auth"
	"github.com/cloudhelper/manager_service/internal/config"
	"github.com/cloudhelper/manager_service/internal/logging"
)

// BuildVersion is injected at build time via -ldflags.
var BuildVersion = "dev"

func main() {
	ranAsService, err := tryRunWindowsService()
	if err != nil {
		log.Fatalf("failed to bootstrap windows service mode: %v", err)
	}
	if ranAsService {
		return
	}

	if err := runManager(nil); err != nil {
		log.Fatalf("manager_service exited with error: %v", err)
	}
}

func runManager(stop <-chan struct{}) error {
	logging.Init()
	logging.Infof("manager_service starting, version=%s", BuildVersion)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	authSvc, err := auth.NewService(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("failed to initialize auth service: %w", err)
	}

	nodeStore := node.NewStore(cfg.DataDir)
	controllerSession := controller.NewSession("http://127.0.0.1:15030")
	netAssistClient := netassist.NewClient("http://127.0.0.1:15030")
	logDir := resolveLogDir()

	router := api.NewRouter(api.RouterOptions{
		AuthSvc:           authSvc,
		NodeStore:         nodeStore,
		ControllerSession: controllerSession,
		NetAssistClient:   netAssistClient,
		BuildVersion:      BuildVersion,
		LogDir:            logDir,
	})

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serverErrCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
			return
		}
		serverErrCh <- nil
	}()

	// RQ-002: only 127.0.0.1:16033 is permitted (enforced in config.Load()).
	logging.Infof("listening on %s", cfg.ListenAddr)

	if stop == nil {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(quit)

		select {
		case err := <-serverErrCh:
			if err != nil {
				return fmt.Errorf("server error: %w", err)
			}
			logging.Infof("manager_service stopped")
			return nil
		case <-quit:
		}
	} else {
		select {
		case err := <-serverErrCh:
			if err != nil {
				return fmt.Errorf("server error: %w", err)
			}
			logging.Infof("manager_service stopped")
			return nil
		case <-stop:
		}
	}

	logging.Infof("shutting down manager_service...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logging.Errorf("graceful shutdown error: %v", err)
	}

	if err := <-serverErrCh; err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	logging.Infof("manager_service stopped")
	return nil
}

func resolveLogDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "log")
	}
	return filepath.Join(".", "log")
}
