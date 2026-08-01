package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMountCoreRoutesKeepsSupplyAndConsoleInOneHandler(t *testing.T) {
	console := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Handler", "console")
		w.WriteHeader(http.StatusNoContent)
	})
	supply := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Handler", "supply")
		w.WriteHeader(http.StatusAccepted)
	})
	handler := mountCoreRoutes(console, supply)

	for _, test := range []struct {
		path       string
		wantStatus int
		want       string
	}{
		{path: "/v1/chat/completions", wantStatus: http.StatusAccepted, want: "supply"},
		{path: "/api/v1/auth/me", wantStatus: http.StatusNoContent, want: "console"},
		{path: "/", wantStatus: http.StatusNoContent, want: "console"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != test.wantStatus || recorder.Header().Get("X-Handler") != test.want {
			t.Errorf("path %s: status=%d handler=%q", test.path, recorder.Code, recorder.Header().Get("X-Handler"))
		}
	}
}

func TestServeCoreCancelsRequestsAndWaitsForWorkersBeforeClosingStore(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
		close(requestCanceled)
	})}
	workerStarted := make(chan struct{})
	workerCanceled := make(chan struct{})
	releaseWorker := make(chan struct{})
	var storeClosed atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveCore(ctx, server, listener, []coreWorker{func(ctx context.Context) {
			close(workerStarted)
			<-ctx.Done()
			close(workerCanceled)
			<-releaseWorker
		}}, func() { storeClosed.Store(true) }, time.Second)
	}()

	select {
	case <-workerStarted:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- requestErr
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("request did not reach server")
	}

	cancel()
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("HTTP request context was not canceled")
	}
	select {
	case <-workerCanceled:
	case <-time.After(time.Second):
		t.Fatal("worker context was not canceled")
	}
	if storeClosed.Load() {
		t.Fatal("store closed while a worker was still live")
	}
	select {
	case err := <-done:
		t.Fatalf("serveCore returned before worker stopped: %v", err)
	default:
	}

	close(releaseWorker)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveCore: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveCore did not finish")
	}
	if !storeClosed.Load() {
		t.Fatal("store was not closed after worker and HTTP shutdown")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("HTTP client did not finish")
	}
}

func TestServeCoreServeFailureCancelsWorkersBeforeClosingStore(t *testing.T) {
	wantErr := errors.New("accept failed")
	listener := &failingListener{err: wantErr}
	workerCanceled := make(chan struct{})
	var workerStopped atomic.Bool
	var storeClosedTooEarly atomic.Bool

	err := serveCore(context.Background(), &http.Server{Handler: http.NotFoundHandler()}, listener, []coreWorker{
		func(ctx context.Context) {
			<-ctx.Done()
			close(workerCanceled)
			workerStopped.Store(true)
		},
	}, func() {
		if !workerStopped.Load() {
			storeClosedTooEarly.Store(true)
		}
	}, time.Second)

	if !errors.Is(err, wantErr) {
		t.Fatalf("serveCore error = %v, want wrapped %v", err, wantErr)
	}
	if storeClosedTooEarly.Load() {
		t.Fatal("store closed before worker stopped")
	}
	select {
	case <-workerCanceled:
	default:
		t.Fatal("serve failure did not cancel worker")
	}
}

func TestServeCoreTimeoutDoesNotCloseStoreWhileWorkerLives(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	releaseWorker := make(chan struct{})
	workerStarted := make(chan struct{})
	var storeClosed atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- serveCore(ctx, &http.Server{Handler: http.NotFoundHandler()}, listener, []coreWorker{
			func(context.Context) {
				close(workerStarted)
				<-releaseWorker
			},
		}, func() { storeClosed.Store(true) }, 25*time.Millisecond)
	}()
	<-workerStarted
	cancel()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "shutdown timed out") {
			t.Fatalf("serveCore error = %v, want shutdown timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveCore did not honor shutdown timeout")
	}
	if storeClosed.Load() {
		t.Fatal("store closed while timed-out worker was still live")
	}
	close(releaseWorker)
}

func TestServeCoreShutdownTimeoutDoesNotCloseStoreWhileHandlerLives(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	var storeClosed atomic.Bool
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(handlerStarted)
		<-releaseHandler
	})}
	done := make(chan error, 1)
	go func() {
		done <- serveCore(ctx, server, listener, nil, func() { storeClosed.Store(true) }, 25*time.Millisecond)
	}()
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- requestErr
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	cancel()

	select {
	case err := <-done:
		if err == nil || (!strings.Contains(err.Error(), "shutdown timed out") && !strings.Contains(err.Error(), "HTTP shutdown failed")) {
			t.Fatalf("serveCore error = %v, want HTTP shutdown error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveCore did not honor HTTP shutdown timeout")
	}
	if storeClosed.Load() {
		t.Fatal("store closed while timed-out HTTP handler was still live")
	}
	close(releaseHandler)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("HTTP client did not finish")
	}
}

type failingListener struct {
	err error
}

func (l *failingListener) Accept() (net.Conn, error) { return nil, l.err }
func (l *failingListener) Close() error              { return nil }
func (l *failingListener) Addr() net.Addr            { return testAddr("failing-listener") }

type testAddr string

func (a testAddr) Network() string { return string(a) }
func (a testAddr) String() string  { return string(a) }

func TestHealthConfigLegacyWriterDefaultsDisabled(t *testing.T) {
	t.Setenv("E2M_LEGACY_HEALTH_AUTOSWITCH", "")
	if cfg := healthConfig(); cfg.AllowLegacyAutoSwitch {
		t.Fatal("legacy health writer must default to disabled")
	}
}

func TestHealthConfigLegacyWriterRequiresExplicitTrue(t *testing.T) {
	for _, value := range []string{"false", "1", "yes"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("E2M_LEGACY_HEALTH_AUTOSWITCH", value)
			if cfg := healthConfig(); cfg.AllowLegacyAutoSwitch {
				t.Fatalf("value %q must not enable the legacy health writer", value)
			}
		})
	}

	t.Run("true", func(t *testing.T) {
		t.Setenv("E2M_LEGACY_HEALTH_AUTOSWITCH", "true")
		if cfg := healthConfig(); !cfg.AllowLegacyAutoSwitch {
			t.Fatal("explicit true should enable the legacy health writer")
		}
	})
}

func TestServeCoreWithMetricsStopsBothListenersBeforeClosingStore(t *testing.T) {
	coreListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	metricsListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = coreListener.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var storeClosed atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- serveCoreWithMetrics(
			ctx,
			&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })},
			coreListener,
			&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("metric 1\n")) })},
			metricsListener,
			nil,
			func() { storeClosed.Store(true) },
			time.Second,
		)
	}()

	for _, rawURL := range []string{"http://" + coreListener.Addr().String(), "http://" + metricsListener.Addr().String()} {
		response, requestErr := http.Get(rawURL)
		if requestErr != nil {
			t.Fatalf("request %s: %v", rawURL, requestErr)
		}
		_ = response.Body.Close()
	}
	cancel()
	select {
	case serveErr := <-done:
		if serveErr != nil {
			t.Fatalf("serveCoreWithMetrics: %v", serveErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dual-listener shutdown timed out")
	}
	if !storeClosed.Load() {
		t.Fatal("store was not closed after both listeners stopped")
	}
}
