// Command registry runs the dataset registry: the HTTP API, the freshness
// evaluator, migrations, and a development token issuer.
//
//	registry serve      run migrations, the API and the evaluator (default)
//	registry migrate    apply migrations and exit
//	registry token -role owner -team checkout -subject amara
//	registry healthcheck  exit 0 if the local API is ready (for container health checks)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/technobasant/dataset-registry/api/internal/auth"
	"github.com/technobasant/dataset-registry/api/internal/db"
	"github.com/technobasant/dataset-registry/api/internal/freshness"
	"github.com/technobasant/dataset-registry/api/internal/httpapi"
	"github.com/technobasant/dataset-registry/api/internal/lineage"
	"github.com/technobasant/dataset-registry/api/internal/registry"
	"github.com/technobasant/dataset-registry/api/internal/telemetry"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "migrate":
		err = migrate()
	case "token":
		err = token(os.Args[2:])
	case "healthcheck":
		err = healthcheck()
	default:
		err = fmt.Errorf("unknown command %q (serve, migrate, token)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "registry:", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func issuer() (*auth.Issuer, error) {
	key := os.Getenv("DR_SIGNING_KEY")
	if key == "" {
		return nil, errors.New("DR_SIGNING_KEY is required (32+ bytes)")
	}
	return auth.NewIssuer([]byte(key), 12*time.Hour)
}

func connect(ctx context.Context) (*pgxpool.Pool, error) {
	url := env("DATABASE_URL", "postgres://registry:registry@127.0.0.1:55433/registry?sslmode=disable")
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if n, err := strconv.Atoi(os.Getenv("DR_DB_MAX_CONNS")); err == nil && n > 0 {
		cfg.MaxConns = int32(n)
	}
	var pool *pgxpool.Pool
	for attempt := 0; attempt < 30; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				return pool, nil
			}
			pool.Close()
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("database not reachable: %w", err)
}

func healthcheck() error {
	addr := env("DR_ADDR", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := &http.Client{Timeout: 2 * time.Second}
	res, err := c.Get("http://" + addr + "/readyz")
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz: %s", res.Status)
	}
	return nil
}

func migrate() error {
	ctx := context.Background()
	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	return db.Migrate(ctx, pool)
}

func token(args []string) error {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	role := fs.String("role", "owner", "admin, owner or consumer")
	team := fs.String("team", "", "team the token acts for")
	subject := fs.String("subject", "", "person or pipeline")
	if err := fs.Parse(args); err != nil {
		return err
	}
	iss, err := issuer()
	if err != nil {
		return err
	}
	r, err := auth.ParseRole(*role)
	if err != nil {
		return err
	}
	tok, err := iss.Issue(auth.Principal{Subject: *subject, Role: r, Team: *team})
	if err != nil {
		return err
	}
	fmt.Println(tok)
	return nil
}

func serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := telemetry.Logger()
	slog.SetDefault(log)

	shutdownTracing, err := telemetry.Tracing(ctx, "dataset-registry")
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

	iss, err := issuer()
	if err != nil {
		return err
	}
	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	metrics := telemetry.NewMetrics()
	reg := registry.New(pool, log, metrics)
	lin, err := lineage.New(pool, log, metrics)
	if err != nil {
		return err
	}

	interval, err := time.ParseDuration(env("DR_FRESHNESS_INTERVAL", "15s"))
	if err != nil {
		return fmt.Errorf("DR_FRESHNESS_INTERVAL: %w", err)
	}
	if env("DR_FRESHNESS_EVALUATOR", "on") != "off" {
		ev := freshness.New(pool, log, metrics, interval)
		go ev.Run(ctx)
	}

	devTokens := env("DR_DEV_TOKENS", "false") == "true"
	if devTokens {
		log.Warn("development token issuer is enabled: anyone who can reach the API can mint any role")
	}
	srv := &http.Server{
		Addr:              env("DR_ADDR", ":8080"),
		Handler:           httpapi.New(httpapi.Config{Registry: reg, Lineage: lin, Issuer: iss, Metrics: metrics, Log: log, DevTokens: devTokens}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", srv.Addr, "freshness_interval", interval.String())

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}
