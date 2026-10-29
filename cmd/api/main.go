// Command api runs the Meowshot backend.
//
//	api            run migrations and serve HTTP
//	api migrate    run migrations and exit
//	api promote <email>   grant the admin role (moderation panel, MOD-4)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"meowshot/internal/api"
	"meowshot/internal/auth"
	"meowshot/internal/config"
	"meowshot/internal/db"
	"meowshot/internal/mail"
	"meowshot/internal/storage"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	switch cmd := arg(1); cmd {
	case "migrate":
		log.Info("migrations applied")
		return nil
	case "promote":
		email := arg(2)
		tag, err := pool.Exec(ctx, `UPDATE users SET role = 'admin' WHERE email = lower($1)`, email)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("no user with email %q", email)
		}
		log.Info("promoted to admin", "email", email)
		return nil
	case "", "serve":
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}

	srv := api.New(api.Deps{
		Config:  cfg,
		DB:      pool,
		Storage: storage.NewB2(cfg.Storage, cfg.SignedURL),
		Google:  auth.NewGoogleVerifier(context.Background(), cfg.GoogleClientIDs),
		Mail:    mail.New(cfg.SMTP, log),
		Log:     log,
	})
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute, // slow mobile uploads
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "env", cfg.Env)
		errc <- httpSrv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
	return nil
}

func arg(i int) string {
	if len(os.Args) > i {
		return os.Args[i]
	}
	return ""
}
