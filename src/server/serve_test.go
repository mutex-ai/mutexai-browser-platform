package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/mutex-ai/mutexai-browser-platform/src/internal/config"
)

func TestStopClosesRequestsAfterDeadline(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	srv, err := New(Options{
		Cfg: config.Server{ShutdownTimeout: 20 * time.Millisecond},
		Log: zap.NewNop(),
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
			close(canceled)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.srv.Close() })
	served := make(chan error, 1)
	go func() { served <- srv.srv.Serve(listener) }()
	requested := make(chan error, 1)
	go func() {
		client := http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://" + listener.Addr().String())
		if resp != nil {
			_ = resp.Body.Close()
		}
		requested <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	if err := srv.Stop(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() = %v, want deadline exceeded", err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown left the request running")
	}
	if err := <-requested; err == nil {
		t.Fatal("aborted request unexpectedly succeeded")
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve() = %v, want server closed", err)
	}
}
