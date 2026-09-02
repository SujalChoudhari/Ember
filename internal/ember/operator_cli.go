package ember

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

var (
	ErrInvalidCLIRequest = errors.New("invalid CLI request")
	ErrInvalidCLITags    = errors.New("invalid CLI tags")
)

func RunCLI(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	if operator == nil || output == nil || len(args) == 0 {
		return ErrInvalidCLIRequest
	}
	switch args[0] {
	case "resource":
		if len(args) < 2 {
			return ErrInvalidCLIRequest
		}
		switch args[1] {
		case "create":
			return runCLICreateResource(ctx, operator, args[2:], output)
		case "get":
			return runCLIGetResource(ctx, operator, args[2:], output)
		case "update-tags":
			return runCLIUpdateResourceTags(ctx, operator, args[2:], output)
		default:
			return ErrInvalidCLIRequest
		}
	case "blob":
		if len(args) < 2 {
			return ErrInvalidCLIRequest
		}
		switch args[1] {
		case "put":
			return runCLIPutBlob(ctx, operator, args[2:], output)
		case "get":
			return runCLIGetBlob(ctx, operator, args[2:], output)
		default:
			return ErrInvalidCLIRequest
		}
	case "operation":
		if len(args) < 2 || args[1] != "get" {
			return ErrInvalidCLIRequest
		}
		return runCLIGetOperation(ctx, operator, args[2:], output)
	case "audit":
		if len(args) < 2 || args[1] != "list" {
			return ErrInvalidCLIRequest
		}
		return runCLIListAudit(ctx, operator, args[2:], output)
	case "reset":
		return runCLIReset(ctx, operator, args[1:], output)
	default:
		return ErrInvalidCLIRequest
	}
}

func newCLIFlagSet(name string) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	return set
}

func requireNoCLIArgs(set *flag.FlagSet) error {
	if set.NArg() != 0 {
		return ErrInvalidCLIRequest
	}
	return nil
}

func runCLICreateResource(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("resource create")
	scopeID := set.String("scope", "", "operator scope")
	resourceType := set.String("type", "", "resource type")
	name := set.String("name", "", "resource name")
	parentID := set.String("parent", "", "parent resource ID")
	tagsText := set.String("tags", "", "comma-separated key=value tags")
	providerNamespace := set.String("provider-namespace", "", "provider namespace")
	providerType := set.String("provider-type", "", "provider type")
	providerVersion := set.String("provider-version", "", "provider version")
	desiredState := set.String("desired-state", "", "desired state")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	tags, err := parseCLITags(*tagsText)
	if err != nil {
		return err
	}
	resource, err := operator.CreateResource(ctx, OperatorPrincipal{ScopeID: *scopeID}, models.ResourceSpec{
		Type: models.ResourceType(*resourceType), Name: *name, ParentID: *parentID, Tags: tags,
		Provider:     models.ProviderMetadata{Namespace: *providerNamespace, Type: *providerType, Version: *providerVersion},
		DesiredState: models.ResourceState(*desiredState),
	})
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{Resource: resource})
}

func runCLIGetResource(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("resource get")
	scopeID := set.String("scope", "", "operator scope")
	resourceID := set.String("id", "", "resource ID")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	resource, err := operator.GetResource(ctx, OperatorPrincipal{ScopeID: *scopeID}, *resourceID)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{Resource: resource})
}

func runCLIUpdateResourceTags(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("resource update-tags")
	scopeID := set.String("scope", "", "operator scope")
	resourceID := set.String("id", "", "resource ID")
	tagsText := set.String("tags", "", "comma-separated key=value tags")
	requestID := set.String("request-id", "", "idempotency request ID")
	correlationID := set.String("correlation-id", "", "correlation ID")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	tags, err := parseCLITags(*tagsText)
	if err != nil {
		return err
	}
	response, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{ScopeID: *scopeID}, *resourceID, tags, *requestID, *correlationID)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, response)
}

func runCLIPutBlob(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("blob put")
	scopeID := set.String("scope", "", "operator scope")
	bucketID := set.String("bucket", "", "bucket resource ID")
	objectKey := set.String("key", "", "object key")
	data := set.String("data", "", "object content")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	response, err := operator.PutBlob(ctx, OperatorPrincipal{ScopeID: *scopeID}, *bucketID, *objectKey, []byte(*data))
	if err != nil {
		return err
	}
	return writeCLIResponse(output, response)
}

func runCLIGetBlob(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("blob get")
	scopeID := set.String("scope", "", "operator scope")
	bucketID := set.String("bucket", "", "bucket resource ID")
	objectKey := set.String("key", "", "object key")
	startText := set.String("start", "", "inclusive range start")
	endText := set.String("end", "", "exclusive range end")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	principal := OperatorPrincipal{ScopeID: *scopeID}
	if (*startText == "") != (*endText == "") {
		return ErrInvalidCLIRequest
	}
	if *startText != "" {
		start, err := strconv.ParseInt(*startText, 10, 64)
		if err != nil {
			return ErrInvalidCLIRequest
		}
		end, err := strconv.ParseInt(*endText, 10, 64)
		if err != nil {
			return ErrInvalidCLIRequest
		}
		response, err := operator.ReadBlobRange(ctx, principal, *bucketID, *objectKey, start, end)
		if err != nil {
			return err
		}
		return writeCLIResponse(output, response)
	}
	response, err := operator.GetBlob(ctx, principal, *bucketID, *objectKey)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, response)
}

func runCLIGetOperation(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("operation get")
	scopeID := set.String("scope", "", "operator scope")
	operationID := set.String("id", "", "operation ID")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	operation, err := operator.GetOperation(ctx, OperatorPrincipal{ScopeID: *scopeID}, *operationID)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{Operation: operation})
}

func runCLIListAudit(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("audit list")
	scopeID := set.String("scope", "", "operator scope")
	resourceID := set.String("resource", "", "resource ID")
	limit := set.Int("limit", persistence.MaxAuditListLimit, "maximum entries")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	history, err := operator.ListAuditHistory(ctx, OperatorPrincipal{ScopeID: *scopeID}, *resourceID, *limit)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{Audit: history})
}

func runCLIReset(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("reset")
	scopeID := set.String("scope", "", "operator scope")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	if err := operator.Reset(ctx, OperatorPrincipal{ScopeID: *scopeID}); err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{})
}

func parseCLITags(value string) (map[string]string, error) {
	if value == "" {
		return nil, nil
	}
	tags := make(map[string]string)
	for _, pair := range strings.Split(value, ",") {
		key, tagValue, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return nil, ErrInvalidCLITags
		}
		tags[key] = tagValue
	}
	return tags, nil
}

func writeCLIResponse(output io.Writer, response *OperatorResponse) error {
	if response == nil {
		return fmt.Errorf("%w: nil response", ErrInvalidCLIRequest)
	}
	return json.NewEncoder(output).Encode(response)
}
