package ember

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func TestNetworkManagerPublishesAndResolvesScopedEndpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "networks.json")
	store, err := persistence.NewFileNetworkStore(path)
	if err != nil {
		t.Fatalf("NewFileNetworkStore() error = %v", err)
	}
	manager, err := NewNetworkManager(store)
	if err != nil {
		t.Fatalf("NewNetworkManager() error = %v", err)
	}
	var plane NetworkControlPlane = manager
	ctx := context.Background()

	network, err := plane.CreateNetwork(ctx, "scope-a", "frontend")
	if err != nil {
		t.Fatalf("CreateNetwork() error = %v", err)
	}
	port, err := plane.AllocatePort(ctx, "scope-a", network.ID, "workload-a", 8080, models.NetworkProtocolTCP)
	if err != nil {
		t.Fatalf("AllocatePort() error = %v", err)
	}
	if _, err := plane.AllocatePort(ctx, "scope-a", network.ID, "workload-b", 8080, models.NetworkProtocolTCP); !errors.Is(err, persistence.ErrNetworkPortConflict) {
		t.Fatalf("conflicting AllocatePort() error = %v, want ErrNetworkPortConflict", err)
	}
	if _, err := plane.AllocatePort(ctx, "scope-b", network.ID, "workload-b", 8080, models.NetworkProtocolTCP); !errors.Is(err, persistence.ErrNetworkNotFound) {
		t.Fatalf("cross-scope AllocatePort() error = %v, want ErrNetworkNotFound", err)
	}

	endpoint, err := plane.PublishEndpoint(ctx, "scope-a", network.ID, port.ID, "api")
	if err != nil {
		t.Fatalf("PublishEndpoint() error = %v", err)
	}
	resolved, err := plane.ResolveEndpoint(ctx, "scope-a", endpoint.ID)
	if err != nil {
		t.Fatalf("ResolveEndpoint(same scope) error = %v", err)
	}
	if !reflect.DeepEqual(resolved, endpoint) || resolved.Address == "" {
		t.Fatalf("ResolveEndpoint() = %#v, want %#v with deterministic address", resolved, endpoint)
	}
	if _, err := plane.ResolveEndpoint(ctx, "scope-b", endpoint.ID); !errors.Is(err, persistence.ErrNetworkEndpointNotFound) {
		t.Fatalf("ResolveEndpoint(cross scope) error = %v, want ErrNetworkEndpointNotFound", err)
	}
}

func TestNetworkManagerPersistsBoundedRelationshipsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "networks.json")
	ctx := context.Background()
	store, err := persistence.NewFileNetworkStore(path)
	if err != nil {
		t.Fatalf("NewFileNetworkStore() error = %v", err)
	}
	network, err := store.CreateNetwork(ctx, "scope-a", "backend")
	if err != nil {
		t.Fatalf("CreateNetwork() error = %v", err)
	}
	port, err := store.AllocatePort(ctx, "scope-a", network.ID, "workload-a", 9090, models.NetworkProtocolUDP)
	if err != nil {
		t.Fatalf("AllocatePort() error = %v", err)
	}
	endpoint, err := store.PublishEndpoint(ctx, "scope-a", network.ID, port.ID, "metrics")
	if err != nil {
		t.Fatalf("PublishEndpoint() error = %v", err)
	}

	reopened, err := persistence.NewFileNetworkStore(path)
	if err != nil {
		t.Fatalf("NewFileNetworkStore(reopen) error = %v", err)
	}
	gotNetwork, err := reopened.GetNetwork(ctx, "scope-a", network.ID)
	if err != nil {
		t.Fatalf("GetNetwork(reopen) error = %v", err)
	}
	if !reflect.DeepEqual(gotNetwork, network) {
		t.Fatalf("reopened network = %#v, want %#v", gotNetwork, network)
	}
	gotEndpoint, err := reopened.ResolveEndpoint(ctx, "scope-a", endpoint.ID)
	if err != nil {
		t.Fatalf("ResolveEndpoint(reopen) error = %v", err)
	}
	if !reflect.DeepEqual(gotEndpoint, endpoint) {
		t.Fatalf("reopened endpoint = %#v, want %#v", gotEndpoint, endpoint)
	}

	if _, err := reopened.ListNetworks(ctx, "scope-a", 0); !errors.Is(err, persistence.ErrInvalidNetworkListLimit) {
		t.Fatalf("ListNetworks(zero limit) error = %v, want ErrInvalidNetworkListLimit", err)
	}
	if _, err := reopened.ListNetworks(ctx, "scope-b", persistence.MaxNetworkListLimit); !errors.Is(err, persistence.ErrNetworkNotFound) {
		t.Fatalf("ListNetworks(cross scope) error = %v, want ErrNetworkNotFound", err)
	}
}

func TestNetworkModelsRejectUnsafeAndUnboundedValues(t *testing.T) {
	valid := models.Network{ID: "network-1", ScopeID: "scope-a", Name: "frontend"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Network.Validate() error = %v", err)
	}
	invalidPort := models.NetworkPort{ID: "port-1", NetworkID: "network-1", ScopeID: "scope-a", WorkloadID: "workload-a", Number: 0, Protocol: models.NetworkProtocolTCP}
	if err := invalidPort.Validate(); !errors.Is(err, models.ErrInvalidNetworkPort) {
		t.Fatalf("invalid NetworkPort.Validate() error = %v, want ErrInvalidNetworkPort", err)
	}
	invalidEndpoint := models.NetworkEndpoint{ID: "endpoint-1", NetworkID: "network-1", ScopeID: "scope-a", PortID: "port-1", Name: "../unsafe", Address: "ember://network-1/unsafe"}
	if err := invalidEndpoint.Validate(); !errors.Is(err, models.ErrInvalidNetworkEndpoint) {
		t.Fatalf("invalid NetworkEndpoint.Validate() error = %v, want ErrInvalidNetworkEndpoint", err)
	}
}
