package persistence

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const MaxNetworkListLimit = 100

var (
	ErrNetworkNotFound             = errors.New("network not found")
	ErrDuplicateNetwork            = errors.New("duplicate network")
	ErrNetworkPortConflict         = errors.New("network port conflict")
	ErrNetworkPortAlreadyPublished = errors.New("network port already published")
	ErrNetworkPortNotFound         = errors.New("network port not found")
	ErrNetworkEndpointNotFound     = errors.New("network endpoint not found")
	ErrDuplicateNetworkEndpoint    = errors.New("duplicate network endpoint")
	ErrNetworkHasDependents        = errors.New("network has dependents")
	ErrInvalidNetworkScope         = errors.New("invalid network scope")
	ErrInvalidNetworkListLimit     = errors.New("invalid network list limit")
	ErrInvalidNetworkStorePath     = errors.New("invalid network store path")
	ErrNetworkStoreCorrupt         = errors.New("corrupt network store")
	ErrNetworkStoreTooLarge        = errors.New("network store exceeds size limit")
	ErrNetworkStoreIO              = errors.New("network store I/O failure")
	ErrNetworkStoreIDExhausted     = errors.New("network store IDs exhausted")
)

type NetworkStore interface {
	Reset(ctx context.Context) error
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
