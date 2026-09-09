package models

import (
	"errors"
	"strings"
)

type NetworkProtocol string

const (
	NetworkProtocolTCP NetworkProtocol = "tcp"
	NetworkProtocolUDP NetworkProtocol = "udp"
)

const (
	MaxNetworkIDLength       = 128
	MaxNetworkScopeIDLength  = 128
	MaxNetworkNameLength     = 128
	MaxNetworkWorkloadIDLen  = 128
	MaxNetworkEndpointLength = 128
	MaxNetworkAddressLength  = 256
)

type Network struct {
	ID      string `json:"id"`
	ScopeID string `json:"scope_id"`
	Name    string `json:"name"`
}

type NetworkPort struct {
	ID         string          `json:"id"`
	NetworkID  string          `json:"network_id"`
	ScopeID    string          `json:"scope_id"`
	WorkloadID string          `json:"workload_id"`
	Number     uint16          `json:"number"`
	Protocol   NetworkProtocol `json:"protocol"`
}

type NetworkEndpoint struct {
	ID        string `json:"id"`
	NetworkID string `json:"network_id"`
	ScopeID   string `json:"scope_id"`
	PortID    string `json:"port_id"`
	Name      string `json:"name"`
	Address   string `json:"address"`
}

var (
	ErrInvalidNetwork         = errors.New("invalid network")
	ErrInvalidNetworkPort     = errors.New("invalid network port")
	ErrInvalidNetworkEndpoint = errors.New("invalid network endpoint")
)

func validNetworkText(value string, maxLength int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxLength && !strings.ContainsAny(value, "/\\\x00")
}

func validNetworkAddress(value string, maxLength int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxLength && !strings.ContainsRune(value, 0)
}

func (network Network) Validate() error {
	if !validNetworkText(network.ID, MaxNetworkIDLength) ||
		!validNetworkText(network.ScopeID, MaxNetworkScopeIDLength) ||
		!validNetworkText(network.Name, MaxNetworkNameLength) {
		return ErrInvalidNetwork
	}
	return nil
}

func (port NetworkPort) Validate() error {
	if !validNetworkText(port.ID, MaxNetworkIDLength) ||
		!validNetworkText(port.NetworkID, MaxNetworkIDLength) ||
		!validNetworkText(port.ScopeID, MaxNetworkScopeIDLength) ||
		!validNetworkText(port.WorkloadID, MaxNetworkWorkloadIDLen) ||
		port.Number == 0 ||
		(port.Protocol != NetworkProtocolTCP && port.Protocol != NetworkProtocolUDP) {
		return ErrInvalidNetworkPort
	}
	return nil
}

func (endpoint NetworkEndpoint) Validate() error {
	if !validNetworkText(endpoint.ID, MaxNetworkIDLength) ||
		!validNetworkText(endpoint.NetworkID, MaxNetworkIDLength) ||
		!validNetworkText(endpoint.ScopeID, MaxNetworkScopeIDLength) ||
		!validNetworkText(endpoint.PortID, MaxNetworkIDLength) ||
		!validNetworkText(endpoint.Name, MaxNetworkEndpointLength) ||
		!validNetworkAddress(endpoint.Address, MaxNetworkAddressLength) {
		return ErrInvalidNetworkEndpoint
	}
	return nil
}
