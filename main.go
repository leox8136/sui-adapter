package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sui-adapter/internal/adapter"
	"sui-adapter/internal/config"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("Invalid configuration", "error", err)
		os.Exit(1)
	}

	transportCredentials := insecure.NewCredentials()
	if cfg.GRPCTLS {
		transportCredentials = credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: cfg.GRPCServerName,
		})
	}

	connectContext, connectCancel := context.WithTimeout(context.Background(), cfg.RequestTimeout)
	defer connectCancel()
	connection, err := grpc.DialContext(
		connectContext,
		cfg.GRPCTarget,
		grpc.WithTransportCredentials(transportCredentials),
		grpc.WithBlock(),
	)
	if err != nil {
		logger.Error("Failed to connect gRPC client", "target", cfg.GRPCTarget, "error", err)
		os.Exit(1)
	}
	defer connection.Close()

	rpcHandler := adapter.NewHandler(
		adapter.NewSuiBackend(connection),
		logger,
		cfg.RequestTimeout,
		cfg.MaxBodyBytes,
	)

	mux := http.NewServeMux()
	mux.Handle("/sui", rpcHandler)
	mux.Handle("/sui/", rpcHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.RequestTimeout + 2*time.Second,
		WriteTimeout:      cfg.RequestTimeout + 2*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("Sui JSON-RPC adapter listening",
			"address", cfg.ListenAddress,
			"grpc_target", cfg.GRPCTarget,
			"grpc_tls", cfg.GRPCTLS,
		)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			os.Exit(1)
		}
	}()

	shutdownSignal, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-shutdownSignal.Done()

	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		logger.Error("HTTP server shutdown failed", "error", err)
	}
}
