package ember

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

var ErrInvalidNetworkStore = errors.New("invalid network store")

type NetworkControlPlane interface {
	CreateNetwork(ctx context.Context, scopeID, name string) (*models.Network, error)
	GetNetwork(ctx context.Context, scopeID, networkID string) (*models.Network, error)
	ListNetworks(ctx context.Context, scopeID string, limit int) ([]models.Network, error)
	DeleteNetwork(ctx context.Context, scopeID, networkID string) error
	AllocatePort(ctx context.Context, scopeID, networkID, workloadID string, number uint16, protocol models.NetworkProtocol) (*models.NetworkPort, error)
	GetPort(ctx context.Context, scopeID, portID string) (*models.NetworkPort, error)
	ListPorts(ctx context.Context, scopeID, networkID string, limit int) ([]models.NetworkPort, error)
	DeletePort(ctx context.Context, scopeID, portID string) error
	PublishEndpoint(ctx context.Context, scopeID, networkID, portID, name string) (*models.NetworkEndpoint, error)
	ResolveEndpoint(ctx context.Context, scopeID, endpointID string) (*models.NetworkEndpoint, error)
	ListEndpoints(ctx context.Context, scopeID, networkID string, limit int) ([]models.NetworkEndpoint, error)
	DeleteEndpoint(ctx context.Context, scopeID, endpointID string) error
}

// NetworkManager delegates logical networking to the approved bounded store
// boundary. It does not open sockets or change host networking.
type NetworkManager struct {
	store persistence.NetworkStore
}

func NewNetworkManager(store persistence.NetworkStore) (*NetworkManager, error) {
	if store == nil {
		return nil, ErrInvalidNetworkStore
	}
	return &NetworkManager{store: store}, nil
}

func (manager *NetworkManager) Reset(ctx context.Context) error {
	return manager.store.Reset(ctx)
}

func (manager *NetworkManager) CreateNetwork(ctx context.Context, scopeID, name string) (*models.Network, error) {
	return manager.store.CreateNetwork(ctx, scopeID, name)
}

func (manager *NetworkManager) GetNetwork(ctx context.Context, scopeID, networkID string) (*models.Network, error) {
	return manager.store.GetNetwork(ctx, scopeID, networkID)
}

func (manager *NetworkManager) ListNetworks(ctx context.Context, scopeID string, limit int) ([]models.Network, error) {
	return manager.store.ListNetworks(ctx, scopeID, limit)
}

func (manager *NetworkManager) DeleteNetwork(ctx context.Context, scopeID, networkID string) error {
	return manager.store.DeleteNetwork(ctx, scopeID, networkID)
}

func (manager *NetworkManager) AllocatePort(ctx context.Context, scopeID, networkID, workloadID string, number uint16, protocol models.NetworkProtocol) (*models.NetworkPort, error) {
	return manager.store.AllocatePort(ctx, scopeID, networkID, workloadID, number, protocol)
}

func (manager *NetworkManager) GetPort(ctx context.Context, scopeID, portID string) (*models.NetworkPort, error) {
	return manager.store.GetPort(ctx, scopeID, portID)
}

func (manager *NetworkManager) ListPorts(ctx context.Context, scopeID, networkID string, limit int) ([]models.NetworkPort, error) {
	return manager.store.ListPorts(ctx, scopeID, networkID, limit)
}

func (manager *NetworkManager) DeletePort(ctx context.Context, scopeID, portID string) error {
	return manager.store.DeletePort(ctx, scopeID, portID)
}

func (manager *NetworkManager) PublishEndpoint(ctx context.Context, scopeID, networkID, portID, name string) (*models.NetworkEndpoint, error) {
	return manager.store.PublishEndpoint(ctx, scopeID, networkID, portID, name)
}

func (manager *NetworkManager) ResolveEndpoint(ctx context.Context, scopeID, endpointID string) (*models.NetworkEndpoint, error) {
	return manager.store.ResolveEndpoint(ctx, scopeID, endpointID)
}

func (manager *NetworkManager) ListEndpoints(ctx context.Context, scopeID, networkID string, limit int) ([]models.NetworkEndpoint, error) {
	return manager.store.ListEndpoints(ctx, scopeID, networkID, limit)
}

func (manager *NetworkManager) DeleteEndpoint(ctx context.Context, scopeID, endpointID string) error {
	return manager.store.DeleteEndpoint(ctx, scopeID, endpointID)
}

var _ NetworkControlPlane = (*NetworkManager)(nil)
