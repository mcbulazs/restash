// restash-server is the server side of restash: the web UI and API, the
// agents' link, and the restic repos (restic's rest-server, built in, with
// deletes going to a trash folder).
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mcbulazs/restash/internal/mail"
	"github.com/mcbulazs/restash/internal/server"
	"github.com/mcbulazs/restash/internal/store"
	"github.com/mcbulazs/restash/internal/trash"
	"github.com/mcbulazs/restash/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	listen    string
	publicURL *url.URL
	storage   string
	db        string
	webDir    string
	trashDays int
	ntfy      string
	metrics   string
	mail      *mail.Sender // nil: email off
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
	pub, err := url.Parse(strings.TrimSuffix(env("RESTASH_PUBLIC_URL", "http://localhost:8080"), "/"))
	if err != nil || pub.Host == "" || (pub.Scheme != "http" && pub.Scheme != "https") {
		return c, fmt.Errorf("RESTASH_PUBLIC_URL must be the site's URL, e.g. https://restash.example.com")
	}
	c.publicURL = pub
	c.storage = env("RESTASH_STORAGE", "/storage")
	c.db = env("RESTASH_DB", filepath.Join(c.storage, "restash.db"))
	c.webDir = os.Getenv("RESTASH_WEB_DIR")
	// Alerts are opt-in: empty or "off" sends nothing.
	c.ntfy = os.Getenv("RESTASH_NTFY_SERVER")
	if c.ntfy == "off" {
		c.ntfy = ""
	}
	c.metrics = os.Getenv("RESTASH_METRICS_LISTEN")
	c.trashDays, err = strconv.Atoi(env("RESTASH_TRASH_DAYS", "30"))
	if err != nil || c.trashDays < 1 {
		return c, fmt.Errorf("RESTASH_TRASH_DAYS must be a number of days")
	}
	// Invite emails are opt-in: without an SMTP server the admin sends
	// invite links themselves.
	if host := os.Getenv("RESTASH_SMTP_HOST"); host != "" {
		c.mail, err = mail.New(mail.Config{
			Addr:     host,
			From:     os.Getenv("RESTASH_SMTP_FROM"),
			Username: os.Getenv("RESTASH_SMTP_USERNAME"),
			Password: os.Getenv("RESTASH_SMTP_PASSWORD"),
			TLS:      os.Getenv("RESTASH_SMTP_TLS"),
		})
		if err != nil {
			return c, fmt.Errorf("RESTASH_SMTP_*: %w", err)
		}
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
	st, err := store.Open(cfg.db)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var webFS fs.FS = web.Dist()
	if cfg.webDir != "" {
		webFS = os.DirFS(cfg.webDir)
	}
	repoProxy, err := trash.NewProxy(repos, trashDir)
	if err != nil {
		return fmt.Errorf("serving repos: %w", err)
	}
	srv, err := server.New(ctx, server.Config{
		PublicURL:   cfg.publicURL,
		Store:       st,
		Repos:       repoProxy,
		ReposDir:    repos,
		Storage:     cfg.storage,
		Web:         webFS,
		Version:     version,
		NtfyServer:  cfg.ntfy,
		AgentSource: env("RESTASH_AGENT_SOURCE", server.DefaultAgentSource(version)),
		Mail:        cfg.mail,
	})
	if err != nil {
		return err
	}
	if cfg.mail != nil {
		slog.Info("invite emails on", "smtp", cfg.mail.Addr(), "from", cfg.mail.From())
	}

	if inv, err := st.SetupInvite(ctx); err == nil {
		slog.Info("no accounts yet: open this link to make the admin account",
			"link", cfg.publicURL.String()+"/signup?invite="+inv.Token, "expires", inv.ExpiresAt.Format(time.DateTime))
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	go trash.RunJanitor(ctx, trashDir, time.Duration(cfg.trashDays)*24*time.Hour)
	go srv.Run(ctx)

	// Metrics go on their own address, which isn't published through the
	// reverse proxy.
	if cfg.metrics != "" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", srv.MetricsHandler())
		ms := &http.Server{Addr: cfg.metrics, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			slog.Info("metrics listening", "addr", cfg.metrics)
			if err := ms.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				slog.Error("metrics server", "err", err)
			}
		}()
		go func() {
			<-ctx.Done()
			ms.Close()
		}()
	}

	httpSrv := &http.Server{
		Addr:              cfg.listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdown)
	}()
	slog.Info("listening", "addr", cfg.listen, "url", cfg.publicURL.String(), "version", version)
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// healthcheck is run by Docker inside the container (distroless has no curl).
func healthcheck() int {
	addr := env("RESTASH_LISTEN", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/health")
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
