package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/k4ran909/webssh/internal/api"
	"github.com/k4ran909/webssh/internal/config"
	"github.com/k4ran909/webssh/internal/gateway"
	"github.com/k4ran909/webssh/internal/store"
	"github.com/k4ran909/webssh/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "webssh.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	secret, err := api.LoadSecret(cfg.DataDir)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := api.New(db, cfg, log, secret)
	a.StartJanitor(ctx)
	gw := gateway.New(cfg, log, gateway.Hooks{User: api.UserID, ClientIP: a.ClientIP, Audit: a.Record})

	mux := http.NewServeMux()
	a.Routes(mux)
	mux.Handle("GET /ws/terminal", a.RequireUser(gw))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("GET /", web.Handler())

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           web.SecurityHeaders(a.SameOrigin(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	if !config.IsLoopback(cfg.Addr) && !cfg.TrustProxy {
		log.Warn("listening on a public address without a TLS proxy; put Caddy (deploy/) in front and set WEBSSH_TRUST_PROXY=1")
	}
	log.Info("webssh listening", "addr", "http://"+cfg.Addr, "data", cfg.DataDir,
		"allowPrivateTargets", cfg.AllowPrivate, "openSignup", cfg.OpenSignup)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
