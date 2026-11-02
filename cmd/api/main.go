package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/rishi/creatorhq/internal/auth"
	"github.com/rishi/creatorhq/internal/brands"
	"github.com/rishi/creatorhq/internal/config"
	"github.com/rishi/creatorhq/internal/db"
	"github.com/rishi/creatorhq/internal/deals"
	"github.com/rishi/creatorhq/internal/httpx"
	"github.com/rishi/creatorhq/migrations"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if cfg.AutoMigrate {
		if err := db.Migrate(ctx, pool, migrations.FS); err != nil {
			return err
		}
	}

	authSvc := &auth.Service{DB: pool, TTL: cfg.SessionTTL, CookieSecure: cfg.CookieSecure}
	brandH := &brands.Handler{DB: pool}
	dealH := &deals.Handler{DB: pool}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, httpx.Logger, middleware.Recoverer)
	r.Use(httpx.CORS(cfg.AllowedOrigin), httpx.RequireJSON)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db_down"})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/api/v1", func(r chi.Router) {
		authSvc.Routes(r, authSvc.Middleware)
		r.Group(func(r chi.Router) {
			r.Use(authSvc.Middleware, auth.RequireWrite)
			brandH.Routes(r)
			dealH.Routes(r)
		})
	})

	// Background: purge expired sessions hourly. This moves to the worker
	// process once Asynq is added in week 3.
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := authSvc.CleanupExpired(ctx); err != nil {
					slog.Warn("session cleanup failed", "err", err)
				} else if n > 0 {
					slog.Info("expired sessions removed", "count", n)
				}
			}
		}
	}()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		slog.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
