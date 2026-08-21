package models

import (
	"errors"
	"strings"
)

type ResourceType string

const (
	ResourceTypeGroup  ResourceType = "group"
	ResourceTypeBucket ResourceType = "bucket"
)

func (resourceType ResourceType) valid() bool {
	switch resourceType {
	case ResourceTypeGroup, ResourceTypeBucket:
		return true
	default:
		return false
	}
}

type ResourceSpec struct {
	Type     ResourceType
	Name     string
	ParentID string
}

type Resource struct {
	ID   string
	Spec ResourceSpec
}

var (
	ErrInvalidResourceSpec = errors.New("invalid resource spec")
	ErrInvalidResource     = errors.New("invalid resource")
)

func (spec ResourceSpec) Validate() error {
	if !spec.Type.valid() || strings.TrimSpace(spec.Name) == "" {
		return ErrInvalidResourceSpec
	}
	if spec.ParentID != "" && strings.TrimSpace(spec.ParentID) == "" {
		return ErrInvalidResourceSpec
	}
	return nil
}

func (resource Resource) Validate() error {
	if strings.TrimSpace(resource.ID) == "" || resource.Spec.Validate() != nil {
		return ErrInvalidResource
	}
	return nil
}
