package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const (
	networkStoreVersion      = 1
	MaxNetworkStoreFileBytes = 1 << 20
)

type networkStoreDiskState struct {
	Version   int                      `json:"version"`
	NextID    uint64                   `json:"next_id"`
	Networks  []models.Network         `json:"networks"`
	Ports     []models.NetworkPort     `json:"ports"`
	Endpoints []models.NetworkEndpoint `json:"endpoints"`
}

// FileNetworkStore persists bounded logical network state in one private JSON snapshot.
// It models connectivity only; it never opens sockets or performs live network changes.
type FileNetworkStore struct {
	mu        sync.RWMutex
	path      string
	networks  map[string]models.Network
	ports     map[string]models.NetworkPort
	endpoints map[string]models.NetworkEndpoint
	nextID    uint64
}

func NewFileNetworkStore(path string) (*FileNetworkStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidNetworkStorePath
	}
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, ErrInvalidNetworkStorePath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrNetworkStoreIO
	}

	store := &FileNetworkStore{
		path:      path,
		networks:  make(map[string]models.Network),
		ports:     make(map[string]models.NetworkPort),
		endpoints: make(map[string]models.NetworkEndpoint),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *FileNetworkStore) load() error {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrNetworkStoreIO
	}
	if len(data) > MaxNetworkStoreFileBytes {
		return ErrNetworkStoreTooLarge
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state networkStoreDiskState
	if err := decoder.Decode(&state); err != nil {
		return ErrNetworkStoreCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrNetworkStoreCorrupt
	}
	if state.Version != networkStoreVersion {
		return ErrNetworkStoreCorrupt
	}

	networks := make(map[string]models.Network, len(state.Networks))
	for _, network := range state.Networks {
		if err := network.Validate(); err != nil {
			return ErrNetworkStoreCorrupt
		}
		if _, exists := networks[network.ID]; exists {
			return ErrNetworkStoreCorrupt
		}
		networks[network.ID] = network
	}
	ports := make(map[string]models.NetworkPort, len(state.Ports))
	for _, port := range state.Ports {
		if err := port.Validate(); err != nil {
			return ErrNetworkStoreCorrupt
		}
		if _, exists := ports[port.ID]; exists {
			return ErrNetworkStoreCorrupt
		}
		network, exists := networks[port.NetworkID]
		if !exists || network.ScopeID != port.ScopeID {
			return ErrNetworkStoreCorrupt
		}
		for _, other := range ports {
			if other.NetworkID == port.NetworkID && other.Number == port.Number && other.Protocol == port.Protocol {
				return ErrNetworkStoreCorrupt
			}
		}
		ports[port.ID] = port
	}
	endpoints := make(map[string]models.NetworkEndpoint, len(state.Endpoints))
	for _, endpoint := range state.Endpoints {
		if err := endpoint.Validate(); err != nil {
			return ErrNetworkStoreCorrupt
		}
		if _, exists := endpoints[endpoint.ID]; exists {
			return ErrNetworkStoreCorrupt
		}
		network, networkExists := networks[endpoint.NetworkID]
		port, portExists := ports[endpoint.PortID]
		if !networkExists || !portExists || network.ScopeID != endpoint.ScopeID || port.ScopeID != endpoint.ScopeID || port.NetworkID != endpoint.NetworkID {
			return ErrNetworkStoreCorrupt
		}
		for _, other := range endpoints {
			if other.NetworkID == endpoint.NetworkID && (other.Name == endpoint.Name || other.PortID == endpoint.PortID) {
				return ErrNetworkStoreCorrupt
			}
		}
		endpoints[endpoint.ID] = endpoint
	}

	store.networks = networks
	store.ports = ports
	store.endpoints = endpoints
	store.nextID = state.NextID
	return nil
}

func (store *FileNetworkStore) saveLocked() error {
	networks := make([]models.Network, 0, len(store.networks))
	for _, network := range store.networks {
		networks = append(networks, network)
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].ID < networks[j].ID })
	ports := make([]models.NetworkPort, 0, len(store.ports))
	for _, port := range store.ports {
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].ID < ports[j].ID })
	endpoints := make([]models.NetworkEndpoint, 0, len(store.endpoints))
	for _, endpoint := range store.endpoints {
		endpoints = append(endpoints, endpoint)
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].ID < endpoints[j].ID })

	data, err := json.MarshalIndent(networkStoreDiskState{
		Version: networkStoreVersion, NextID: store.nextID, Networks: networks, Ports: ports, Endpoints: endpoints,
	}, "", "  ")
	if err != nil {
		return ErrNetworkStoreIO
	}
	data = append(data, '\n')
	if len(data) > MaxNetworkStoreFileBytes {
		return ErrNetworkStoreTooLarge
	}

	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrNetworkStoreIO
	}
	temporary, err := os.CreateTemp(directory, ".ember-networks-*.tmp")
	if err != nil {
		return ErrNetworkStoreIO
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return ErrNetworkStoreIO
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		_ = temporary.Close()
		return ErrNetworkStoreIO
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrNetworkStoreIO
	}
	if err := temporary.Close(); err != nil {
		return ErrNetworkStoreIO
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return ErrNetworkStoreIO
	}
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

func (store *FileNetworkStore) nextIDLocked(prefix string) (string, error) {
	if store.nextID == ^uint64(0) {
		return "", ErrNetworkStoreIDExhausted
	}
	store.nextID++
	return fmt.Sprintf("%s-%08d", prefix, store.nextID), nil
}

func validateNetworkScope(scopeID string) error {
	if strings.TrimSpace(scopeID) == "" {
		return ErrInvalidNetworkScope
	}
	return nil
}

func validNetworkName(value string, maxLength int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxLength && !strings.ContainsAny(value, "/\\\x00")
}

func (store *FileNetworkStore) CreateNetwork(ctx context.Context, scopeID, name string) (*models.Network, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return nil, err
	}
	candidate := models.Network{ScopeID: scopeID, Name: name}
	if !validNetworkName(name, models.MaxNetworkNameLength) {
		return nil, models.ErrInvalidNetwork
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	for _, network := range store.networks {
		if network.ScopeID == scopeID && network.Name == name {
			return nil, ErrDuplicateNetwork
		}
	}
	id, err := store.nextIDLocked("network")
	if err != nil {
		return nil, err
	}
	candidate.ID = id
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	store.networks[id] = candidate
	if err := store.saveLocked(); err != nil {
		delete(store.networks, id)
		return nil, err
	}
	copy := candidate
	return &copy, nil
}

func (store *FileNetworkStore) GetNetwork(ctx context.Context, scopeID, networkID string) (*models.Network, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	network, exists := store.networks[networkID]
	if !exists || network.ScopeID != scopeID {
		return nil, ErrNetworkNotFound
	}
	copy := network
	return &copy, nil
}

func (store *FileNetworkStore) ListNetworks(ctx context.Context, scopeID string, limit int) ([]models.Network, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxNetworkListLimit {
		return nil, ErrInvalidNetworkListLimit
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	networks := make([]models.Network, 0, limit)
	for _, network := range store.networks {
		if network.ScopeID == scopeID {
			networks = append(networks, network)
		}
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].ID < networks[j].ID })
	if len(networks) > limit {
		networks = networks[:limit]
	}
	if len(networks) == 0 {
		return nil, ErrNetworkNotFound
	}
	return networks, nil
}

func (store *FileNetworkStore) DeleteNetwork(ctx context.Context, scopeID, networkID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	network, exists := store.networks[networkID]
	if !exists || network.ScopeID != scopeID {
		return nil
	}
	for _, port := range store.ports {
		if port.NetworkID == networkID {
			return ErrNetworkHasDependents
		}
	}
	for _, endpoint := range store.endpoints {
		if endpoint.NetworkID == networkID {
			return ErrNetworkHasDependents
		}
	}
	delete(store.networks, networkID)
	if err := store.saveLocked(); err != nil {
		store.networks[networkID] = network
		return err
	}
	return nil
}

func (store *FileNetworkStore) AllocatePort(ctx context.Context, scopeID, networkID, workloadID string, number uint16, protocol models.NetworkProtocol) (*models.NetworkPort, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return nil, err
	}
	candidate := models.NetworkPort{ID: "pending", NetworkID: networkID, ScopeID: scopeID, WorkloadID: workloadID, Number: number, Protocol: protocol}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	network, exists := store.networks[networkID]
	if !exists || network.ScopeID != scopeID {
		return nil, ErrNetworkNotFound
	}
	for _, port := range store.ports {
		if port.NetworkID == networkID && port.Number == number && port.Protocol == protocol {
			return nil, ErrNetworkPortConflict
		}
	}
	id, err := store.nextIDLocked("port")
	if err != nil {
		return nil, err
	}
	candidate.ID = id
	store.ports[id] = candidate
	if err := store.saveLocked(); err != nil {
		delete(store.ports, id)
		return nil, err
	}
	copy := candidate
	return &copy, nil
}

func (store *FileNetworkStore) GetPort(ctx context.Context, scopeID, portID string) (*models.NetworkPort, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	port, exists := store.ports[portID]
	if !exists || port.ScopeID != scopeID {
		return nil, ErrNetworkPortNotFound
	}
	copy := port
	return &copy, nil
}

func (store *FileNetworkStore) ListPorts(ctx context.Context, scopeID, networkID string, limit int) ([]models.NetworkPort, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxNetworkListLimit {
		return nil, ErrInvalidNetworkListLimit
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	network, exists := store.networks[networkID]
	if !exists || network.ScopeID != scopeID {
		return nil, ErrNetworkNotFound
	}
	ports := make([]models.NetworkPort, 0, limit)
	for _, port := range store.ports {
		if port.NetworkID == networkID && port.ScopeID == scopeID {
			ports = append(ports, port)
		}
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].ID < ports[j].ID })
	if len(ports) > limit {
		ports = ports[:limit]
	}
	return ports, nil
}

func (store *FileNetworkStore) DeletePort(ctx context.Context, scopeID, portID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	port, exists := store.ports[portID]
	if !exists || port.ScopeID != scopeID {
		return nil
	}
	for _, endpoint := range store.endpoints {
		if endpoint.PortID == portID {
			return ErrNetworkPortAlreadyPublished
		}
	}
	delete(store.ports, portID)
	if err := store.saveLocked(); err != nil {
		store.ports[portID] = port
		return err
	}
	return nil
}

func (store *FileNetworkStore) DeletePortsForWorkload(ctx context.Context, scopeID, workloadID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return err
	}
	if strings.TrimSpace(workloadID) == "" || len(workloadID) > models.MaxNetworkWorkloadIDLen {
		return ErrInvalidNetworkWorkloadID
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	removedPorts := make(map[string]models.NetworkPort)
	for portID, port := range store.ports {
		if port.ScopeID == scopeID && port.WorkloadID == workloadID {
			removedPorts[portID] = port
		}
	}
	if len(removedPorts) == 0 {
		return nil
	}
	removedEndpoints := make(map[string]models.NetworkEndpoint)
	for endpointID, endpoint := range store.endpoints {
		if _, attached := removedPorts[endpoint.PortID]; attached {
			removedEndpoints[endpointID] = endpoint
		}
	}
	for portID := range removedPorts {
		delete(store.ports, portID)
	}
	for endpointID := range removedEndpoints {
		delete(store.endpoints, endpointID)
	}
	if err := store.saveLocked(); err != nil {
		for portID, port := range removedPorts {
			store.ports[portID] = port
		}
		for endpointID, endpoint := range removedEndpoints {
			store.endpoints[endpointID] = endpoint
		}
		return err
	}
	return nil
}

func (store *FileNetworkStore) PublishEndpoint(ctx context.Context, scopeID, networkID, portID, name string) (*models.NetworkEndpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	network, networkExists := store.networks[networkID]
	port, portExists := store.ports[portID]
	if !networkExists || !portExists || network.ScopeID != scopeID || port.ScopeID != scopeID || port.NetworkID != networkID {
		return nil, ErrNetworkNotFound
	}
	candidate := models.NetworkEndpoint{ID: "pending", NetworkID: networkID, ScopeID: scopeID, PortID: portID, Name: name, Address: fmt.Sprintf("ember://%s/%s", networkID, name)}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	for _, endpoint := range store.endpoints {
		if endpoint.NetworkID == networkID && endpoint.Name == name {
			return nil, ErrDuplicateNetworkEndpoint
		}
		if endpoint.PortID == portID {
			return nil, ErrNetworkPortAlreadyPublished
		}
	}
	id, err := store.nextIDLocked("endpoint")
	if err != nil {
		return nil, err
	}
	candidate.ID = id
	store.endpoints[id] = candidate
	if err := store.saveLocked(); err != nil {
		delete(store.endpoints, id)
		return nil, err
	}
	copy := candidate
	return &copy, nil
}

func (store *FileNetworkStore) ResolveEndpoint(ctx context.Context, scopeID, endpointID string) (*models.NetworkEndpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	endpoint, exists := store.endpoints[endpointID]
	if !exists || endpoint.ScopeID != scopeID {
		return nil, ErrNetworkEndpointNotFound
	}
	copy := endpoint
	return &copy, nil
}

func (store *FileNetworkStore) ListEndpoints(ctx context.Context, scopeID, networkID string, limit int) ([]models.NetworkEndpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxNetworkListLimit {
		return nil, ErrInvalidNetworkListLimit
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	network, exists := store.networks[networkID]
	if !exists || network.ScopeID != scopeID {
		return nil, ErrNetworkNotFound
	}
	endpoints := make([]models.NetworkEndpoint, 0, limit)
	for _, endpoint := range store.endpoints {
		if endpoint.NetworkID == networkID && endpoint.ScopeID == scopeID {
			endpoints = append(endpoints, endpoint)
		}
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].ID < endpoints[j].ID })
	if len(endpoints) > limit {
		endpoints = endpoints[:limit]
	}
	return endpoints, nil
}

func (store *FileNetworkStore) DeleteEndpoint(ctx context.Context, scopeID, endpointID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNetworkScope(scopeID); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	endpoint, exists := store.endpoints[endpointID]
	if !exists || endpoint.ScopeID != scopeID {
		return nil
	}
	delete(store.endpoints, endpointID)
	if err := store.saveLocked(); err != nil {
		store.endpoints[endpointID] = endpoint
		return err
	}
	return nil
}

func (store *FileNetworkStore) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.networks = make(map[string]models.Network)
	store.ports = make(map[string]models.NetworkPort)
	store.endpoints = make(map[string]models.NetworkEndpoint)
	store.nextID = 0
	if err := os.Remove(store.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrNetworkStoreIO
	}
	return nil
}

var _ NetworkStore = (*FileNetworkStore)(nil)
