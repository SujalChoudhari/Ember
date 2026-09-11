package ember

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func TestNetworkAttachmentValidatesWorkloadAndCleansOnDelete(t *testing.T) {
	ctx := context.Background()
	operator, err := NewFileOperator(filepath.Join(t.TempDir(), "state"), 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	root, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource(root) error = %v", err)
	}
	workload, err := operator.CreateWorkload(ctx, OperatorPrincipal{ScopeID: root.ID}, models.ResourceSpec{
		Type:         models.ResourceTypeWorkload,
		Name:         "api",
		ParentID:     root.ID,
		Provider:     models.ProviderMetadata{Namespace: "Ember.Compute", Type: "workloads", Version: "v1"},
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}
	network, err := operator.CreateNetwork(ctx, OperatorPrincipal{ScopeID: root.ID}, "frontend")
	if err != nil {
		t.Fatalf("CreateNetwork() error = %v", err)
	}
	if _, err := operator.AllocateNetworkPort(ctx, OperatorPrincipal{ScopeID: root.ID}, network.ID, "missing-workload", 8080, models.NetworkProtocolTCP); !errors.Is(err, ErrWorkloadNotFound) {
		t.Fatalf("AllocateNetworkPort(missing workload) error = %v, want ErrWorkloadNotFound", err)
	}
	port, err := operator.AllocateNetworkPort(ctx, OperatorPrincipal{ScopeID: root.ID}, network.ID, workload.Resource.ID, 8080, models.NetworkProtocolTCP)
	if err != nil {
		t.Fatalf("AllocateNetworkPort() error = %v", err)
	}
	endpoint, err := operator.PublishNetworkEndpoint(ctx, OperatorPrincipal{ScopeID: root.ID}, network.ID, port.ID, "api")
	if err != nil {
		t.Fatalf("PublishNetworkEndpoint() error = %v", err)
	}

	if err := operator.DeleteWorkload(ctx, OperatorPrincipal{ScopeID: root.ID}, workload.Resource.ID); err != nil {
		t.Fatalf("DeleteWorkload() error = %v", err)
	}
	if _, err := operator.GetNetworkPort(ctx, OperatorPrincipal{ScopeID: root.ID}, port.ID); !errors.Is(err, persistence.ErrNetworkPortNotFound) {
		t.Fatalf("GetNetworkPort(after workload delete) error = %v, want ErrNetworkPortNotFound", err)
	}
	if _, err := operator.GetNetworkEndpoint(ctx, OperatorPrincipal{ScopeID: root.ID}, endpoint.ID); !errors.Is(err, persistence.ErrNetworkEndpointNotFound) {
		t.Fatalf("GetNetworkEndpoint(after workload delete) error = %v, want ErrNetworkEndpointNotFound", err)
	}
}
