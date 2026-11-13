// Package server owns the HTTP server lifecycle: one plain-HTTP listener.
// It is used through loopback or a restricted private network.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"go.uber.org/zap"

	"github.com/mutex-ai/mutexai-browser-platform/src/internal/config"
)

// Server wraps the single http.Server behind one Start/Stop lifecycle.
type Server struct {
	srv   *http.Server
	cfg   config.Server
	log   *zap.Logger
	errCh chan error
}

// Options parameterises construction. Handler is required.
type Options struct {
	Cfg     config.Server
	Handler http.Handler
	Log     *zap.Logger
}

// New configures the HTTP server. Handler is required.
func New(opts Options) (*Server, error) {
	if opts.Handler == nil {
		return nil, errors.New("handler is required")
	}
	srv := &http.Server{
		Addr:              opts.Cfg.ListenAddr,
		Handler:           opts.Handler,
		ReadTimeout:       opts.Cfg.ReadTimeout,
		ReadHeaderTimeout: opts.Cfg.ReadHeaderTimeout,
		WriteTimeout:      opts.Cfg.WriteTimeout,
		IdleTimeout:       opts.Cfg.IdleTimeout,
	}
	return &Server{
		srv:   srv,
		cfg:   opts.Cfg,
		log:   opts.Log,
		errCh: make(chan error, 1),
	}, nil
}

// Start serves the listener in a background goroutine. Non-blocking.
// A fatal failure is sent on Err(); the caller tears down via Stop().
func (s *Server) Start() {
	go func() {
		s.log.Info("listener starting", zap.String("addr", s.srv.Addr))
		err := s.srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.errCh <- fmt.Errorf("listener: %w", err)
		}
	}()
}

// Err returns a channel that signals the listener's fatal failure.
func (s *Server) Err() <-chan error { return s.errCh }

// Stop drains requests, closing remaining connections if the deadline expires.
func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown HTTP server: %w", errors.Join(err, s.srv.Close()))
	}
	s.log.Info("server stopped")
	return nil
}
