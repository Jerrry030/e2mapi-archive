package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
	cpaadapter "e2m.local/core/internal/adapters/cpa"
	newapiadapter "e2m.local/core/internal/adapters/newapi"
	subadapter "e2m.local/core/internal/adapters/sub2api"
	"e2m.local/core/internal/auth"
	"e2m.local/core/internal/health"
	"e2m.local/core/internal/httpapi"
	"e2m.local/core/internal/keyproof"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/operationalmetrics"
	"e2m.local/core/internal/orchestrator"
	"e2m.local/core/internal/publish"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/supplygateway"
	"e2m.local/core/internal/vault"
)

const coreShutdownTimeout = 75 * time.Second

type coreWorker func(context.Context)

func main() {
	if err := run(); err != nil {
		log.Printf("%v", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := getenv("E2M_CORE_ADDR", ":8080")
	backend := getenv("E2M_CORE_STORE", "memory")

	st, err := buildStore(ctx, backend)
	if err != nil {
		return fmt.Errorf("e2m-core store init failed: %w", err)
	}
	closeStore := storeCloseFunc(st)
	defer func() {
		// Ownership moves to serveCore once workers start. If initialization
		// fails before then, no worker can still be using the store.
		if closeStore != nil {
			closeStore()
		}
	}()

	// Credential vault. E2M_CORE_VAULT=file uses the AES-256-GCM encrypted file
	// backend (E2M_VAULT_KEY + E2M_VAULT_PATH); default is the in-process
	// MemoryVault for local development.
	v, err := buildVault()
	if err != nil {
		return fmt.Errorf("e2m-core vault init failed: %w", err)
	}

	// Core queues typed domain tasks only. Gateway endpoints, authentication,
	// and native HTTP mappings stay in the outbound connector runtime.
	adapterSet := map[contracts.InstanceKind]adapters.GatewayAdapter{
		contracts.InstanceKindSub2API: subadapter.New(st),
		contracts.InstanceKindNewAPI:  newapiadapter.New(st),
		contracts.InstanceKindCPA:     cpaadapter.New(st),
	}
	orch := orchestrator.New(st, adapterSet)

	// Notifier: route-selected system Feishu/QQ plus per-user Vault webhooks.
	router := buildNotifyRouter()
	router.SetSecretResolver(v)
	router.SetDeliveryStore(st)

	// Console realtime event stream (SSE).
	events := httpapi.NewEventBus()

	// The legacy health checker remains an observation source. Its old gateway
	// writer is disabled unless E2M_LEGACY_HEALTH_AUTOSWITCH=true is explicit.
	checker := health.New(healthConfig(), st, orch, router)
	checker.SetEventSink(func(eventType string, userID int64, payload any) {
		events.Publish(httpapi.StreamEvent{Type: eventType, UserID: userID, Payload: payload})
	})
	workers := []coreWorker{checker.Run, notify.NewWorker(st, router, notificationDeliveryInterval()).Run}

	// Console auth: bootstrap the first platform admin from env when the user
	// table is empty.
	authSvc := auth.NewService(st)
	authSvc.ConfigureRegistration(auth.ParseRegistrationConfigFromEnv(os.Getenv))
	if settings, err := st.GetAuthSystemSettings(ctx); err == nil {
		authSvc.ConfigureRegistration(auth.RegistrationConfigFromSystemSettings(settings, authSvc.RegistrationConfig()))
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Printf("auth: load persisted registration settings failed: %v", err)
	}
	if err := authSvc.Bootstrap(ctx, os.Getenv("E2M_ADMIN_EMAIL"), os.Getenv("E2M_ADMIN_PASSWORD")); err != nil {
		return fmt.Errorf("e2m-core auth bootstrap failed: %w", err)
	}

	// Random-challenge HMAC verification proves that the Connector-local binding
	// matches Core's owner-delivery key without moving either plaintext value.
	keyVerifier := keyproof.New(st, v, orch)
	publisher := publish.New(st, orch, publish.WithDeliveryKeyVerifier(keyVerifier))
	server := httpapi.NewServer(st, orch, checker, nil, nil, authSvc, events, publisher)
	server.SetVault(v)
	server.SetDeliveryKeyVerifier(keyVerifier)
	server.SetNotifier(reconcileNotifier{store: st, router: router})
	server.SetNotificationRouter(router)
	server.SetPublicBaseURL(os.Getenv("E2M_CORE_PUBLIC_URL"))
	if getenv("E2M_CORE_DEV_CORS", "") == "true" {
		server.EnableDevCORS()
	}
	if getenv("E2M_SUPPLY_ALLOW_INSECURE_UPSTREAMS", "") == "true" || getenv("E2M_ALLOW_INSECURE_SUPPLY_UPSTREAMS", "") == "true" {
		server.EnableInsecureSupplyUpstreams()
	}
	supplyHandler, err := supplygateway.New(st, v, supplygateway.Config{Currency: getenv("E2M_SUPPLY_CURRENCY", "CNY")})
	if err != nil {
		return fmt.Errorf("e2m-core supply gateway init failed: %w", err)
	}
	rootHandler := mountCoreRoutes(server.Routes(), supplyHandler.Routes())

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("e2m-core listen on %s failed: %w", addr, err)
	}
	httpServer := &http.Server{
		Addr:    addr,
		Handler: rootHandler,
	}
	var metricsServer *http.Server
	var metricsListener net.Listener
	if metricsAddr := strings.TrimSpace(os.Getenv("E2M_METRICS_ADDR")); metricsAddr != "" {
		metricsListener, err = net.Listen("tcp", metricsAddr)
		if err != nil {
			_ = listener.Close()
			return fmt.Errorf("e2m-core metrics listen on %s failed: %w", metricsAddr, err)
		}
		metricsMux := http.NewServeMux()
		metricsMux.Handle("GET /metrics", operationalmetrics.New(st))
		metricsServer = &http.Server{Addr: metricsAddr, Handler: metricsMux}
		log.Printf("starting e2m-core metrics on %s", metricsAddr)
	}

	log.Printf("starting e2m-core on %s (store=%s)", addr, backend)
	lifecycleCloseStore := closeStore
	closeStore = nil
	return serveCoreWithMetrics(ctx, httpServer, listener, metricsServer, metricsListener, workers, lifecycleCloseStore, coreShutdownTimeout)
}

// mountCoreRoutes keeps the platform data plane and the control console in one
// E2M process. The narrow OpenAI-compatible route wins over the console SPA
// fallback without changing any existing control-plane API route.
func mountCoreRoutes(console, supply http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", supply)
	mux.Handle("/", console)
	return mux
}

// serveCore owns the live process lifecycle after the TCP listener is bound.
// It cancels request and worker contexts together, then closes a close-capable
// store only after both HTTP and every worker have stopped.
func serveCore(
	parentCtx context.Context,
	server *http.Server,
	listener net.Listener,
	workers []coreWorker,
	closeStore func(),
	shutdownTimeout time.Duration,
) error {
	return serveCoreWithMetrics(parentCtx, server, listener, nil, nil, workers, closeStore, shutdownTimeout)
}

func serveCoreWithMetrics(
	parentCtx context.Context,
	server *http.Server,
	listener net.Listener,
	metricsServer *http.Server,
	metricsListener net.Listener,
	workers []coreWorker,
	closeStore func(),
	shutdownTimeout time.Duration,
) error {
	if shutdownTimeout <= 0 {
		shutdownTimeout = coreShutdownTimeout
	}
	processCtx, cancelProcess := context.WithCancel(parentCtx)
	defer cancelProcess()
	server.BaseContext = func(net.Listener) context.Context { return processCtx }

	var workersWG sync.WaitGroup
	workersWG.Add(len(workers))
	for _, worker := range workers {
		worker := worker
		go func() {
			defer workersWG.Done()
			worker(processCtx)
		}()
	}

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- server.Serve(listener)
	}()
	metricsErrCh := make(chan error, 1)
	metricsEnabled := metricsServer != nil && metricsListener != nil
	if metricsEnabled {
		metricsServer.BaseContext = func(net.Listener) context.Context { return processCtx }
		go func() { metricsErrCh <- metricsServer.Serve(metricsListener) }()
	}

	var resultErr error
	serveFinished := false
	metricsFinished := false
	select {
	case <-parentCtx.Done():
	case err := <-serveErrCh:
		serveFinished = true
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			resultErr = fmt.Errorf("e2m-core stopped: %w", err)
		}
	case err := <-metricsErrCh:
		metricsFinished = true
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			resultErr = fmt.Errorf("e2m-core metrics stopped: %w", err)
		}
	}

	cancelProcess()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()

	shutdownErrCh := make(chan error, 1)
	go func() {
		shutdownErrCh <- server.Shutdown(shutdownCtx)
	}()
	metricsShutdownErrCh := make(chan error, 1)
	if metricsEnabled {
		go func() { metricsShutdownErrCh <- metricsServer.Shutdown(shutdownCtx) }()
	}
	workersDoneCh := make(chan struct{})
	go func() {
		workersWG.Wait()
		close(workersDoneCh)
	}()

	serveWait := serveErrCh
	if serveFinished {
		serveWait = nil
	}
	shutdownWait := shutdownErrCh
	var metricsServeWait <-chan error
	var metricsShutdownWait <-chan error
	if metricsEnabled && !metricsFinished {
		metricsServeWait = metricsErrCh
	}
	if metricsEnabled {
		metricsShutdownWait = metricsShutdownErrCh
	}
	workersWait := workersDoneCh
	var shutdownErr error
	var metricsShutdownErr error
	timedOut := false
	for serveWait != nil || metricsServeWait != nil || shutdownWait != nil || metricsShutdownWait != nil || workersWait != nil {
		select {
		case <-serveWait:
			serveWait = nil
		case <-metricsServeWait:
			metricsServeWait = nil
		case shutdownErr = <-shutdownWait:
			shutdownWait = nil
		case metricsShutdownErr = <-metricsShutdownWait:
			metricsShutdownWait = nil
		case <-workersWait:
			workersWait = nil
		case <-shutdownCtx.Done():
			timedOut = true
			serveWait = nil
			metricsServeWait = nil
			shutdownWait = nil
			metricsShutdownWait = nil
			workersWait = nil
		}
	}

	if timedOut {
		// Force the listener/connections closed so process exit is not held up.
		// The store is deliberately left to the OS because a stuck worker may
		// still be executing against it.
		_ = server.Close()
		if metricsServer != nil {
			_ = metricsServer.Close()
		}
		return errors.Join(resultErr, fmt.Errorf("e2m-core shutdown timed out after %s", shutdownTimeout))
	}
	if shutdownErr != nil {
		// Shutdown errors do not prove all handlers have returned. Force-close
		// HTTP, but keep the store alive until process exit for any handler that
		// is still unwinding.
		_ = server.Close()
		return errors.Join(resultErr, fmt.Errorf("e2m-core HTTP shutdown failed: %w", shutdownErr))
	}
	if metricsShutdownErr != nil {
		_ = metricsServer.Close()
		return errors.Join(resultErr, fmt.Errorf("e2m-core metrics shutdown failed: %w", metricsShutdownErr))
	}
	if closeStore != nil {
		closeStore()
	}
	return resultErr
}

func storeCloseFunc(st store.Store) func() {
	closer, ok := st.(interface{ Close() })
	if !ok {
		return nil
	}
	return closer.Close
}

// buildNotifyRouter wires the system Feishu + QQ channels from env. Either may
// be absent; generic webhook routes resolve their per-user target from Vault.
func buildNotifyRouter() *notify.Router {
	var feishu notify.Notifier
	if url := os.Getenv("E2M_FEISHU_WEBHOOK"); url != "" {
		feishu = notify.NewFeishu(url, os.Getenv("E2M_FEISHU_SECRET"))
	}
	var qq notify.Notifier
	if url := os.Getenv("E2M_QQ_ONEBOT_URL"); url != "" {
		gid, _ := strconv.ParseInt(os.Getenv("E2M_QQ_GROUP_ID"), 10, 64)
		qq = notify.NewQQ(url, os.Getenv("E2M_QQ_TOKEN"), gid)
	}
	return notify.NewRouter(feishu, qq)
}

func notificationDeliveryInterval() time.Duration {
	if value := strings.TrimSpace(os.Getenv("E2M_NOTIFICATION_DELIVERY_INTERVAL")); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return time.Second
}

type reconcileNotifier struct {
	store  store.Store
	router *notify.Router
}

func (n reconcileNotifier) Dispatch(ctx context.Context, userID int64, ev notify.Event) {
	if n.router == nil {
		return
	}
	routes, err := n.store.ListNotificationRoutes(ctx, userID)
	if err != nil {
		log.Printf("notify: list routes for user %d failed: %v", userID, err)
		return
	}

	n.router.DispatchAll(ctx, ev, routes)
}

// healthConfig reads checker tuning from env (all optional).
func healthConfig() health.Config {
	cfg := health.Config{}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("E2M_LEGACY_HEALTH_AUTOSWITCH")), "true") {
		cfg.AllowLegacyAutoSwitch = true
		log.Printf("WARNING: E2M legacy health auto-switch writer is enabled; disable E2M_LEGACY_HEALTH_AUTOSWITCH after migration")
	}
	// Balance alerting (0 disables) and upstream config-drift detection.
	if s := os.Getenv("E2M_BALANCE_THRESHOLD"); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			cfg.BalanceThreshold = f
		}
	}
	if s := os.Getenv("E2M_BALANCE_ALERT_COOLDOWN"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			cfg.BalanceAlertCooldown = d
		}
	}
	// Backup selection strategy for auto-switch: stability | cost | performance.
	cfg.Strategy = getenv("E2M_HEALTH_STRATEGY", "stability")
	return cfg
}

// buildVault selects the credential backend: "file" (AES-256-GCM encrypted
// file, production default posture) or "memory" (dev).
func buildVault() (vault.Vault, error) {
	switch getenv("E2M_CORE_VAULT", "memory") {
	case "file":
		return vault.NewFileVault(
			getenv("E2M_VAULT_PATH", "data/vault.enc"),
			os.Getenv("E2M_VAULT_KEY"),
		)
	default:
		return vault.NewMemoryVault(), nil
	}
}

func buildStore(ctx context.Context, backend string) (store.Store, error) {
	switch backend {
	case "postgres":
		dsn := os.Getenv("E2M_CORE_DATABASE_URL")
		return store.NewPostgresStore(ctx, dsn)
	default:
		return store.NewMemoryStore(time.Now()), nil
	}
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
