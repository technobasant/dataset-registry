package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/technobasant/dataset-registry/api/internal/auth"
	"github.com/technobasant/dataset-registry/api/internal/db"
	"github.com/technobasant/dataset-registry/api/internal/httpapi"
	"github.com/technobasant/dataset-registry/api/internal/lineage"
	"github.com/technobasant/dataset-registry/api/internal/registry"
	"github.com/technobasant/dataset-registry/api/internal/telemetry"
)

// client talks to a registry over HTTP, minting tokens with the shared key.
type client struct {
	base   string
	http   *http.Client
	issuer *auth.Issuer
	tokens map[string]string
}

func newClient(base string) (*client, error) {
	key := os.Getenv("DR_SIGNING_KEY")
	if key == "" {
		return nil, fmt.Errorf("DR_SIGNING_KEY is required to mint tokens")
	}
	iss, err := auth.NewIssuer([]byte(key), 12*time.Hour)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 256, IdleConnTimeout: 90 * time.Second}
	return &client{base: strings.TrimRight(base, "/"), http: &http.Client{Transport: tr, Timeout: 60 * time.Second},
		issuer: iss, tokens: map[string]string{}}, nil
}

// as returns a token for subject/role/team, cached.
func (c *client) as(subject string, role auth.Role, team string) string {
	k := subject + "|" + string(role) + "|" + team
	if t, ok := c.tokens[k]; ok {
		return t
	}
	t, err := c.issuer.Issue(auth.Principal{Subject: subject, Role: role, Team: team})
	if err != nil {
		panic(err)
	}
	c.tokens[k] = t
	return t
}

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

func (c *client) do(token, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return &apiError{Status: res.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// ---- OpenLineage events ----

type dsRef struct{ Namespace, Name string }

const (
	producerAirflow = "https://github.com/apache/airflow/tree/providers-openlineage/2.3.0"
	producerDbt     = "https://github.com/OpenLineage/OpenLineage/tree/1.30.0/integration/dbt"
	producerSpark   = "https://github.com/OpenLineage/OpenLineage/tree/1.30.0/integration/spark"
)

func runEvent(eventType string, at time.Time, runID uuid.UUID, producer, jobNS, jobName, integration string, inputs, outputs []dsRef) map[string]any {
	refs := func(rs []dsRef) []map[string]any {
		out := make([]map[string]any, 0, len(rs))
		for _, r := range rs {
			out = append(out, map[string]any{"namespace": r.Namespace, "name": r.Name})
		}
		return out
	}
	return map[string]any{
		"eventType": eventType,
		"eventTime": at.UTC().Format(time.RFC3339Nano),
		"producer":  producer,
		"schemaURL": "https://openlineage.io/spec/2-0-2/OpenLineage.json#/$defs/RunEvent",
		"run":       map[string]any{"runId": runID.String()},
		"job": map[string]any{
			"namespace": jobNS, "name": jobName,
			"facets": map[string]any{"jobType": map[string]any{
				"_producer":      producer,
				"_schemaURL":     "https://openlineage.io/spec/facets/2-0-3/JobTypeJobFacet.json#/$defs/JobTypeJobFacet",
				"processingType": "BATCH", "integration": integration, "jobType": "JOB",
			}},
		},
		"inputs":  refs(inputs),
		"outputs": refs(outputs),
	}
}

// ---- in-process registry for benchmarks ----

// benchStack is the real HTTP handler on a loopback listener, backed by a
// fresh database, so each measurement starts from an empty registry.
type benchStack struct {
	pool   *pgxpool.Pool
	server *httptest.Server
	client *client
	reg    *registry.Service
	lin    *lineage.Service
	log    *slog.Logger
	m      *telemetry.Metrics
}

func newBenchStack(ctx context.Context, adminURL, dbName string, maxConns int32) (*benchStack, error) {
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		return nil, err
	}
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		admin.Close()
		return nil, err
	}
	admin.Close()
	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.Database = dbName
	cfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, pool); err != nil {
		return nil, err
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := telemetry.NewMetrics()
	key := os.Getenv("DR_SIGNING_KEY")
	if key == "" {
		key = strings.Repeat("bench-only-signing-key-", 2)
		_ = os.Setenv("DR_SIGNING_KEY", key)
	}
	iss, err := auth.NewIssuer([]byte(key), 12*time.Hour)
	if err != nil {
		return nil, err
	}
	reg := registry.New(pool, log, m)
	lin, err := lineage.New(pool, log, m)
	if err != nil {
		return nil, err
	}
	srv := httptest.NewServer(httpapi.New(httpapi.Config{Registry: reg, Lineage: lin, Issuer: iss, Metrics: m, Log: log}))
	cl, err := newClient(srv.URL)
	if err != nil {
		return nil, err
	}
	return &benchStack{pool: pool, server: srv, client: cl, reg: reg, lin: lin, log: log, m: m}, nil
}

func (b *benchStack) Close() {
	b.server.Close()
	b.pool.Close()
}

// ---- statistics ----

func percentile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	pos := q / 100 * float64(len(s)-1)
	lo := int(pos)
	if lo >= len(s)-1 {
		return s[len(s)-1]
	}
	frac := pos - float64(lo)
	return s[lo]*(1-frac) + s[lo+1]*frac
}

func median(xs []float64) float64 { return percentile(xs, 50) }

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func writeJSON(path string, v any) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
