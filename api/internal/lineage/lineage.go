// Package lineage ingests OpenLineage run events and answers upstream and
// downstream questions over the graph they build.
//
// Every event that names inputs or outputs adds edges dataset → job → dataset.
// A COMPLETE event is also a successful write to each output, which is what the
// freshness evaluator measures against. Datasets that only appear in lineage
// are created unregistered: something writes them, and nobody owns them yet.
package lineage

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/technobasant/dataset-registry/api/internal/db"
	"github.com/technobasant/dataset-registry/api/internal/problem"
	"github.com/technobasant/dataset-registry/api/internal/telemetry"
)

//go:embed spec/OpenLineage.json
var specJSON []byte

const specURL = "https://openlineage.io/spec/2-0-2/OpenLineage.json"

// Service ingests events and queries the graph.
type Service struct {
	pool    *pgxpool.Pool
	q       *db.Queries
	runSch  *jsonschema.Schema
	log     *slog.Logger
	metrics *telemetry.Metrics
}

// New compiles the embedded OpenLineage schema.
func New(pool *pgxpool.Pool, log *slog.Logger, m *telemetry.Metrics) (*Service, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(specJSON))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(specURL, doc); err != nil {
		return nil, err
	}
	sch, err := c.Compile(specURL + "#/$defs/RunEvent")
	if err != nil {
		return nil, fmt.Errorf("compile OpenLineage RunEvent schema: %w", err)
	}
	return &Service{pool: pool, q: db.New(pool), runSch: sch, log: log, metrics: m}, nil
}

// RunEvent is the part of an OpenLineage RunEvent the registry uses.
type RunEvent struct {
	EventType string    `json:"eventType"`
	EventTime time.Time `json:"eventTime"`
	Producer  string    `json:"producer"`
	Run       struct {
		RunID uuid.UUID `json:"runId"`
	} `json:"run"`
	Job     Ref   `json:"job"`
	Inputs  []Ref `json:"inputs"`
	Outputs []Ref `json:"outputs"`
}

// Ref names a job or dataset.
type Ref struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Result says what an event changed.
type Result struct {
	JobID         int64 `json:"job_id"`
	Datasets      int   `json:"datasets"`
	Edges         int   `json:"edges"`
	WritesUpdated int   `json:"writes_recorded"`
}

// Validate checks body against the OpenLineage RunEvent schema and decodes it.
func (s *Service) Validate(body []byte) (RunEvent, error) {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return RunEvent{}, problem.BadRequest("body is not JSON: %v", err)
	}
	if err := s.runSch.Validate(inst); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return RunEvent{}, problem.BadRequest("not a valid OpenLineage RunEvent").With(ve.BasicOutput())
		}
		return RunEvent{}, problem.BadRequest("not a valid OpenLineage RunEvent: %v", err)
	}
	var ev RunEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return RunEvent{}, problem.BadRequest("decode RunEvent: %v", err)
	}
	if ev.EventType == "" {
		// eventType is optional in the spec; without it the event says nothing
		// about success, so it only contributes edges.
		ev.EventType = "OTHER"
	}
	return ev, nil
}

// Ingest validates and stores one event.
func (s *Service) Ingest(ctx context.Context, body []byte) (Result, error) {
	start := time.Now()
	ev, err := s.Validate(body)
	if err != nil {
		s.observe("invalid", "rejected", start)
		return Result{}, err
	}
	var res Result
	for attempt := 0; ; attempt++ {
		res, err = s.store(ctx, ev)
		if err == nil || attempt == 2 || !retryable(err) {
			break
		}
	}
	if err != nil {
		s.observe(ev.EventType, "error", start)
		return Result{}, err
	}
	s.observe(ev.EventType, "stored", start)
	return res, nil
}

func (s *Service) observe(eventType, result string, start time.Time) {
	if s.metrics == nil {
		return
	}
	s.metrics.LineageEvents.WithLabelValues(eventType, result).Inc()
	s.metrics.LineageDuration.Observe(time.Since(start).Seconds())
}

func retryable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "40P01" || pg.Code == "40001")
}

type dsRef struct {
	Ref
	direction string
}

func (s *Service) store(ctx context.Context, ev RunEvent) (Result, error) {
	// Lock datasets in one global order so concurrent events that share
	// datasets cannot deadlock.
	refs := make([]dsRef, 0, len(ev.Inputs)+len(ev.Outputs))
	for _, r := range ev.Inputs {
		refs = append(refs, dsRef{r, "input"})
	}
	for _, r := range ev.Outputs {
		refs = append(refs, dsRef{r, "output"})
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.direction < b.direction
	})

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	state := strings.ToUpper(ev.EventType)
	runID := ev.Run.RunID
	jobID, err := q.UpsertJob(ctx, db.UpsertJobParams{Namespace: ev.Job.Namespace, Name: ev.Job.Name,
		LastRunID: &runID, LastState: &state, LastEventAt: &ev.EventTime})
	if err != nil {
		return Result{}, err
	}
	res := Result{JobID: jobID}
	ids := map[Ref]int64{}
	for _, r := range refs {
		id, ok := ids[r.Ref]
		if !ok {
			id, err = s.datasetID(ctx, q, r.Ref)
			if err != nil {
				return Result{}, err
			}
			ids[r.Ref] = id
		}
		if err := q.UpsertEdge(ctx, db.UpsertEdgeParams{JobID: jobID, DatasetID: id, Direction: r.direction, FirstSeen: ev.EventTime}); err != nil {
			return Result{}, err
		}
		res.Edges++
		if r.direction == "output" && state == "COMPLETE" {
			n, err := q.RecordWrite(ctx, db.RecordWriteParams{ID: id, LastWriteAt: &ev.EventTime, LastWriteRun: &runID})
			if err != nil {
				return Result{}, err
			}
			res.WritesUpdated += int(n)
		}
	}
	res.Datasets = len(ids)
	return res, tx.Commit(ctx)
}

func (s *Service) datasetID(ctx context.Context, q *db.Queries, r Ref) (int64, error) {
	d, err := q.GetDatasetByName(ctx, db.GetDatasetByNameParams{Namespace: r.Namespace, Name: r.Name})
	if err == nil {
		return d.ID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	return q.EnsureLineageDataset(ctx, db.EnsureLineageDatasetParams{Namespace: r.Namespace, Name: r.Name})
}

// Node is a dataset or a job in a lineage graph.
type Node struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	DatasetID  int64   `json:"dataset_id,omitempty"`
	Namespace  string  `json:"namespace"`
	Name       string  `json:"name"`
	Registered bool    `json:"registered"`
	OwnerTeam  *string `json:"owner_team,omitempty"`
	SLOStatus  string  `json:"slo_status,omitempty"`
	LastState  *string `json:"last_state,omitempty"`
	Depth      int     `json:"depth"`
}

// Edge is a directed edge in a lineage graph: input → job or job → output.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph is the neighbourhood of one dataset.
type Graph struct {
	Root      string `json:"root"`
	Direction string `json:"direction"`
	Depth     int    `json:"depth"`
	Nodes     []Node `json:"nodes"`
	Edges     []Edge `json:"edges"`
}

// MaxDepth bounds a lineage query.
const MaxDepth = 10

func dsNode(id int64) string  { return fmt.Sprintf("d:%d", id) }
func jobNode(id int64) string { return fmt.Sprintf("j:%d", id) }

// Query walks up to depth dataset hops in direction "upstream", "downstream" or "both".
func (s *Service) Query(ctx context.Context, datasetID int64, direction string, depth int) (Graph, error) {
	if depth < 1 || depth > MaxDepth {
		return Graph{}, problem.BadRequest("depth must be between 1 and %d", MaxDepth)
	}
	if direction != "upstream" && direction != "downstream" && direction != "both" {
		return Graph{}, problem.BadRequest("direction must be upstream, downstream or both")
	}
	if _, err := s.q.GetDataset(ctx, datasetID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Graph{}, problem.NotFound("dataset %d not found", datasetID)
		}
		return Graph{}, err
	}
	g := Graph{Root: dsNode(datasetID), Direction: direction, Depth: depth, Nodes: []Node{}, Edges: []Edge{}}

	// Depth is in graph steps from the root: datasets at even steps, jobs at
	// odd ones, negative upstream. A node reached both ways keeps the nearest.
	dsDepth := map[int64]int{datasetID: 0}
	jobDepth := map[int64]int{}
	edges := map[Edge]bool{}
	note := func(m map[int64]int, id int64, d int) {
		if cur, ok := m[id]; !ok || abs(d) < abs(cur) {
			m[id] = d
		}
	}
	// A row is (input, job, output, hop). Upstream the output is nearer the
	// root; downstream the input is.
	add := func(input, job, output int64, hop int, up bool) {
		sign, near, far := 1, input, output
		if up {
			sign, near, far = -1, output, input
		}
		note(jobDepth, job, sign*(2*hop-1))
		if near != 0 {
			note(dsDepth, near, sign*(2*hop-2))
		}
		if far != 0 {
			note(dsDepth, far, sign*2*hop)
		}
		if input != 0 {
			edges[Edge{From: dsNode(input), To: jobNode(job)}] = true
		}
		if output != 0 {
			edges[Edge{From: jobNode(job), To: dsNode(output)}] = true
		}
	}
	if direction != "downstream" {
		rows, err := s.q.LineageUpstream(ctx, db.LineageUpstreamParams{DatasetID: datasetID, MaxDepth: int32(depth)})
		if err != nil {
			return Graph{}, err
		}
		for _, r := range rows {
			add(r.InputID, r.JobID, r.OutputID, int(r.Depth), true)
		}
	}
	if direction != "upstream" {
		rows, err := s.q.LineageDownstream(ctx, db.LineageDownstreamParams{DatasetID: datasetID, MaxDepth: int32(depth)})
		if err != nil {
			return Graph{}, err
		}
		for _, r := range rows {
			add(r.InputID, r.JobID, r.OutputID, int(r.Depth), false)
		}
	}

	dsIDs := make([]int64, 0, len(dsDepth))
	for id := range dsDepth {
		dsIDs = append(dsIDs, id)
	}
	jobIDs := make([]int64, 0, len(jobDepth))
	for id := range jobDepth {
		jobIDs = append(jobIDs, id)
	}
	datasets, err := s.q.GetDatasetsByIDs(ctx, dsIDs)
	if err != nil {
		return Graph{}, err
	}
	for _, d := range datasets {
		g.Nodes = append(g.Nodes, Node{ID: dsNode(d.ID), Kind: "dataset", DatasetID: d.ID, Namespace: d.Namespace, Name: d.Name,
			Registered: d.Registered, OwnerTeam: d.OwnerTeam, SLOStatus: d.SloStatus, Depth: dsDepth[d.ID]})
	}
	jobs, err := s.q.GetJobsByIDs(ctx, jobIDs)
	if err != nil {
		return Graph{}, err
	}
	for _, j := range jobs {
		g.Nodes = append(g.Nodes, Node{ID: jobNode(j.ID), Kind: "job", Namespace: j.Namespace, Name: j.Name, Registered: true,
			LastState: j.LastState, Depth: jobDepth[j.ID]})
	}
	sort.Slice(g.Nodes, func(i, j int) bool {
		if g.Nodes[i].Depth != g.Nodes[j].Depth {
			return g.Nodes[i].Depth < g.Nodes[j].Depth
		}
		return g.Nodes[i].ID < g.Nodes[j].ID
	})
	for e := range edges {
		g.Edges = append(g.Edges, e)
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	return g, nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
