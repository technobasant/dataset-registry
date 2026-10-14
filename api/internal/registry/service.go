// Package registry holds the rules: who may register, change and depend on a
// dataset, and how a breaking contract change waits for its consumers.
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/technobasant/dataset-registry/api/internal/auth"
	"github.com/technobasant/dataset-registry/api/internal/changereq"
	"github.com/technobasant/dataset-registry/api/internal/compat"
	"github.com/technobasant/dataset-registry/api/internal/db"
	"github.com/technobasant/dataset-registry/api/internal/problem"
	"github.com/technobasant/dataset-registry/api/internal/telemetry"
)

// Service implements the registry's operations over PostgreSQL.
type Service struct {
	pool    *pgxpool.Pool
	q       *db.Queries
	log     *slog.Logger
	metrics *telemetry.Metrics
}

// New returns a Service.
func New(pool *pgxpool.Pool, log *slog.Logger, m *telemetry.Metrics) *Service {
	return &Service{pool: pool, q: db.New(pool), log: log, metrics: m}
}

// Queries exposes read queries to sibling packages.
func (s *Service) Queries() *db.Queries { return s.q }

func (s *Service) inTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func audit(ctx context.Context, q *db.Queries, p auth.Principal, action, entity string, id int64, details any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return q.InsertAudit(ctx, db.InsertAuditParams{Actor: p.Actor(), Action: action, EntityType: entity, EntityID: id, Details: b})
}

func (s *Service) count(event string) {
	if s.metrics != nil {
		s.metrics.ChangeRequests.WithLabelValues(event).Inc()
	}
}

func notFound(err error, what string, id any) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return problem.NotFound("%s %v not found", what, id)
	}
	return err
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// ---- authorization ----

func requireSignedIn(p auth.Principal) error {
	if p.IsAnonymous() {
		return problem.Unauthorized("sign in with a bearer token to change anything")
	}
	return nil
}

// requireOwner allows the dataset's owning team (role owner) and admins.
func requireOwner(p auth.Principal, d db.Dataset) error {
	if err := requireSignedIn(p); err != nil {
		return err
	}
	if p.Role == auth.Admin {
		return nil
	}
	if p.Role == auth.Owner && d.OwnerTeam != nil && *d.OwnerTeam == p.Team {
		return nil
	}
	return problem.Forbidden("only team %s (role owner) or an admin can do this to %s/%s", deref(d.OwnerTeam), d.Namespace, d.Name)
}

// ---- datasets ----

// DatasetInput registers or updates a dataset.
type DatasetInput struct {
	Namespace           string          `json:"namespace"`
	Name                string          `json:"name"`
	Description         *string         `json:"description"`
	Owner               *Owner          `json:"owner"`
	Tier                *string         `json:"tier"`
	FreshnessSLOSeconds *int32          `json:"freshness_slo_seconds"`
	ClearSLO            bool            `json:"clear_slo"`
	Compatibility       *string         `json:"compatibility"`
	Schema              json.RawMessage `json:"schema,omitempty"`
}

func validTier(t string) bool { return t == "gold" || t == "silver" || t == "bronze" }

func (in DatasetInput) validate(creating bool) error {
	if creating {
		if strings.TrimSpace(in.Namespace) == "" || strings.TrimSpace(in.Name) == "" {
			return problem.BadRequest("namespace and name are required")
		}
		if in.Owner == nil || in.Owner.Team == "" || in.Owner.Person == "" {
			return problem.BadRequest("a dataset needs an owner: team and person")
		}
	} else if in.Owner != nil && (in.Owner.Team == "" || in.Owner.Person == "") {
		return problem.BadRequest("owner needs both team and person")
	}
	if in.Tier != nil && !validTier(*in.Tier) {
		return problem.BadRequest("tier must be gold, silver or bronze")
	}
	if in.FreshnessSLOSeconds != nil && *in.FreshnessSLOSeconds <= 0 {
		return problem.BadRequest("freshness_slo_seconds must be positive")
	}
	if in.Compatibility != nil {
		if _, err := compat.ParseMode(*in.Compatibility); err != nil {
			return problem.BadRequest("%v", err)
		}
	}
	return nil
}

// CreateDataset registers a dataset, or claims one that lineage discovered.
func (s *Service) CreateDataset(ctx context.Context, p auth.Principal, in DatasetInput) (Dataset, error) {
	if err := requireSignedIn(p); err != nil {
		return Dataset{}, err
	}
	if err := in.validate(true); err != nil {
		return Dataset{}, err
	}
	if p.Role == auth.Consumer || (p.Role == auth.Owner && in.Owner.Team != p.Team) {
		return Dataset{}, problem.Forbidden("owners register datasets for their own team (%s); admins for any team", p.Team)
	}
	tier, mode := "bronze", string(compat.Forward)
	if in.Tier != nil {
		tier = *in.Tier
	}
	if in.Compatibility != nil {
		m, _ := compat.ParseMode(*in.Compatibility)
		mode = string(m)
	}
	if len(in.Schema) > 0 {
		if err := compat.Validate(in.Schema); err != nil {
			return Dataset{}, problem.Unprocessable("schema: %v", err)
		}
	}

	var out db.Dataset
	err := s.inTx(ctx, func(q *db.Queries) error {
		prior, err := q.GetDatasetByName(ctx, db.GetDatasetByNameParams{Namespace: in.Namespace, Name: in.Name})
		claimed := err == nil && !prior.Registered
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = q.CreateDataset(ctx, db.CreateDatasetParams{
			Namespace: in.Namespace, Name: in.Name, Description: deref(in.Description),
			OwnerTeam: &in.Owner.Team, OwnerPerson: &in.Owner.Person, Tier: tier,
			FreshnessSloSeconds: in.FreshnessSLOSeconds, Compatibility: mode,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return problem.Conflict("%s/%s is already registered by team %s", in.Namespace, in.Name, deref(prior.OwnerTeam))
		}
		if err != nil {
			return err
		}
		action := "dataset.registered"
		if claimed {
			action = "dataset.claimed"
		}
		if err := audit(ctx, q, p, action, "dataset", out.ID, map[string]any{
			"namespace": out.Namespace, "name": out.Name, "owner_team": in.Owner.Team, "owner_person": in.Owner.Person,
			"tier": tier, "freshness_slo_seconds": in.FreshnessSLOSeconds, "compatibility": mode,
		}); err != nil {
			return err
		}
		if len(in.Schema) > 0 {
			_, err := s.publishFirst(ctx, q, p, out, in.Schema)
			return err
		}
		return nil
	})
	return datasetFrom(out), err
}

// ListFilter narrows ListDatasets.
type ListFilter struct {
	Registered *bool
	SLOStatus  *string
	OwnerTeam  *string
	Limit      int32
}

// ListDatasets returns datasets, owned ones first.
func (s *Service) ListDatasets(ctx context.Context, f ListFilter) ([]Dataset, error) {
	if f.Limit <= 0 || f.Limit > 20000 {
		f.Limit = 1000
	}
	rows, err := s.q.ListDatasets(ctx, db.ListDatasetsParams{Registered: f.Registered, SloStatus: f.SLOStatus, OwnerTeam: f.OwnerTeam, RowLimit: f.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]Dataset, 0, len(rows))
	for _, r := range rows {
		out = append(out, datasetFromList(r))
	}
	return out, nil
}

// GetDataset returns one dataset with everything around it.
func (s *Service) GetDataset(ctx context.Context, id int64) (DatasetDetail, error) {
	d, err := s.q.GetDataset(ctx, id)
	if err != nil {
		return DatasetDetail{}, notFound(err, "dataset", id)
	}
	out := DatasetDetail{Dataset: datasetFrom(d), Consumers: []Consumer{}, Versions: []SchemaVersion{}, SLOHistory: []SLOTransition{}}
	cs, err := s.q.ListConsumers(ctx, id)
	if err != nil {
		return out, err
	}
	for _, c := range cs {
		out.Consumers = append(out.Consumers, consumerFrom(c))
	}
	vs, err := s.q.ListSchemaVersions(ctx, id)
	if err != nil {
		return out, err
	}
	for _, v := range vs {
		out.Versions = append(out.Versions, schemaFrom(v))
		if v.Status == "published" && out.CurrentVersion == nil {
			ver := v.Version
			out.CurrentVersion = &ver
		}
	}
	n := int32(len(cs))
	out.ConsumerCount = &n
	if cr, err := s.q.OpenChangeRequestForDataset(ctx, id); err == nil {
		full, err := s.GetChangeRequest(ctx, cr.ID)
		if err != nil {
			return out, err
		}
		out.OpenChangeRequest = &full
		out.Dataset.OpenChangeRequest = &full.ID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	ts, err := s.q.ListSLOTransitions(ctx, db.ListSLOTransitionsParams{DatasetID: id, RowLimit: 20})
	if err != nil {
		return out, err
	}
	for _, t := range ts {
		out.SLOHistory = append(out.SLOHistory, SLOTransition{From: t.FromStatus, To: t.ToStatus, StalenessMs: t.StalenessMs, DeadlineAt: t.DeadlineAt, DetectedAt: t.DetectedAt})
	}
	return out, nil
}

// GetDatasetByName looks a dataset up by its OpenLineage identity.
func (s *Service) GetDatasetByName(ctx context.Context, namespace, name string) (DatasetDetail, error) {
	d, err := s.q.GetDatasetByName(ctx, db.GetDatasetByNameParams{Namespace: namespace, Name: name})
	if err != nil {
		return DatasetDetail{}, notFound(err, "dataset", namespace+"/"+name)
	}
	return s.GetDataset(ctx, d.ID)
}

// UpdateDataset changes ownership, tier, SLO or compatibility mode.
func (s *Service) UpdateDataset(ctx context.Context, p auth.Principal, id int64, in DatasetInput) (Dataset, error) {
	if err := in.validate(false); err != nil {
		return Dataset{}, err
	}
	var out db.Dataset
	err := s.inTx(ctx, func(q *db.Queries) error {
		d, err := q.GetDatasetForUpdate(ctx, id)
		if err != nil {
			return notFound(err, "dataset", id)
		}
		if !d.Registered {
			return problem.Conflict("%s/%s has no owner yet; register it first", d.Namespace, d.Name)
		}
		if err := requireOwner(p, d); err != nil {
			return err
		}
		params := db.UpdateDatasetParams{ID: id, Description: in.Description, Tier: in.Tier,
			FreshnessSloSeconds: in.FreshnessSLOSeconds, ClearSlo: in.ClearSLO}
		if in.Owner != nil {
			params.OwnerTeam, params.OwnerPerson = &in.Owner.Team, &in.Owner.Person
		}
		if in.Compatibility != nil {
			m, _ := compat.ParseMode(*in.Compatibility)
			ms := string(m)
			params.Compatibility = &ms
		}
		out, err = q.UpdateDataset(ctx, params)
		if err != nil {
			return err
		}
		return audit(ctx, q, p, "dataset.updated", "dataset", id, map[string]any{
			"before": datasetFrom(d), "after": datasetFrom(out),
		})
	})
	return datasetFrom(out), err
}

// DeleteDataset removes a dataset. An owner cannot delete a dataset that
// others depend on; an admin can, and the audit log says so.
func (s *Service) DeleteDataset(ctx context.Context, p auth.Principal, id int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		d, err := q.GetDatasetForUpdate(ctx, id)
		if err != nil {
			return notFound(err, "dataset", id)
		}
		if d.Registered {
			if err := requireOwner(p, d); err != nil {
				return err
			}
		} else if p.Role != auth.Admin {
			return problem.Forbidden("only an admin can delete a dataset nobody owns")
		}
		cs, err := q.ListConsumers(ctx, id)
		if err != nil {
			return err
		}
		if len(cs) > 0 && p.Role != auth.Admin {
			return problem.Conflict("%d team(s) depend on %s/%s; they must unregister first", len(cs), d.Namespace, d.Name)
		}
		if err := audit(ctx, q, p, "dataset.deleted", "dataset", id, map[string]any{
			"namespace": d.Namespace, "name": d.Name, "consumers": len(cs),
		}); err != nil {
			return err
		}
		_, err = q.DeleteDataset(ctx, id)
		return err
	})
}

// ---- schemas ----

// ProposeResult is what happened to a proposed schema.
type ProposeResult struct {
	Version       SchemaVersion  `json:"version"`
	Published     bool           `json:"published"`
	Report        *compat.Report `json:"report,omitempty"`
	ChangeRequest *ChangeRequest `json:"change_request,omitempty"`
}

func (s *Service) publishFirst(ctx context.Context, q *db.Queries, p auth.Principal, d db.Dataset, schema []byte) (db.SchemaVersion, error) {
	v, err := q.CreateSchemaVersion(ctx, db.CreateSchemaVersionParams{DatasetID: d.ID, Version: 1, Schema: schema, Status: "published", CreatedBy: p.Actor()})
	if err != nil {
		return v, err
	}
	return v, audit(ctx, q, p, "schema.published", "dataset", d.ID, map[string]any{"version": 1, "first": true})
}

// ProposeSchema adds a contract version. A compatible change is published at
// once; a breaking one opens a change request that every registered consumer
// must acknowledge.
func (s *Service) ProposeSchema(ctx context.Context, p auth.Principal, datasetID int64, schema json.RawMessage) (ProposeResult, error) {
	if err := compat.Validate(schema); err != nil {
		return ProposeResult{}, problem.Unprocessable("schema: %v", err)
	}
	var res ProposeResult
	var crID int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		d, err := q.GetDatasetForUpdate(ctx, datasetID)
		if err != nil {
			return notFound(err, "dataset", datasetID)
		}
		if !d.Registered {
			return problem.Conflict("%s/%s has no owner; register it before giving it a contract", d.Namespace, d.Name)
		}
		if err := requireOwner(p, d); err != nil {
			return err
		}
		if open, err := q.OpenChangeRequestForDataset(ctx, d.ID); err == nil {
			return problem.Conflict("change request #%d is still %s; publish or withdraw it first", open.ID, open.State)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		latest, err := q.LatestPublishedSchema(ctx, d.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			v, err := s.publishFirst(ctx, q, p, d, schema)
			res = ProposeResult{Version: schemaFrom(v), Published: true}
			return err
		}
		if err != nil {
			return err
		}
		if sameJSON(latest.Schema, schema) {
			return problem.Unprocessable("identical to the published version %d", latest.Version)
		}
		mode, _ := compat.ParseMode(d.Compatibility)
		report, err := compat.Compare(latest.Schema, schema, mode)
		if err != nil {
			return err
		}
		res.Report = &report
		next, err := q.NextSchemaVersion(ctx, d.ID)
		if err != nil {
			return err
		}

		if report.Compatible {
			v, err := q.CreateSchemaVersion(ctx, db.CreateSchemaVersionParams{DatasetID: d.ID, Version: next, Schema: schema, Status: "published", CreatedBy: p.Actor()})
			if err != nil {
				return err
			}
			res.Version, res.Published = schemaFrom(v), true
			return audit(ctx, q, p, "schema.published", "dataset", d.ID, map[string]any{
				"version": next, "from_version": latest.Version, "changes": len(report.Changes), "mode": mode,
			})
		}

		v, err := q.CreateSchemaVersion(ctx, db.CreateSchemaVersionParams{DatasetID: d.ID, Version: next, Schema: schema, Status: "pending", CreatedBy: p.Actor()})
		if err != nil {
			return err
		}
		res.Version = schemaFrom(v)
		consumers, err := q.ListConsumers(ctx, d.ID)
		if err != nil {
			return err
		}
		rb, _ := json.Marshal(report)
		cr, err := q.CreateChangeRequest(ctx, db.CreateChangeRequestParams{
			DatasetID: d.ID, SchemaVersionID: v.ID, FromVersion: latest.Version,
			State: string(changereq.Initial(len(consumers))), Report: rb, OpenedBy: p.Actor(),
		})
		if isUniqueViolation(err) {
			return problem.Conflict("another change request for this dataset was opened at the same time")
		}
		if err != nil {
			return err
		}
		teams := make([]string, 0, len(consumers))
		for _, c := range consumers {
			if err := q.AddReview(ctx, db.AddReviewParams{ChangeRequestID: cr.ID, ConsumerTeam: c.Team}); err != nil {
				return err
			}
			teams = append(teams, c.Team)
		}
		crID = cr.ID
		return audit(ctx, q, p, "change_request.opened", "change_request", cr.ID, map[string]any{
			"dataset_id": d.ID, "from_version": latest.Version, "to_version": next, "mode": mode,
			"breaking_changes": report.BreakingCount, "awaiting": teams, "state": cr.State,
		})
	})
	if err != nil {
		return ProposeResult{}, err
	}
	if crID != 0 {
		s.count("opened")
		cr, err := s.GetChangeRequest(ctx, crID)
		if err != nil {
			return res, err
		}
		res.ChangeRequest = &cr
	}
	return res, nil
}

func sameJSON(a, b []byte) bool {
	ca, err1 := compat.Canonical(a)
	cb, err2 := compat.Canonical(b)
	return err1 == nil && err2 == nil && bytes.Equal(ca, cb)
}

// CheckSchema reports what proposing schema would do, without storing it.
func (s *Service) CheckSchema(ctx context.Context, datasetID int64, schema json.RawMessage) (compat.Report, error) {
	if err := compat.Validate(schema); err != nil {
		return compat.Report{}, problem.Unprocessable("schema: %v", err)
	}
	d, err := s.q.GetDataset(ctx, datasetID)
	if err != nil {
		return compat.Report{}, notFound(err, "dataset", datasetID)
	}
	latest, err := s.q.LatestPublishedSchema(ctx, datasetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return compat.Report{Mode: compat.Mode(d.Compatibility), Compatible: true, Changes: []compat.Change{}}, nil
	}
	if err != nil {
		return compat.Report{}, err
	}
	mode, _ := compat.ParseMode(d.Compatibility)
	return compat.Compare(latest.Schema, schema, mode)
}

// CompareVersions reports the differences between two stored versions.
func (s *Service) CompareVersions(ctx context.Context, datasetID int64, from, to int32) (compat.Report, error) {
	d, err := s.q.GetDataset(ctx, datasetID)
	if err != nil {
		return compat.Report{}, notFound(err, "dataset", datasetID)
	}
	a, err := s.q.GetSchemaVersion(ctx, db.GetSchemaVersionParams{DatasetID: datasetID, Version: from})
	if err != nil {
		return compat.Report{}, notFound(err, "version", from)
	}
	b, err := s.q.GetSchemaVersion(ctx, db.GetSchemaVersionParams{DatasetID: datasetID, Version: to})
	if err != nil {
		return compat.Report{}, notFound(err, "version", to)
	}
	mode, _ := compat.ParseMode(d.Compatibility)
	return compat.Compare(a.Schema, b.Schema, mode)
}

// ListSchemas returns every version, newest first.
func (s *Service) ListSchemas(ctx context.Context, datasetID int64) ([]SchemaVersion, error) {
	if _, err := s.q.GetDataset(ctx, datasetID); err != nil {
		return nil, notFound(err, "dataset", datasetID)
	}
	vs, err := s.q.ListSchemaVersions(ctx, datasetID)
	out := make([]SchemaVersion, 0, len(vs))
	for _, v := range vs {
		out = append(out, schemaFrom(v))
	}
	return out, err
}

// GetSchema returns one version.
func (s *Service) GetSchema(ctx context.Context, datasetID int64, version int32) (SchemaVersion, error) {
	v, err := s.q.GetSchemaVersion(ctx, db.GetSchemaVersionParams{DatasetID: datasetID, Version: version})
	if err != nil {
		return SchemaVersion{}, notFound(err, "version", version)
	}
	return schemaFrom(v), nil
}

// ---- consumers ----

// ConsumerInput registers a dependency.
type ConsumerInput struct {
	Team    string `json:"team"`
	Contact string `json:"contact"`
	Purpose string `json:"purpose"`
}

// AddConsumer records that a team depends on a dataset. Only owned datasets
// can be depended on: ownership before consumption.
func (s *Service) AddConsumer(ctx context.Context, p auth.Principal, datasetID int64, in ConsumerInput) (Consumer, error) {
	if err := requireSignedIn(p); err != nil {
		return Consumer{}, err
	}
	if in.Team == "" {
		in.Team = p.Team
	}
	if in.Contact == "" {
		return Consumer{}, problem.BadRequest("contact is required: who should hear about breaking changes")
	}
	if p.Role != auth.Admin && in.Team != p.Team {
		return Consumer{}, problem.Forbidden("you can register your own team (%s) as a consumer, not %s", p.Team, in.Team)
	}
	var out db.Consumer
	err := s.inTx(ctx, func(q *db.Queries) error {
		d, err := q.GetDatasetForUpdate(ctx, datasetID)
		if err != nil {
			return notFound(err, "dataset", datasetID)
		}
		if !d.Registered {
			return problem.Conflict("%s/%s has no owner. Nobody can depend on it until someone owns it", d.Namespace, d.Name)
		}
		out, err = q.AddConsumer(ctx, db.AddConsumerParams{DatasetID: datasetID, Team: in.Team, Contact: in.Contact, Purpose: in.Purpose})
		if errors.Is(err, pgx.ErrNoRows) {
			return problem.Conflict("team %s is already a consumer", in.Team)
		}
		if err != nil {
			return err
		}
		return audit(ctx, q, p, "consumer.registered", "dataset", datasetID, in)
	})
	return consumerFrom(out), err
}

// RemoveConsumer drops a dependency. A pending review from that team on an open
// change request is dropped too, which may approve it.
func (s *Service) RemoveConsumer(ctx context.Context, p auth.Principal, datasetID int64, team string) error {
	if err := requireSignedIn(p); err != nil {
		return err
	}
	if p.Role != auth.Admin && team != p.Team {
		return problem.Forbidden("you can unregister your own team (%s) only", p.Team)
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		if _, err := q.GetDatasetForUpdate(ctx, datasetID); err != nil {
			return notFound(err, "dataset", datasetID)
		}
		n, err := q.DeleteConsumer(ctx, db.DeleteConsumerParams{DatasetID: datasetID, Team: team})
		if err != nil {
			return err
		}
		if n == 0 {
			return problem.NotFound("team %s is not a consumer of dataset %d", team, datasetID)
		}
		if err := audit(ctx, q, p, "consumer.unregistered", "dataset", datasetID, map[string]any{"team": team}); err != nil {
			return err
		}
		cr, err := q.OpenChangeRequestForDataset(ctx, datasetID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		dropped, err := q.DropUndecidedReview(ctx, db.DropUndecidedReviewParams{ChangeRequestID: cr.ID, ConsumerTeam: team})
		if err != nil || dropped == 0 {
			return err
		}
		return s.maybeApprove(ctx, q, p, cr)
	})
}

// ListConsumers returns a dataset's consumers.
func (s *Service) ListConsumers(ctx context.Context, datasetID int64) ([]Consumer, error) {
	cs, err := s.q.ListConsumers(ctx, datasetID)
	out := make([]Consumer, 0, len(cs))
	for _, c := range cs {
		out = append(out, consumerFrom(c))
	}
	return out, err
}

// ---- change requests ----

func (s *Service) maybeApprove(ctx context.Context, q *db.Queries, p auth.Principal, cr db.ChangeRequest) error {
	if changereq.State(cr.State) != changereq.Open {
		return nil
	}
	left, err := q.CountUndecidedReviews(ctx, cr.ID)
	if err != nil || left > 0 {
		return err
	}
	to, err := changereq.Next(changereq.Open, changereq.AllAcknowledged)
	if err != nil {
		return err
	}
	if err := q.SetChangeRequestState(ctx, db.SetChangeRequestStateParams{ID: cr.ID, State: string(to)}); err != nil {
		return err
	}
	s.count("approved")
	return audit(ctx, q, p, "change_request.approved", "change_request", cr.ID, map[string]any{"reason": "every consumer acknowledged"})
}

// GetChangeRequest returns a change request with its reviews.
func (s *Service) GetChangeRequest(ctx context.Context, id int64) (ChangeRequest, error) {
	r, err := s.q.GetChangeRequest(ctx, id)
	if err != nil {
		return ChangeRequest{}, notFound(err, "change request", id)
	}
	out := ChangeRequest{ID: r.ID, DatasetID: r.DatasetID, Dataset: r.Namespace + "/" + r.Name, OwnerTeam: deref(r.OwnerTeam),
		FromVersion: r.FromVersion, ToVersion: r.ToVersion, State: r.State, OpenedBy: r.OpenedBy, OpenedAt: r.OpenedAt,
		ClosedAt: r.ClosedAt, Reviews: []Review{}}
	if err := json.Unmarshal(r.Report, &out.Report); err != nil {
		return out, err
	}
	rs, err := s.q.ListReviews(ctx, id)
	if err != nil {
		return out, err
	}
	var pending int32
	for _, rv := range rs {
		out.Reviews = append(out.Reviews, reviewFrom(rv))
		if rv.Decision == nil {
			pending++
		}
	}
	out.PendingReviews = &pending
	return out, nil
}

// CRFilter narrows ListChangeRequests.
type CRFilter struct {
	DatasetID    *int64
	State        *string
	ConsumerTeam *string
}

// ListChangeRequests returns change requests, newest first.
func (s *Service) ListChangeRequests(ctx context.Context, f CRFilter) ([]ChangeRequest, error) {
	rows, err := s.q.ListChangeRequests(ctx, db.ListChangeRequestsParams{DatasetID: f.DatasetID, State: f.State, ConsumerTeam: f.ConsumerTeam})
	if err != nil {
		return nil, err
	}
	out := make([]ChangeRequest, 0, len(rows))
	for _, r := range rows {
		cr := ChangeRequest{ID: r.ID, DatasetID: r.DatasetID, Dataset: r.Namespace + "/" + r.Name, OwnerTeam: deref(r.OwnerTeam),
			FromVersion: r.FromVersion, ToVersion: r.ToVersion, State: r.State, OpenedBy: r.OpenedBy, OpenedAt: r.OpenedAt, ClosedAt: r.ClosedAt}
		pending := r.PendingReviews
		cr.PendingReviews = &pending
		if err := json.Unmarshal(r.Report, &cr.Report); err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, nil
}

// ReviewInput is a consumer's decision.
type ReviewInput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// Review records a consumer team's acknowledgement or rejection. Only that
// team can answer for itself; an admin cannot acknowledge on its behalf.
func (s *Service) Review(ctx context.Context, p auth.Principal, crID int64, in ReviewInput) (ChangeRequest, error) {
	if err := requireSignedIn(p); err != nil {
		return ChangeRequest{}, err
	}
	if in.Decision != "acknowledged" && in.Decision != "rejected" {
		return ChangeRequest{}, problem.BadRequest("decision must be acknowledged or rejected")
	}
	if in.Decision == "rejected" && strings.TrimSpace(in.Reason) == "" {
		return ChangeRequest{}, problem.BadRequest("a rejection needs a reason the owner can act on")
	}
	err := s.inTx(ctx, func(q *db.Queries) error {
		cr, err := q.LockChangeRequest(ctx, crID)
		if err != nil {
			return notFound(err, "change request", crID)
		}
		if changereq.State(cr.State) != changereq.Open {
			return problem.Conflict("change request #%d is %s; it is not waiting for reviews", crID, cr.State)
		}
		reviews, err := q.ListReviews(ctx, crID)
		if err != nil {
			return err
		}
		var mine *db.ChangeRequestReview
		for i := range reviews {
			if reviews[i].ConsumerTeam == p.Team {
				mine = &reviews[i]
			}
		}
		if mine == nil {
			return problem.Forbidden("team %s was not a consumer when this change request opened, so it has nothing to review", p.Team)
		}
		if mine.Decision != nil {
			return problem.Conflict("team %s already %s this change request", p.Team, *mine.Decision)
		}
		if _, err := q.DecideReview(ctx, db.DecideReviewParams{ChangeRequestID: crID, ConsumerTeam: p.Team,
			Decision: &in.Decision, Reason: in.Reason, DecidedBy: ptr(p.Actor())}); err != nil {
			return err
		}
		if err := audit(ctx, q, p, "change_request."+in.Decision, "change_request", crID, map[string]any{
			"team": p.Team, "reason": in.Reason,
		}); err != nil {
			return err
		}
		s.count(in.Decision)
		if in.Decision == "rejected" {
			to, err := changereq.Next(changereq.Open, changereq.ConsumerRejected)
			if err != nil {
				return err
			}
			if err := q.SetChangeRequestState(ctx, db.SetChangeRequestStateParams{ID: crID, State: string(to)}); err != nil {
				return err
			}
			return q.SetSchemaVersionStatus(ctx, db.SetSchemaVersionStatusParams{ID: cr.SchemaVersionID, Status: "rejected"})
		}
		return s.maybeApprove(ctx, q, p, cr)
	})
	if err != nil {
		return ChangeRequest{}, err
	}
	return s.GetChangeRequest(ctx, crID)
}

// Publish makes an approved change request's schema the current contract.
func (s *Service) Publish(ctx context.Context, p auth.Principal, crID int64) (ChangeRequest, error) {
	return s.close(ctx, p, crID, changereq.Publish, "published")
}

// Withdraw abandons a change request.
func (s *Service) Withdraw(ctx context.Context, p auth.Principal, crID int64) (ChangeRequest, error) {
	return s.close(ctx, p, crID, changereq.Withdraw, "withdrawn")
}

func (s *Service) close(ctx context.Context, p auth.Principal, crID int64, e changereq.Event, schemaStatus string) (ChangeRequest, error) {
	err := s.inTx(ctx, func(q *db.Queries) error {
		cr, err := q.LockChangeRequest(ctx, crID)
		if err != nil {
			return notFound(err, "change request", crID)
		}
		d, err := q.GetDatasetForUpdate(ctx, cr.DatasetID)
		if err != nil {
			return err
		}
		if err := requireOwner(p, d); err != nil {
			return err
		}
		to, err := changereq.Next(changereq.State(cr.State), e)
		if err != nil {
			if e == changereq.Publish && changereq.State(cr.State) == changereq.Open {
				left, _ := q.CountUndecidedReviews(ctx, crID)
				return problem.Conflict("change request #%d is waiting for %d consumer review(s)", crID, left)
			}
			return problem.Conflict("%v", err)
		}
		if err := q.SetChangeRequestState(ctx, db.SetChangeRequestStateParams{ID: crID, State: string(to)}); err != nil {
			return err
		}
		if err := q.SetSchemaVersionStatus(ctx, db.SetSchemaVersionStatusParams{ID: cr.SchemaVersionID, Status: schemaStatus}); err != nil {
			return err
		}
		s.count(string(to))
		return audit(ctx, q, p, "change_request."+string(to), "change_request", crID, map[string]any{"dataset_id": cr.DatasetID})
	})
	if err != nil {
		return ChangeRequest{}, err
	}
	return s.GetChangeRequest(ctx, crID)
}

// ---- audit and SLO history ----

// ListAudit returns audit entries, newest first.
func (s *Service) ListAudit(ctx context.Context, entityType *string, entityID *int64, limit int32) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.q.ListAudit(ctx, db.ListAuditParams{EntityType: entityType, EntityID: entityID, RowLimit: limit})
	out := make([]AuditEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, AuditEntry{ID: r.ID, At: r.At, Actor: r.Actor, Action: r.Action, EntityType: r.EntityType, EntityID: r.EntityID, Details: r.Details})
	}
	return out, err
}

// SLOHistory returns freshness transitions for a dataset.
func (s *Service) SLOHistory(ctx context.Context, datasetID int64, limit int32) ([]SLOTransition, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	ts, err := s.q.ListSLOTransitions(ctx, db.ListSLOTransitionsParams{DatasetID: datasetID, RowLimit: limit})
	out := make([]SLOTransition, 0, len(ts))
	for _, t := range ts {
		out = append(out, SLOTransition{From: t.FromStatus, To: t.ToStatus, StalenessMs: t.StalenessMs, DeadlineAt: t.DeadlineAt, DetectedAt: t.DetectedAt})
	}
	return out, err
}

// Ping checks the database.
func (s *Service) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.pool.Ping(ctx)
}

func ptr[T any](v T) *T { return &v }
