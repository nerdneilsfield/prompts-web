// Command server runs the prompt library: API, SQLite and the embedded frontend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"promptsweb/internal/app"
	"promptsweb/web"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func run() error {
	origin := env("APP_ORIGIN", "http://localhost:8080")
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("APP_ORIGIN must be an http(s) origin such as https://prompts.example.com")
	}
	dataDir := env("DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}

	cfg := app.DefaultConfig()
	cfg.Password = os.Getenv("APP_PASSWORD")
	cfg.DBPath = filepath.Join(dataDir, "prompts.db")
	cfg.AllowedOrigins = []string{u.Scheme + "://" + u.Host}
	cfg.CookieSecure = u.Scheme == "https"
	cfg.BaseURL = os.Getenv("LLM_BASE_URL")
	cfg.APIKey = os.Getenv("LLM_API_KEY")
	cfg.Model = os.Getenv("LLM_MODEL")
	// Optional: without it the MCP endpoint stays off and answers 404.
	cfg.MCPToken = os.Getenv("MCP_TOKEN")

	trustProxy, err := parseBoolEnv("TRUST_PROXY", false)
	if err != nil {
		return err
	}
	cfg.TrustProxy = trustProxy

	store, err := app.OpenStore(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	assets, err := web.Assets()
	if err != nil {
		return err
	}
	handler, err := app.NewServer(store, cfg, assets)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              env("LISTEN_ADDR", "127.0.0.1:8080"),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Must outlast the 90s upstream model call.
		WriteTimeout: cfg.RequestTimeout + 30*time.Second,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("listening on %s, origin %s, model configured: %t, mcp enabled: %t, trust proxy: %t", srv.Addr, origin, cfg.BaseURL != "" && cfg.APIKey != "" && cfg.Model != "", cfg.MCPToken != "", cfg.TrustProxy)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), cfg.RequestTimeout+10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// parseBoolEnv reads a boolean switch. Only the forms Go itself accepts are
// allowed; anything else is an error rather than a silent false, because a typo
// here would silently disable the trusted-proxy path.
func parseBoolEnv(key string, def bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean (true/false/1/0): %q", key, raw)
	}
	return v, nil
}
