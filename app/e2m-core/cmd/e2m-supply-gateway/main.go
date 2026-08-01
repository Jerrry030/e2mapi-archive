package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"e2m.local/core/internal/store"
	"e2m.local/core/internal/supplygateway"
	"e2m.local/core/internal/vault"
)

func main() {
	if err := run(); err != nil {
		log.Printf("%v", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dsn := strings.TrimSpace(os.Getenv("E2M_CORE_DATABASE_URL"))
	st, err := store.NewPostgresStore(ctx, dsn)
	if err != nil {
		return fmt.Errorf("supply gateway store init: %w", err)
	}
	defer st.Close()
	v, err := vault.NewFileVault(getenv("E2M_VAULT_PATH", "/data/vault.enc"), os.Getenv("E2M_VAULT_KEY"))
	if err != nil {
		return fmt.Errorf("supply gateway vault init: %w", err)
	}
	handler, err := supplygateway.New(st, v, supplygateway.Config{Currency: getenv("E2M_SUPPLY_CURRENCY", "CNY")})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: getenv("E2M_SUPPLY_GATEWAY_ADDR", ":8081"), Handler: handler.Routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
