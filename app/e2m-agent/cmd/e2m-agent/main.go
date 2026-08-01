package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentconnector "e2m.local/agent/internal/connector"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connectorEnabled := getenvBool("E2M_CONNECTOR_ENABLED", true)
	dataDir := getenv("E2M_AGENT_DATA_DIR", "/var/lib/e2m-agent")
	localUIAddr := getenv("E2M_LOCAL_UI_ADDR", "127.0.0.1:18081")
	localUIEnabled := getenvBool("E2M_LOCAL_UI_ENABLED", true)
	localUIAllowPrivatePeers := getenvBool("E2M_LOCAL_UI_ALLOW_PRIVATE_PEERS", false)
	coreURL := getenv("E2M_CORE_URL", "http://localhost:8080")

	store := agentconnector.NewLocalConfigStore(dataDir)
	defaultGatewayCfg := defaultGatewayConfigFromEnv()
	logLevel := &slog.LevelVar{}
	applyLogLevel(logLevel, defaultGatewayCfg.Runtime.LogLevel)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)
	store.SetRuntimeApply(func(settings agentconnector.LocalRuntimeSettings) {
		applyLogLevel(logLevel, settings.LogLevel)
	})
	if _, err := store.Ensure(defaultGatewayCfg); err != nil {
		slog.Warn("local gateway config init skipped", "error_code", "config_init_failed")
	} else if cfg, err := store.Load(); err == nil {
		applyLogLevel(logLevel, cfg.Runtime.LogLevel)
	}
	var conn *agentconnector.Connector
	if connectorEnabled {
		var err error
		conn, err = newOutboundConnector(dataDir, coreURL, store)
		if err != nil {
			slog.Error("connector init failed", "error_code", "connector_init_failed")
			os.Exit(1)
		}
	}

	// Bind the local configuration UI before polling Core. The minimal
	// Connector manages only this owner's gateway; upstream-intelligence upload
	// is deliberately not exposed.
	if localUIEnabled {
		tokenPath := filepath.Join(dataDir, "local-ui.token")
		token, err := agentconnector.EnsureLocalUIToken(tokenPath)
		if err != nil {
			slog.Error("local UI token init failed", "error_code", "local_ui_token_failed")
			os.Exit(1)
		}
		if err := startLocalUI(ctx, localUIAddr, token, store, defaultGatewayCfg, coreURL, localUIAllowPrivatePeers); err != nil {
			slog.Error("local UI start failed", "error_code", "local_ui_start_failed")
			os.Exit(1)
		}
		slog.Info("connector local config UI listening", "url", publicLocalUIURL(localUIAddr), "token_file", tokenPath)
	}

	if !connectorEnabled {
		slog.Info("outbound connector disabled; local configuration UI remains available")
		<-ctx.Done()
		return
	}
	slog.Info("starting outbound connector")
	conn.Run(ctx)
}

func newOutboundConnector(dataDir, coreURL string, store *agentconnector.LocalConfigStore) (*agentconnector.Connector, error) {
	identityPath := getenv("E2M_CONNECTOR_IDENTITY_FILE", filepath.Join(dataDir, agentconnector.ConnectorIdentityFilename))
	identity, err := agentconnector.ResolveConnectorIdentity(
		identityPath,
		os.Getenv("E2M_CONNECTOR_ID"),
		os.Getenv("E2M_INSTANCE_ID"),
	)
	if err != nil {
		return nil, fmt.Errorf("connector identity invalid: %w", err)
	}
	enrollTokenFile := strings.TrimSpace(os.Getenv("E2M_CONNECTOR_ENROLL_TOKEN_FILE"))
	enrollToken, err := readSecretFile(enrollTokenFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("connector enrollment token file unavailable: %w", err)
	}
	conn := agentconnector.New(agentconnector.Config{
		CoreURL:     coreURL,
		EnrollToken: enrollToken,
		TokenFile:   os.Getenv("E2M_CONNECTOR_TOKEN_FILE"),
		ConnectorID: identity.ConnectorID,
		InstanceID:  identity.InstanceID,
		ConfigStore: store,
		Version:     "0.2.0-dev",
	})
	if err := conn.Validate(); err != nil {
		return nil, fmt.Errorf("connector config invalid: %w", err)
	}
	return conn, nil
}

func defaultGatewayConfigFromEnv() agentconnector.GatewayLocalConfig {
	cfg := agentconnector.GatewayLocalConfig{
		GatewayKind: getenv("E2M_GATEWAY_KIND", "sub2api"),
	}
	cfg.Normalize()
	return cfg
}

func startLocalUI(ctx context.Context, addr, token string, store *agentconnector.LocalConfigStore, defaultCfg agentconnector.GatewayLocalConfig, coreURL string, allowPrivatePeers bool) error {
	server := &http.Server{
		Addr: addr,
		Handler: agentconnector.NewLocalAPIHandler(agentconnector.LocalAPIConfig{
			Store:             store,
			Token:             token,
			Default:           defaultCfg,
			CoreURL:           coreURL,
			AllowPrivatePeers: allowPrivatePeers,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("local UI stopped", "error_code", "local_ui_serve_failed")
		}
	}()
	return nil
}

func applyLogLevel(level *slog.LevelVar, configured agentconnector.LocalLogLevel) {
	switch configured {
	case agentconnector.LocalLogLevelError:
		level.Set(slog.LevelError)
	case agentconnector.LocalLogLevelDebug:
		level.Set(slog.LevelDebug)
	default:
		level.Set(slog.LevelInfo)
	}
}

func publicLocalUIURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "http://127.0.0.1:18081/"
	}
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	if strings.HasPrefix(addr, "0.0.0.0:") {
		addr = "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	if strings.HasPrefix(addr, "[::]:") {
		addr = "127.0.0.1:" + strings.TrimPrefix(addr, "[::]:")
	}
	return "http://" + strings.TrimRight(addr, "/") + "/"
}

func readSecretFile(path string) (string, error) {
	if path == "" {
		return "", os.ErrNotExist
	}
	before, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return "", fmt.Errorf("refusing non-regular secret file %q", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return "", fmt.Errorf("secret file %q changed while opening", path)
	}
	const maxSecretBytes = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(file, maxSecretBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxSecretBytes {
		return "", fmt.Errorf("secret file %q exceeds 64 KiB", path)
	}
	return strings.TrimSpace(string(raw)), nil
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func getenvBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
