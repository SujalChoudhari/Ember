package ember

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/events"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const MaxObservabilityLogLimit = MaxWorkloadLogRecords

var (
	ErrInvalidObservabilityManager  = errors.New("invalid observability manager")
	ErrInvalidObservabilityLogLimit = errors.New("invalid observability log limit")
)

// RuntimeObservability exposes the bounded presentation contract for supported
// runtime components without retaining a second health, log, or metrics store.
type RuntimeObservability interface {
	InspectWorkload(context.Context, string, string, int) (*RuntimeObservabilityReport, error)
	SnapshotMetrics() events.MetricsSnapshot
}

// RuntimeObservabilityReport combines the current workload health/readiness,
// bounded redacted logs, and constant-cardinality event metrics.
type RuntimeObservabilityReport struct {
	Status  models.WorkloadStatus  `json:"status"`
	Logs    []models.WorkloadLog   `json:"logs"`
	Metrics events.MetricsSnapshot `json:"metrics"`
}

// ObservabilityManager delegates health and logs to the existing workload
// boundary and metrics to the existing process-local aggregate.
type ObservabilityManager struct {
	workloads WorkloadControlPlane
	metrics   *events.Metrics
}

func NewObservabilityManager(workloads WorkloadControlPlane, metrics *events.Metrics) (*ObservabilityManager, error) {
	if workloads == nil || metrics == nil {
		return nil, ErrInvalidObservabilityManager
	}
	return &ObservabilityManager{workloads: workloads, metrics: metrics}, nil
}

func (manager *ObservabilityManager) InspectWorkload(ctx context.Context, scopeID, resourceID string, logLimit int) (*RuntimeObservabilityReport, error) {
	if logLimit <= 0 || logLimit > MaxObservabilityLogLimit {
		return nil, ErrInvalidObservabilityLogLimit
	}
	view, err := manager.workloads.GetWorkload(ctx, scopeID, resourceID)
	if err != nil {
		return nil, err
	}
	logs, err := manager.workloads.GetWorkloadLogs(ctx, scopeID, resourceID, logLimit)
	if err != nil {
		return nil, err
	}
	if len(logs) > logLimit {
		logs = logs[:logLimit]
	}
	return &RuntimeObservabilityReport{
		Status:  view.Status,
		Logs:    append([]models.WorkloadLog(nil), logs...),
		Metrics: manager.metrics.Snapshot(),
	}, nil
}

func (manager *ObservabilityManager) SnapshotMetrics() events.MetricsSnapshot {
	if manager == nil || manager.metrics == nil {
		return events.MetricsSnapshot{}
	}
	return manager.metrics.Snapshot()
}

var _ RuntimeObservability = (*ObservabilityManager)(nil)
