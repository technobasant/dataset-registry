package httpapi_test

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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/technobasant/dataset-registry/api/internal/auth"
	"github.com/technobasant/dataset-registry/api/internal/db"
	"github.com/technobasant/dataset-registry/api/internal/freshness"
	"github.com/technobasant/dataset-registry/api/internal/httpapi"
	"github.com/technobasant/dataset-registry/api/internal/lineage"
	"github.com/technobasant/dataset-registry/api/internal/registry"
	"github.com/technobasant/dataset-registry/api/internal/telemetry"
)

// adminURL points at a PostgreSQL server where each test creates its own
// database. TEST_DATABASE_URL reuses a running server (CI's service
// container); otherwise testcontainers starts one.
var adminURL string

func TestMain(m *testing.M) {
	adminURL = os.Getenv("TEST_DATABASE_URL")
	var stop func()
	if adminURL == "" {
		ctx := context.Background()
		c, err := tcpostgres.Run(ctx, "postgres:18.1-alpine",
			tcpostgres.WithDatabase("registry"), tcpostgres.WithUsername("registry"), tcpostgres.WithPassword("registry"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)))
		if err != nil {
			fmt.Fprintln(os.Stderr, "start postgres:", err)
			os.Exit(1)
		}
		stop = func() { _ = c.Terminate(ctx) }
		adminURL, err = c.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	if stop != nil {
		stop()
	}
	os.Exit(code)
}

var dbSeq atomic.Int64

type env struct {
	t      *testing.T
	srv    *httptest.Server
	pool   *pgxpool.Pool
	issuer *auth.Issuer
	eval   *freshness.Evaluator
	tokens map[string]string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("t_%d_%d", os.Getpid(), dbSeq.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	admin.Close()

	cfg, _ := pgxpool.ParseConfig(adminURL)
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := telemetry.NewMetrics()
	iss, _ := auth.NewIssuer([]byte(strings.Repeat("s", 32)), time.Hour)
	lin, err := lineage.New(pool, log, m)
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(httpapi.Config{Registry: registry.New(pool, log, m), Lineage: lin, Issuer: iss, Metrics: m, Log: log, DevTokens: true})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	e := &env{t: t, srv: srv, pool: pool, issuer: iss, eval: freshness.New(pool, log, m, time.Second), tokens: map[string]string{}}
	for key, p := range map[string]auth.Principal{
		"admin":    {Subject: "ines", Role: auth.Admin, Team: "data-platform"},
		"checkout": {Subject: "amara", Role: auth.Owner, Team: "checkout"},
		"payments": {Subject: "theo", Role: auth.Owner, Team: "payments"},
		"finance":  {Subject: "jun", Role: auth.Consumer, Team: "finance-analytics"},
		"ml":       {Subject: "sofia", Role: auth.Consumer, Team: "ml-platform"},
	} {
		tok, err := iss.Issue(p)
		if err != nil {
			t.Fatal(err)
		}
		e.tokens[key] = tok
	}
	return e
}

type resp struct {
	status int
	body   map[string]any
	raw    string
}

func (e *env) do(who, method, path string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if who != "" {
		tok, ok := e.tokens[who]
		if !ok {
			tok = who
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (e *env) must(r resp, status int) resp {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("want %d, got %d: %s", status, r.status, r.raw)
	}
	return r
}

func num(v any) int64 { f, _ := v.(float64); return int64(f) }

const ordersV1 = `{"type":"object","properties":{"order_id":{"type":"string"},"amount_cents":{"type":"integer"},"status":{"type":"string","enum":["placed","shipped"]}},"required":["order_id","amount_cents"]}`

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func (e *env) createOrders(slo int) int64 {
	e.t.Helper()
	r := e.must(e.do("checkout", "POST", "/api/v1/datasets", map[string]any{
		"namespace": "iceberg://lake", "name": "sales.orders", "description": "One row per order",
		"owner": map[string]string{"team": "checkout", "person": "amara"}, "tier": "gold",
		"freshness_slo_seconds": slo, "schema": raw(ordersV1),
	}), http.StatusCreated)
	return num(r.body["id"])
}

func runEvent(eventType string, at time.Time, job string, inputs, outputs []string) map[string]any {
	refs := func(names []string) []map[string]any {
		out := []map[string]any{}
		for _, n := range names {
			out = append(out, map[string]any{"namespace": "iceberg://lake", "name": n})
		}
		return out
	}
	return map[string]any{
		"eventType": eventType, "eventTime": at.UTC().Format(time.RFC3339Nano),
		"producer": "https://example.com/airflow", "schemaURL": "https://openlineage.io/spec/2-0-2/OpenLineage.json#/$defs/RunEvent",
		"run":    map[string]any{"runId": uuid.NewString()},
		"job":    map[string]any{"namespace": "airflow", "name": job},
		"inputs": refs(inputs), "outputs": refs(outputs),
	}
}

func TestOwnershipBeforeConsumption(t *testing.T) {
	e := newEnv(t)
	e.must(e.do("checkout", "POST", "/api/v1/lineage", runEvent("COMPLETE", time.Now(), "load_orders", []string{"raw.orders"}, []string{"sales.orders"})), http.StatusCreated)

	r := e.must(e.do("", "GET", "/api/v1/datasets/by-name?namespace=iceberg://lake&name=sales.orders", nil), http.StatusOK)
	id := num(r.body["id"])
	if r.body["registered"] != false || r.body["owner"] != nil {
		t.Fatalf("a dataset seen only in lineage is unowned: %s", r.raw)
	}
	e.must(e.do("finance", "POST", fmt.Sprintf("/api/v1/datasets/%d/consumers", id),
		map[string]string{"contact": "#finance-data"}), http.StatusConflict)

	// The owner claims it; now it can be depended on.
	e.createOrders(3600)
	e.must(e.do("finance", "POST", fmt.Sprintf("/api/v1/datasets/%d/consumers", id),
		map[string]string{"contact": "#finance-data", "purpose": "revenue reporting"}), http.StatusCreated)
	e.must(e.do("finance", "POST", fmt.Sprintf("/api/v1/datasets/%d/consumers", id),
		map[string]string{"contact": "#finance-data"}), http.StatusConflict)

	a := e.must(e.do("", "GET", fmt.Sprintf("/api/v1/audit?entity_type=dataset&entity_id=%d", id), nil), http.StatusOK)
	actions := []string{}
	for _, x := range a.body["entries"].([]any) {
		actions = append(actions, x.(map[string]any)["action"].(string))
	}
	if strings.Join(actions, ",") != "consumer.registered,schema.published,dataset.claimed" {
		t.Errorf("audit actions: %v", actions)
	}
	// A second team cannot take it over.
	e.must(e.do("payments", "POST", "/api/v1/datasets", map[string]any{
		"namespace": "iceberg://lake", "name": "sales.orders", "owner": map[string]string{"team": "payments", "person": "theo"},
	}), http.StatusConflict)
}

func TestBreakingChangeWaitsForEveryConsumer(t *testing.T) {
	e := newEnv(t)
	id := e.createOrders(3600)
	path := fmt.Sprintf("/api/v1/datasets/%d", id)
	for _, who := range []string{"finance", "ml"} {
		e.must(e.do(who, "POST", path+"/consumers", map[string]string{"contact": who + "@lumenloom.example"}), http.StatusCreated)
	}

	// Compatible under FORWARD (the default): an enum value removed.
	v2 := strings.Replace(ordersV1, `["placed","shipped"]`, `["placed"]`, 1)
	r := e.must(e.do("checkout", "POST", path+"/schemas", map[string]any{"schema": raw(v2)}), http.StatusCreated)
	if r.body["published"] != true {
		t.Fatalf("compatible change should publish: %s", r.raw)
	}

	// Breaking: amount_cents becomes a number.
	v3 := strings.Replace(v2, `"amount_cents":{"type":"integer"}`, `"amount_cents":{"type":"number"}`, 1)
	r = e.must(e.do("checkout", "POST", path+"/schemas", map[string]any{"schema": raw(v3)}), http.StatusAccepted)
	cr := r.body["change_request"].(map[string]any)
	crPath := fmt.Sprintf("/api/v1/change-requests/%d", num(cr["id"]))
	if cr["state"] != "open" || num(cr["pending_reviews"]) != 2 {
		t.Fatalf("want open with 2 pending: %s", r.raw)
	}
	report := cr["report"].(map[string]any)
	if report["compatible"] != false || report["changes"].([]any)[0].(map[string]any)["kind"] != "type_widened" {
		t.Fatalf("report: %v", report)
	}

	e.must(e.do("checkout", "POST", crPath+"/publish", nil), http.StatusConflict)
	e.must(e.do("checkout", "POST", path+"/schemas", map[string]any{"schema": raw(ordersV1)}), http.StatusConflict)
	e.must(e.do("admin", "POST", crPath+"/reviews", map[string]string{"decision": "acknowledged"}), http.StatusForbidden)
	e.must(e.do("payments", "POST", crPath+"/publish", nil), http.StatusForbidden)

	e.must(e.do("finance", "POST", crPath+"/reviews", map[string]string{"decision": "acknowledged"}), http.StatusOK)
	e.must(e.do("finance", "POST", crPath+"/reviews", map[string]string{"decision": "acknowledged"}), http.StatusConflict)
	e.must(e.do("checkout", "POST", crPath+"/publish", nil), http.StatusConflict)
	r = e.must(e.do("ml", "POST", crPath+"/reviews", map[string]string{"decision": "acknowledged", "reason": "feature job casts to float"}), http.StatusOK)
	if r.body["state"] != "approved" {
		t.Fatalf("want approved: %s", r.raw)
	}
	r = e.must(e.do("checkout", "POST", crPath+"/publish", nil), http.StatusOK)
	if r.body["state"] != "published" {
		t.Fatalf("want published: %s", r.raw)
	}
	e.must(e.do("checkout", "POST", crPath+"/withdraw", nil), http.StatusConflict)

	d := e.must(e.do("", "GET", path, nil), http.StatusOK)
	if num(d.body["current_version"]) != 3 || d.body["open_change_request"] != nil {
		t.Fatalf("v3 should be current with nothing open: %s", d.raw)
	}

	// The audit log is append-only, even for the application's own role.
	if _, err := e.pool.Exec(context.Background(), "UPDATE audit_log SET actor = 'someone else'"); err == nil {
		t.Fatal("audit_log accepted an UPDATE")
	}
	if _, err := e.pool.Exec(context.Background(), "DELETE FROM audit_log"); err == nil {
		t.Fatal("audit_log accepted a DELETE")
	}
}

func TestRejectionAndUnregistering(t *testing.T) {
	e := newEnv(t)
	id := e.createOrders(3600)
	path := fmt.Sprintf("/api/v1/datasets/%d", id)
	e.must(e.do("finance", "POST", path+"/consumers", map[string]string{"contact": "jun"}), http.StatusCreated)
	e.must(e.do("ml", "POST", path+"/consumers", map[string]string{"contact": "sofia"}), http.StatusCreated)

	removed := `{"type":"object","properties":{"order_id":{"type":"string"}},"required":["order_id"]}`
	r := e.must(e.do("checkout", "POST", path+"/schemas", map[string]any{"schema": raw(removed)}), http.StatusAccepted)
	crPath := fmt.Sprintf("/api/v1/change-requests/%d", num(r.body["change_request"].(map[string]any)["id"]))

	e.must(e.do("finance", "POST", crPath+"/reviews", map[string]string{"decision": "rejected"}), http.StatusBadRequest)
	r = e.must(e.do("finance", "POST", crPath+"/reviews", map[string]string{"decision": "rejected", "reason": "revenue reports sum amount_cents"}), http.StatusOK)
	if r.body["state"] != "rejected" {
		t.Fatalf("want rejected: %s", r.raw)
	}
	vs := e.must(e.do("", "GET", path+"/schemas", nil), http.StatusOK)
	if vs.body["versions"].([]any)[0].(map[string]any)["status"] != "rejected" {
		t.Fatalf("version should be rejected: %s", vs.raw)
	}

	// A new attempt is allowed. When the only undecided consumer leaves, the
	// request no longer waits on anyone.
	r = e.must(e.do("checkout", "POST", path+"/schemas", map[string]any{"schema": raw(removed)}), http.StatusAccepted)
	crPath = fmt.Sprintf("/api/v1/change-requests/%d", num(r.body["change_request"].(map[string]any)["id"]))
	e.must(e.do("finance", "POST", crPath+"/reviews", map[string]string{"decision": "acknowledged"}), http.StatusOK)
	e.must(e.do("ml", "DELETE", path+"/consumers/ml-platform", nil), http.StatusNoContent)
	r = e.must(e.do("", "GET", crPath, nil), http.StatusOK)
	if r.body["state"] != "approved" {
		t.Fatalf("want approved after the last pending consumer left: %s", r.raw)
	}
}

func TestNoConsumersMeansNobodyToAsk(t *testing.T) {
	e := newEnv(t)
	id := e.createOrders(3600)
	breaking := strings.Replace(ordersV1, `"type":"string"}`, `"type":"integer"}`, 1)
	r := e.must(e.do("checkout", "POST", fmt.Sprintf("/api/v1/datasets/%d/schemas", id), map[string]any{"schema": raw(breaking)}), http.StatusAccepted)
	if r.body["change_request"].(map[string]any)["state"] != "approved" {
		t.Fatalf("with no consumers a breaking change starts approved: %s", r.raw)
	}
}

func TestAuthorization(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"namespace": "kafka://bus", "name": "orders.v1", "owner": map[string]string{"team": "checkout", "person": "amara"}}
	e.must(e.do("", "POST", "/api/v1/datasets", body), http.StatusUnauthorized)
	e.must(e.do("not-a-token", "POST", "/api/v1/datasets", body), http.StatusUnauthorized)
	e.must(e.do("finance", "POST", "/api/v1/datasets", body), http.StatusForbidden)
	e.must(e.do("payments", "POST", "/api/v1/datasets", body), http.StatusForbidden)
	r := e.must(e.do("checkout", "POST", "/api/v1/datasets", body), http.StatusCreated)
	path := fmt.Sprintf("/api/v1/datasets/%d", num(r.body["id"]))
	e.must(e.do("payments", "PATCH", path, map[string]any{"tier": "gold"}), http.StatusForbidden)
	e.must(e.do("admin", "PATCH", path, map[string]any{"tier": "gold"}), http.StatusOK)
	e.must(e.do("finance", "POST", path+"/consumers", map[string]string{"team": "ml-platform", "contact": "x"}), http.StatusForbidden)
	e.must(e.do("finance", "POST", "/api/v1/lineage", runEvent("COMPLETE", time.Now(), "j", nil, []string{"x"})), http.StatusForbidden)
	e.must(e.do("", "GET", path, nil), http.StatusOK)

	// Owners cannot delete what others depend on; admins can.
	e.must(e.do("finance", "POST", path+"/consumers", map[string]string{"contact": "jun"}), http.StatusCreated)
	e.must(e.do("checkout", "DELETE", path, nil), http.StatusConflict)
	e.must(e.do("admin", "DELETE", path, nil), http.StatusNoContent)

	// Dev token issuer.
	r = e.must(e.do("", "POST", "/api/v1/dev/tokens", map[string]string{"subject": "a", "role": "owner", "team": "t"}), http.StatusCreated)
	if p, err := e.issuer.Verify(r.body["token"].(string)); err != nil || p.Team != "t" {
		t.Fatalf("dev token: %v %v", p, err)
	}
}

func TestSchemaValidation(t *testing.T) {
	e := newEnv(t)
	id := e.createOrders(3600)
	path := fmt.Sprintf("/api/v1/datasets/%d/schemas", id)
	e.must(e.do("checkout", "POST", path, map[string]any{"schema": raw(`{"type":"array"}`)}), http.StatusUnprocessableEntity)
	e.must(e.do("checkout", "POST", path, map[string]any{"schema": raw(`{"type":"object","minProperties":"x"}`)}), http.StatusUnprocessableEntity)
	e.must(e.do("checkout", "POST", path, map[string]any{"schema": raw(ordersV1)}), http.StatusUnprocessableEntity)
	r := e.must(e.do("", "POST", fmt.Sprintf("/api/v1/datasets/%d/compatibility-check", id),
		map[string]any{"schema": raw(strings.Replace(ordersV1, `"placed","shipped"`, `"placed","shipped","returned"`, 1))}), http.StatusOK)
	if r.body["compatible"] != false {
		t.Fatalf("enum value added breaks FORWARD: %s", r.raw)
	}
}

func TestLineageIngestAndQuery(t *testing.T) {
	e := newEnv(t)
	bad := runEvent("COMPLETE", time.Now(), "j", nil, nil)
	delete(bad, "run")
	r := e.must(e.do("checkout", "POST", "/api/v1/lineage", bad), http.StatusBadRequest)
	if r.body["extra"] == nil {
		t.Errorf("validation errors should be returned: %s", r.raw)
	}

	t0 := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	// raw.orders → load → staging.orders → model → marts.revenue → export → bi.revenue
	e.must(e.do("checkout", "POST", "/api/v1/lineage", runEvent("START", t0, "load", []string{"raw.orders"}, []string{"staging.orders"})), http.StatusCreated)
	e.must(e.do("checkout", "POST", "/api/v1/lineage", runEvent("COMPLETE", t0.Add(time.Minute), "load", []string{"raw.orders"}, []string{"staging.orders"})), http.StatusCreated)
	e.must(e.do("checkout", "POST", "/api/v1/lineage", runEvent("COMPLETE", t0.Add(2*time.Minute), "model", []string{"staging.orders", "raw.fx_rates"}, []string{"marts.revenue"})), http.StatusCreated)
	e.must(e.do("checkout", "POST", "/api/v1/lineage", runEvent("FAIL", t0.Add(3*time.Minute), "export", []string{"marts.revenue"}, []string{"bi.revenue"})), http.StatusCreated)
	// A late COMPLETE for an older run must not move last_write_at backwards.
	e.must(e.do("checkout", "POST", "/api/v1/lineage", runEvent("COMPLETE", t0.Add(-time.Hour), "load", []string{"raw.orders"}, []string{"staging.orders"})), http.StatusCreated)

	get := func(name string) map[string]any {
		return e.must(e.do("", "GET", "/api/v1/datasets/by-name?namespace=iceberg://lake&name="+name, nil), http.StatusOK).body
	}
	staging := get("staging.orders")
	if at, _ := time.Parse(time.RFC3339Nano, staging["last_write_at"].(string)); !at.Equal(t0.Add(time.Minute)) {
		t.Errorf("last write should be the latest COMPLETE: %v", staging["last_write_at"])
	}
	if get("bi.revenue")["last_write_at"] != nil {
		t.Error("a FAIL is not a write")
	}

	names := func(path string) map[string]int {
		g := e.must(e.do("", "GET", path, nil), http.StatusOK).body
		out := map[string]int{}
		for _, n := range g["nodes"].([]any) {
			m := n.(map[string]any)
			out[m["name"].(string)] = int(num(m["depth"]))
		}
		return out
	}
	mart := num(get("marts.revenue")["id"])
	up1 := names(fmt.Sprintf("/api/v1/datasets/%d/lineage?direction=upstream&depth=1", mart))
	want1 := map[string]int{"marts.revenue": 0, "model": -1, "staging.orders": -2, "raw.fx_rates": -2}
	if fmt.Sprint(up1) != fmt.Sprint(want1) {
		t.Errorf("upstream depth 1: %v", up1)
	}
	up2 := names(fmt.Sprintf("/api/v1/datasets/%d/lineage?direction=upstream&depth=2", mart))
	if up2["raw.orders"] != -4 || up2["load"] != -3 {
		t.Errorf("upstream depth 2: %v", up2)
	}
	both := names(fmt.Sprintf("/api/v1/datasets/%d/lineage?direction=both&depth=1", mart))
	if both["bi.revenue"] != 2 || both["export"] != 1 || both["staging.orders"] != -2 {
		t.Errorf("both: %v", both)
	}
	e.must(e.do("", "GET", fmt.Sprintf("/api/v1/datasets/%d/lineage?depth=11", mart), nil), http.StatusBadRequest)
}

func TestFreshnessEvaluator(t *testing.T) {
	e := newEnv(t)
	id := e.createOrders(60)
	wrote := time.Now().Add(-50 * time.Second)
	e.must(e.do("checkout", "POST", "/api/v1/lineage", runEvent("COMPLETE", wrote, "load", nil, []string{"sales.orders"})), http.StatusCreated)

	ctx := context.Background()
	status := func() string {
		return e.must(e.do("", "GET", fmt.Sprintf("/api/v1/datasets/%d", id), nil), http.StatusOK).body["slo_status"].(string)
	}
	e.eval.Now = func() time.Time { return wrote.Add(40 * time.Second) }
	if _, err := e.eval.EvaluateOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if s := status(); s != "healthy" {
		t.Fatalf("40 s into a 60 s SLO: %s", s)
	}
	e.eval.Now = func() time.Time { return wrote.Add(50 * time.Second) }
	_, _ = e.eval.EvaluateOnce(ctx)
	if s := status(); s != "at_risk" {
		t.Fatalf("50 s into a 60 s SLO: %s", s)
	}
	e.eval.Now = func() time.Time { return wrote.Add(63 * time.Second) }
	sum, _ := e.eval.EvaluateOnce(ctx)
	if s := status(); s != "breached" || sum.Transitions != 1 {
		t.Fatalf("63 s into a 60 s SLO: %s, %+v", s, sum)
	}
	h := e.must(e.do("", "GET", fmt.Sprintf("/api/v1/datasets/%d/slo-history", id), nil), http.StatusOK)
	last := h.body["transitions"].([]any)[0].(map[string]any)
	deadline, _ := time.Parse(time.RFC3339Nano, last["deadline_at"].(string))
	detected, _ := time.Parse(time.RFC3339Nano, last["detected_at"].(string))
	if d := detected.Sub(deadline); d < 2900*time.Millisecond || d > 3100*time.Millisecond {
		t.Errorf("detection delay should be recorded as ~3 s, got %v", d)
	}

	// A new successful write recovers it at the next evaluation.
	e.must(e.do("checkout", "POST", "/api/v1/lineage", runEvent("COMPLETE", wrote.Add(64*time.Second), "load", nil, []string{"sales.orders"})), http.StatusCreated)
	e.eval.Now = func() time.Time { return wrote.Add(65 * time.Second) }
	_, _ = e.eval.EvaluateOnce(ctx)
	if s := status(); s != "healthy" {
		t.Fatalf("after a fresh write: %s", s)
	}
	m := e.do("", "GET", "/metrics", nil)
	if !strings.Contains(m.raw, `registry_dataset_slo_status{dataset="iceberg://lake/sales.orders",owner_team="checkout",status="healthy",tier="gold"} 1`) {
		t.Errorf("metrics should expose the status")
	}
}
