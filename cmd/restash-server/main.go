// restash-server is the server side of restash. For now it serves the restic
// repos (through the delete-to-trash proxy in front of rest-server) and a
// health check.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/mcbulazs/restash/internal/trash"
)

type config struct {
	listen     string
	restServer *url.URL
	storage    string
	trashDays  int
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	var c config
	c.listen = env("RESTASH_LISTEN", ":8080")
	u, err := url.Parse(env("RESTASH_REST_SERVER_URL", "http://rest-server:8000"))
	if err != nil || u.Host == "" {
		return c, fmt.Errorf("RESTASH_REST_SERVER_URL is not a URL")
	}
	c.restServer = u
	c.storage = env("RESTASH_STORAGE", "/storage")
	c.trashDays, err = strconv.Atoi(env("RESTASH_TRASH_DAYS", "30"))
	if err != nil || c.trashDays < 1 {
		return c, fmt.Errorf("RESTASH_TRASH_DAYS must be a number of days")
	}
	return c, nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	repos := filepath.Join(cfg.storage, "repos")
	trashDir := filepath.Join(cfg.storage, "trash")
	for _, d := range []string{repos, trashDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go trash.RunJanitor(ctx, trashDir, time.Duration(cfg.trashDays)*24*time.Hour)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.Handle("/repo/", http.StripPrefix("/repo", trash.NewProxy(cfg.restServer, repos, trashDir)))

	srv := &http.Server{
		Addr:              cfg.listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("listening", "addr", cfg.listen, "rest_server", cfg.restServer.String(), "storage", cfg.storage)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// healthcheck is run by Docker inside the container (distroless has no curl).
func healthcheck() int {
	addr := env("RESTASH_LISTEN", ":8080")
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://localhost" + addr + "/api/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
