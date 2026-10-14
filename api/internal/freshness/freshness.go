// Package freshness compares each dataset's last successful write with the
// staleness its owner committed to, and records when that changes.
//
// The evaluator runs on an interval. Breach detection therefore lags the real
// breach by up to one interval plus the time an evaluation takes; MEASUREMENTS.md
// measures that lag rather than assuming it.
package freshness

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/technobasant/dataset-registry/api/internal/db"
	"github.com/technobasant/dataset-registry/api/internal/telemetry"
)

// Status of a dataset against its freshness SLO.
type Status string

// Statuses.
const (
	Unknown  Status = "unknown"
	Healthy  Status = "healthy"
	AtRisk   Status = "at_risk"
	Breached Status = "breached"
)

// DefaultAtRiskRatio marks a dataset at risk once it has used 75% of its SLO.
const DefaultAtRiskRatio = 0.75

// Classify decides a status from staleness and the SLO.
func Classify(staleness, slo time.Duration, atRiskRatio float64) Status {
	switch {
	case slo <= 0:
		return Unknown
	case staleness > slo:
		return Breached
	case float64(staleness) > atRiskRatio*float64(slo):
		return AtRisk
	default:
		return Healthy
	}
}

// Evaluator checks every dataset that has a freshness SLO.
type Evaluator struct {
	pool        *pgxpool.Pool
	q           *db.Queries
	log         *slog.Logger
	metrics     *telemetry.Metrics
	Interval    time.Duration
	AtRiskRatio float64
	Now         func() time.Time
}

// New returns an evaluator; zero values pick the defaults.
func New(pool *pgxpool.Pool, log *slog.Logger, m *telemetry.Metrics, interval time.Duration) *Evaluator {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Evaluator{pool: pool, q: db.New(pool), log: log, metrics: m, Interval: interval,
		AtRiskRatio: DefaultAtRiskRatio, Now: time.Now}
}

// Run evaluates until ctx is cancelled.
func (e *Evaluator) Run(ctx context.Context) {
	t := time.NewTicker(e.Interval)
	defer t.Stop()
	for {
		if _, err := e.EvaluateOnce(ctx); err != nil && ctx.Err() == nil {
			e.log.Error("freshness evaluation failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Summary is one evaluation's outcome.
type Summary struct {
	Evaluated   int
	Transitions int
	ByStatus    map[Status]int
	Took        time.Duration
}

// EvaluateOnce checks every SLO and records status changes.
func (e *Evaluator) EvaluateOnce(ctx context.Context) (Summary, error) {
	start := time.Now()
	rows, err := e.q.ListSLODatasets(ctx)
	if err != nil {
		return Summary{}, err
	}
	now := e.Now()
	sum := Summary{ByStatus: map[Status]int{Unknown: 0, Healthy: 0, AtRisk: 0, Breached: 0}}
	if e.metrics != nil {
		e.metrics.Staleness.Reset()
		e.metrics.SLOSeconds.Reset()
		e.metrics.SLOStatus.Reset()
	}
	for _, r := range rows {
		// A dataset that has never been written is measured from registration.
		since := r.CreatedAt
		if r.LastWriteAt != nil {
			since = *r.LastWriteAt
		}
		slo := time.Duration(*r.FreshnessSloSeconds) * time.Second
		staleness := now.Sub(since)
		if staleness < 0 {
			staleness = 0
		}
		status := Classify(staleness, slo, e.AtRiskRatio)
		sum.Evaluated++
		sum.ByStatus[status]++
		e.export(r, staleness, slo, status)

		if Status(r.SloStatus) == status {
			continue
		}
		deadline := since.Add(slo)
		if err := e.transition(ctx, r.ID, Status(r.SloStatus), status, staleness, deadline, now); err != nil {
			return sum, err
		}
		sum.Transitions++
		if e.metrics != nil {
			e.metrics.Transitions.WithLabelValues(string(status)).Inc()
			if status == Breached {
				e.metrics.DetectionDelay.Observe(now.Sub(deadline).Seconds())
			}
		}
		e.log.InfoContext(ctx, "freshness status changed", "dataset", r.Namespace+"/"+r.Name,
			"from", r.SloStatus, "to", status, "staleness_s", staleness.Seconds(), "slo_s", slo.Seconds())
	}
	sum.Took = time.Since(start)
	if e.metrics != nil {
		for st, n := range sum.ByStatus {
			e.metrics.DatasetsByStatus.WithLabelValues(string(st)).Set(float64(n))
		}
		e.metrics.EvaluationSeconds.Observe(sum.Took.Seconds())
	}
	return sum, nil
}

func (e *Evaluator) export(r db.ListSLODatasetsRow, staleness, slo time.Duration, status Status) {
	if e.metrics == nil {
		return
	}
	name := r.Namespace + "/" + r.Name
	team := ""
	if r.OwnerTeam != nil {
		team = *r.OwnerTeam
	}
	e.metrics.Staleness.WithLabelValues(name, team, r.Tier).Set(staleness.Seconds())
	e.metrics.SLOSeconds.WithLabelValues(name, team, r.Tier).Set(slo.Seconds())
	for _, st := range []Status{Healthy, AtRisk, Breached} {
		v := 0.0
		if st == status {
			v = 1
		}
		e.metrics.SLOStatus.WithLabelValues(name, team, r.Tier, string(st)).Set(v)
	}
}

func (e *Evaluator) transition(ctx context.Context, id int64, from, to Status, staleness time.Duration, deadline, now time.Time) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := e.q.WithTx(tx)
	if err := q.SetSLOStatus(ctx, db.SetSLOStatusParams{ID: id, SloStatus: string(to), SloStatusSince: &now}); err != nil {
		return err
	}
	var dl *time.Time
	if to == Breached {
		dl = &deadline
	}
	if err := q.InsertSLOTransition(ctx, db.InsertSLOTransitionParams{DatasetID: id, FromStatus: string(from), ToStatus: string(to),
		StalenessMs: staleness.Milliseconds(), DeadlineAt: dl, DetectedAt: now}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
