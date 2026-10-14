package registry

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/technobasant/dataset-registry/api/internal/compat"
	"github.com/technobasant/dataset-registry/api/internal/db"
)

// Owner is the team accountable for a dataset and the person to call.
type Owner struct {
	Team   string `json:"team"`
	Person string `json:"person"`
}

// Dataset is the registry's view of one dataset.
type Dataset struct {
	ID                  int64      `json:"id"`
	Namespace           string     `json:"namespace"`
	Name                string     `json:"name"`
	Description         string     `json:"description"`
	Registered          bool       `json:"registered"`
	Owner               *Owner     `json:"owner"`
	Tier                string     `json:"tier"`
	FreshnessSLOSeconds *int32     `json:"freshness_slo_seconds"`
	Compatibility       string     `json:"compatibility"`
	LastWriteAt         *time.Time `json:"last_write_at"`
	LastWriteRun        *uuid.UUID `json:"last_write_run"`
	SLOStatus           string     `json:"slo_status"`
	SLOStatusSince      *time.Time `json:"slo_status_since"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`

	ConsumerCount     *int32 `json:"consumer_count,omitempty"`
	CurrentVersion    *int32 `json:"current_version,omitempty"`
	OpenChangeRequest *int64 `json:"open_change_request_id,omitempty"`
}

func datasetFrom(d db.Dataset) Dataset {
	out := Dataset{
		ID: d.ID, Namespace: d.Namespace, Name: d.Name, Description: d.Description,
		Registered: d.Registered, Tier: d.Tier, FreshnessSLOSeconds: d.FreshnessSloSeconds,
		Compatibility: d.Compatibility, LastWriteAt: d.LastWriteAt, LastWriteRun: d.LastWriteRun,
		SLOStatus: d.SloStatus, SLOStatusSince: d.SloStatusSince, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if d.OwnerTeam != nil {
		out.Owner = &Owner{Team: *d.OwnerTeam, Person: deref(d.OwnerPerson)}
	}
	return out
}

func datasetFromList(r db.ListDatasetsRow) Dataset {
	out := datasetFrom(db.Dataset{
		ID: r.ID, Namespace: r.Namespace, Name: r.Name, Description: r.Description, Registered: r.Registered,
		OwnerTeam: r.OwnerTeam, OwnerPerson: r.OwnerPerson, Tier: r.Tier, FreshnessSloSeconds: r.FreshnessSloSeconds,
		Compatibility: r.Compatibility, LastWriteAt: r.LastWriteAt, LastWriteRun: r.LastWriteRun,
		SloStatus: r.SloStatus, SloStatusSince: r.SloStatusSince, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	})
	out.ConsumerCount = &r.ConsumerCount
	if r.CurrentVersion > 0 {
		out.CurrentVersion = &r.CurrentVersion
	}
	if r.OpenChangeRequest > 0 {
		out.OpenChangeRequest = &r.OpenChangeRequest
	}
	return out
}

// SchemaVersion is one version of a dataset's contract.
type SchemaVersion struct {
	Version   int32           `json:"version"`
	Status    string          `json:"status"`
	Schema    json.RawMessage `json:"schema"`
	CreatedBy string          `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
}

func schemaFrom(s db.SchemaVersion) SchemaVersion {
	return SchemaVersion{Version: s.Version, Status: s.Status, Schema: s.Schema, CreatedBy: s.CreatedBy, CreatedAt: s.CreatedAt}
}

// Consumer is a team that depends on a dataset.
type Consumer struct {
	Team      string    `json:"team"`
	Contact   string    `json:"contact"`
	Purpose   string    `json:"purpose"`
	CreatedAt time.Time `json:"created_at"`
}

func consumerFrom(c db.Consumer) Consumer {
	return Consumer{Team: c.Team, Contact: c.Contact, Purpose: c.Purpose, CreatedAt: c.CreatedAt}
}

// Review is one consumer's answer to a change request.
type Review struct {
	ConsumerTeam string     `json:"consumer_team"`
	Decision     *string    `json:"decision"`
	Reason       string     `json:"reason"`
	DecidedBy    *string    `json:"decided_by"`
	DecidedAt    *time.Time `json:"decided_at"`
}

func reviewFrom(r db.ChangeRequestReview) Review {
	return Review{ConsumerTeam: r.ConsumerTeam, Decision: r.Decision, Reason: r.Reason, DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt}
}

// ChangeRequest is a breaking schema change waiting on its consumers.
type ChangeRequest struct {
	ID             int64         `json:"id"`
	DatasetID      int64         `json:"dataset_id"`
	Dataset        string        `json:"dataset"`
	OwnerTeam      string        `json:"owner_team"`
	FromVersion    int32         `json:"from_version"`
	ToVersion      int32         `json:"to_version"`
	State          string        `json:"state"`
	Report         compat.Report `json:"report"`
	OpenedBy       string        `json:"opened_by"`
	OpenedAt       time.Time     `json:"opened_at"`
	ClosedAt       *time.Time    `json:"closed_at"`
	PendingReviews *int32        `json:"pending_reviews,omitempty"`
	Reviews        []Review      `json:"reviews,omitempty"`
}

// AuditEntry is one row of the append-only audit log.
type AuditEntry struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	EntityType string          `json:"entity_type"`
	EntityID   int64           `json:"entity_id"`
	Details    json.RawMessage `json:"details"`
}

// SLOTransition is a change of freshness status.
type SLOTransition struct {
	From        string     `json:"from"`
	To          string     `json:"to"`
	StalenessMs int64      `json:"staleness_ms"`
	DeadlineAt  *time.Time `json:"deadline_at"`
	DetectedAt  time.Time  `json:"detected_at"`
}

// DatasetDetail is everything the dataset page shows.
type DatasetDetail struct {
	Dataset
	Consumers         []Consumer      `json:"consumers"`
	Versions          []SchemaVersion `json:"versions"`
	OpenChangeRequest *ChangeRequest  `json:"open_change_request"`
	SLOHistory        []SLOTransition `json:"slo_history"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
