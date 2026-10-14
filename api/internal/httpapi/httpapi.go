// Package httpapi exposes the registry over HTTP: JSON in, JSON or RFC 9457
// problem details out.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/technobasant/dataset-registry/api/internal/auth"
	"github.com/technobasant/dataset-registry/api/internal/lineage"
	"github.com/technobasant/dataset-registry/api/internal/problem"
	"github.com/technobasant/dataset-registry/api/internal/registry"
	"github.com/technobasant/dataset-registry/api/internal/telemetry"
	"github.com/technobasant/dataset-registry/api/openapi"
)

// Config wires the handlers.
type Config struct {
	Registry  *registry.Service
	Lineage   *lineage.Service
	Issuer    *auth.Issuer
	Metrics   *telemetry.Metrics
	Log       *slog.Logger
	DevTokens bool
}

type api struct{ Config }

const maxBody = 1 << 20

// New returns the HTTP handler.
func New(c Config) http.Handler {
	a := &api{c}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, a.recoverer, a.observe)
	r.Use(c.Issuer.Middleware(func(w http.ResponseWriter, r *http.Request, err error) {
		a.fail(w, r, problem.Unauthorized("invalid token: %v", err))
	}))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := c.Registry.Ping(r.Context()); err != nil {
			a.fail(w, r, &problem.Error{Status: http.StatusServiceUnavailable, Code: "not_ready", Detail: err.Error()})
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	r.Handle("/metrics", promhttp.HandlerFor(c.Metrics.Registry, promhttp.HandlerOpts{}))
	r.Get("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(openapi.Spec)
	})

	r.Route("/api/v1", func(r chi.Router) {
		if c.DevTokens {
			r.Post("/dev/tokens", a.devToken)
		}
		r.Get("/whoami", func(w http.ResponseWriter, r *http.Request) { a.ok(w, http.StatusOK, auth.From(r.Context())) })

		r.Get("/datasets", a.listDatasets)
		r.Post("/datasets", a.createDataset)
		r.Get("/datasets/by-name", a.datasetByName)
		r.Route("/datasets/{id}", func(r chi.Router) {
			r.Get("/", a.getDataset)
			r.Patch("/", a.updateDataset)
			r.Delete("/", a.deleteDataset)
			r.Get("/schemas", a.listSchemas)
			r.Post("/schemas", a.proposeSchema)
			r.Get("/schemas/{version}", a.getSchema)
			r.Post("/compatibility-check", a.checkSchema)
			r.Get("/compatibility", a.compareVersions)
			r.Get("/consumers", a.listConsumers)
			r.Post("/consumers", a.addConsumer)
			r.Delete("/consumers/{team}", a.removeConsumer)
			r.Get("/lineage", a.lineageQuery)
			r.Get("/slo-history", a.sloHistory)
		})

		r.Get("/change-requests", a.listChangeRequests)
		r.Get("/change-requests/{id}", a.getChangeRequest)
		r.Post("/change-requests/{id}/reviews", a.review)
		r.Post("/change-requests/{id}/publish", a.publish)
		r.Post("/change-requests/{id}/withdraw", a.withdraw)

		r.Post("/lineage", a.ingest)
		r.Get("/audit", a.listAudit)
	})
	return otelhttp.NewHandler(r, "http",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + r.URL.Path }))
}

// ---- plumbing ----

func (a *api) ok(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	pe, ok := problem.As(err)
	if !ok {
		a.Log.ErrorContext(r.Context(), "request failed", "err", err, "path", r.URL.Path)
		pe = &problem.Error{Status: http.StatusInternalServerError, Code: "internal", Detail: "internal error"}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(pe.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "about:blank", "title": http.StatusText(pe.Status), "status": pe.Status,
		"code": pe.Code, "detail": pe.Detail, "extra": pe.Extra,
	})
}

func (a *api) decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return problem.BadRequest("invalid JSON body: %v", err)
	}
	return nil
}

func (a *api) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				a.Log.ErrorContext(r.Context(), "panic", "value", v, "path", r.URL.Path)
				a.fail(w, r, errors.New("panic"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// observe logs each request and records its latency under the route pattern,
// so /datasets/17 and /datasets/18 share a series.
func (a *api) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		took := time.Since(start)
		a.Metrics.HTTPDuration.WithLabelValues(r.Method, route, strconv.Itoa(ww.Status())).Observe(took.Seconds())
		if route == "/metrics" || route == "/healthz" {
			return
		}
		level := slog.LevelInfo
		if ww.Status() >= 500 {
			level = slog.LevelError
		}
		a.Log.Log(r.Context(), level, "http", "method", r.Method, "route", route, "status", ww.Status(),
			"duration_ms", float64(took.Microseconds())/1000, "request_id", middleware.GetReqID(r.Context()))
	})
}

func pathID(r *http.Request, key string) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, key), 10, 64)
	if err != nil || id <= 0 {
		return 0, problem.BadRequest("%s must be a positive integer", key)
	}
	return id, nil
}

func queryInt(r *http.Request, key string, def int) (int, error) {
	s := r.URL.Query().Get(key)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, problem.BadRequest("%s must be an integer", key)
	}
	return n, nil
}

func optString(r *http.Request, key string) *string {
	if v := r.URL.Query().Get(key); v != "" {
		return &v
	}
	return nil
}

// ---- handlers ----

func (a *api) devToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Subject string `json:"subject"`
		Role    string `json:"role"`
		Team    string `json:"team"`
	}
	if err := a.decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	role, err := auth.ParseRole(in.Role)
	if err != nil {
		a.fail(w, r, problem.BadRequest("%v", err))
		return
	}
	tok, err := a.Issuer.Issue(auth.Principal{Subject: in.Subject, Role: role, Team: in.Team})
	if err != nil {
		a.fail(w, r, problem.BadRequest("%v", err))
		return
	}
	a.ok(w, http.StatusCreated, map[string]string{"token": tok, "warning": "development token issuer; not production authentication"})
}

func (a *api) listDatasets(w http.ResponseWriter, r *http.Request) {
	f := registry.ListFilter{SLOStatus: optString(r, "slo_status"), OwnerTeam: optString(r, "owner_team")}
	if v := r.URL.Query().Get("registered"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			a.fail(w, r, problem.BadRequest("registered must be true or false"))
			return
		}
		f.Registered = &b
	}
	limit, err := queryInt(r, "limit", 1000)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	f.Limit = int32(limit)
	out, err := a.Registry.ListDatasets(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, map[string]any{"datasets": out})
}

func (a *api) createDataset(w http.ResponseWriter, r *http.Request) {
	var in registry.DatasetInput
	if err := a.decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	d, err := a.Registry.CreateDataset(r.Context(), auth.From(r.Context()), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusCreated, d)
}

func (a *api) datasetByName(w http.ResponseWriter, r *http.Request) {
	ns, name := r.URL.Query().Get("namespace"), r.URL.Query().Get("name")
	if ns == "" || name == "" {
		a.fail(w, r, problem.BadRequest("namespace and name are required"))
		return
	}
	d, err := a.Registry.GetDatasetByName(r.Context(), ns, name)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, d)
}

func (a *api) getDataset(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	d, err := a.Registry.GetDataset(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, d)
}

func (a *api) updateDataset(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in registry.DatasetInput
	if err := a.decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	d, err := a.Registry.UpdateDataset(r.Context(), auth.From(r.Context()), id, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, d)
}

func (a *api) deleteDataset(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Registry.DeleteDataset(r.Context(), auth.From(r.Context()), id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) listSchemas(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	vs, err := a.Registry.ListSchemas(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, map[string]any{"versions": vs})
}

type schemaBody struct {
	Schema json.RawMessage `json:"schema"`
}

func (a *api) proposeSchema(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in schemaBody
	if err := a.decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.Registry.ProposeSchema(r.Context(), auth.From(r.Context()), id, in.Schema)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if !res.Published {
		status = http.StatusAccepted
	}
	a.ok(w, status, res)
}

func (a *api) getSchema(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	v, err := pathID(r, "version")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	s, err := a.Registry.GetSchema(r.Context(), id, int32(v))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, s)
}

func (a *api) checkSchema(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in schemaBody
	if err := a.decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	rep, err := a.Registry.CheckSchema(r.Context(), id, in.Schema)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, rep)
}

func (a *api) compareVersions(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	from, err := queryInt(r, "from", 0)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	to, err := queryInt(r, "to", 0)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if from <= 0 || to <= 0 {
		a.fail(w, r, problem.BadRequest("from and to versions are required"))
		return
	}
	rep, err := a.Registry.CompareVersions(r.Context(), id, int32(from), int32(to))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, rep)
}

func (a *api) listConsumers(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	cs, err := a.Registry.ListConsumers(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, map[string]any{"consumers": cs})
}

func (a *api) addConsumer(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in registry.ConsumerInput
	if err := a.decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	c, err := a.Registry.AddConsumer(r.Context(), auth.From(r.Context()), id, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusCreated, c)
}

func (a *api) removeConsumer(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Registry.RemoveConsumer(r.Context(), auth.From(r.Context()), id, chi.URLParam(r, "team")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) lineageQuery(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	depth, err := queryInt(r, "depth", 2)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	dir := r.URL.Query().Get("direction")
	if dir == "" {
		dir = "both"
	}
	g, err := a.Lineage.Query(r.Context(), id, dir, depth)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, g)
}

func (a *api) sloHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ts, err := a.Registry.SLOHistory(r.Context(), id, int32(limit))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, map[string]any{"transitions": ts})
}

func (a *api) listChangeRequests(w http.ResponseWriter, r *http.Request) {
	f := registry.CRFilter{State: optString(r, "state"), ConsumerTeam: optString(r, "consumer_team")}
	if v := r.URL.Query().Get("dataset_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			a.fail(w, r, problem.BadRequest("dataset_id must be an integer"))
			return
		}
		f.DatasetID = &id
	}
	out, err := a.Registry.ListChangeRequests(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, map[string]any{"change_requests": out})
}

func (a *api) getChangeRequest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	cr, err := a.Registry.GetChangeRequest(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, cr)
}

func (a *api) review(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in registry.ReviewInput
	if err := a.decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	cr, err := a.Registry.Review(r.Context(), auth.From(r.Context()), id, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, cr)
}

func (a *api) publish(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	cr, err := a.Registry.Publish(r.Context(), auth.From(r.Context()), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, cr)
}

func (a *api) withdraw(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	cr, err := a.Registry.Withdraw(r.Context(), auth.From(r.Context()), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, cr)
}

// ingest accepts one OpenLineage RunEvent, as the OpenLineage HTTP transport
// sends it. Pipelines present an owner or admin token.
func (a *api) ingest(w http.ResponseWriter, r *http.Request) {
	p := auth.From(r.Context())
	if p.IsAnonymous() {
		a.fail(w, r, problem.Unauthorized("lineage ingest needs an owner or admin token"))
		return
	}
	if p.Role == auth.Consumer {
		a.fail(w, r, problem.Forbidden("consumers read lineage; pipelines (owner or admin tokens) write it"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		a.fail(w, r, problem.BadRequest("read body: %v", err))
		return
	}
	res, err := a.Lineage.Ingest(r.Context(), body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusCreated, res)
}

func (a *api) listAudit(w http.ResponseWriter, r *http.Request) {
	var entityID *int64
	if v := r.URL.Query().Get("entity_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			a.fail(w, r, problem.BadRequest("entity_id must be an integer"))
			return
		}
		entityID = &id
	}
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out, err := a.Registry.ListAudit(r.Context(), optString(r, "entity_type"), entityID, int32(limit))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.ok(w, http.StatusOK, map[string]any{"entries": out})
}
