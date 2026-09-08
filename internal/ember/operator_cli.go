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

	"github.com/SujalChoudhari/Ember/internal/ember/deployment"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
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
		case "list":
			return runCLIListResources(ctx, operator, args[2:], output)
		case "update-tags":
			return runCLIUpdateResourceTags(ctx, operator, args[2:], output)
		case "delete":
			return runCLIDeleteResource(ctx, operator, args[2:], output)
		case "lock":
			if len(args) < 3 {
				return ErrInvalidCLIRequest
			}
			switch args[2] {
			case "acquire":
				return runCLIAcquireResourceLock(ctx, operator, args[3:], output)
			case "release":
				return runCLIReleaseResourceLock(ctx, operator, args[3:], output)
			case "inspect":
				return runCLIInspectResourceLock(ctx, operator, args[3:], output)
			default:
				return ErrInvalidCLIRequest
			}
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
		case "verify":
			return runCLIVerifyBlob(ctx, operator, args[2:], output)
		case "recover":
			return runCLIRecoverBlob(ctx, operator, args[2:], output)
		case "list":
			return runCLIListBlobs(ctx, operator, args[2:], output)
		case "delete":
			return runCLIDeleteBlob(ctx, operator, args[2:], output)
		default:
			return ErrInvalidCLIRequest
		}
	case "operation":
		if len(args) < 2 {
			return ErrInvalidCLIRequest
		}
		switch args[1] {
		case "get":
			return runCLIGetOperation(ctx, operator, args[2:], output)
		case "list":
			return runCLIListOperations(ctx, operator, args[2:], output)
		default:
			return ErrInvalidCLIRequest
		}
	case "audit":
		if len(args) < 2 || args[1] != "list" {
			return ErrInvalidCLIRequest
		}
		return runCLIListAudit(ctx, operator, args[2:], output)
	case "deployment":
		return runCLIDeployment(ctx, operator, args[1:], output)
	case "reset":
		return runCLIReset(ctx, operator, args[1:], output)
	default:
		return ErrInvalidCLIRequest
	}
}

func runCLIDeployment(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	if len(args) < 1 {
		return ErrInvalidCLIRequest
	}
	switch args[0] {
	case "plan":
		return runCLIDeploymentPlan(ctx, operator, args[1:], output)
	case "apply":
		return runCLIDeploymentApply(ctx, operator, args[1:], output)
	case "apply-progress":
		if len(args) < 2 {
			return ErrInvalidCLIRequest
		}
		switch args[1] {
		case "get":
			return runCLIGetApplyProgress(ctx, operator, args[2:], output)
		case "list":
			return runCLIListApplyProgress(ctx, operator, args[2:], output)
		case "operation":
			return runCLIGetApplyProgressByOperation(ctx, operator, args[2:], output)
		default:
			return ErrInvalidCLIRequest
		}
	case "recovery":
		switch args[1] {
		case "get":
			return runCLIGetRecovery(ctx, operator, args[2:], output)
		case "list":
			return runCLIListRecoveries(ctx, operator, args[2:], output)
		case "run":
			return runCLIRunRecovery(ctx, operator, args[2:], output)
		default:
			return ErrInvalidCLIRequest
		}
	default:
		return ErrInvalidCLIRequest
	}
}

func runCLIDeploymentPlan(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("deployment plan")
	scopeID := set.String("scope", "", "operator scope")
	inline := set.String("document", "", "inline JSON deployment document")
	path := set.String("file", "", "deployment document path")
	var parameterValues []string
	set.Func("parameter", "deployment parameter in name=value form", func(value string) error {
		parameterValues = append(parameterValues, value)
		return nil
	})
	if err := set.Parse(args); err != nil || requireNoCLIArgs(set) != nil {
		return ErrInvalidCLIRequest
	}
	data, err := readCLIDeploymentDocument(*inline, *path)
	if err != nil {
		return err
	}
	parameters, err := parseCLIParameters(parameterValues)
	if err != nil {
		return err
	}
	resolution, plan, err := operator.planDeployment(ctx, OperatorPrincipal{ScopeID: *scopeID}, data, parameters)
	if err != nil {
		return err
	}
	resolved := resolution.Document()
	return writeCLIResponse(output, &OperatorResponse{Plan: &plan, Resolution: &resolved})
}

func runCLIDeploymentApply(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("deployment apply")
	scopeID := set.String("scope", "", "operator scope")
	inline := set.String("document", "", "inline JSON deployment document")
	path := set.String("file", "", "deployment document path")
	requestID := set.String("request-id", "", "idempotency request ID")
	correlationID := set.String("correlation-id", "", "correlation ID")
	confirm := set.Bool("confirm", false, "confirm destructive changes")
	var parameterValues []string
	set.Func("parameter", "deployment parameter in name=value form", func(value string) error {
		parameterValues = append(parameterValues, value)
		return nil
	})
	if err := set.Parse(args); err != nil || requireNoCLIArgs(set) != nil {
		return ErrInvalidCLIRequest
	}
	data, err := readCLIDeploymentDocument(*inline, *path)
	if err != nil {
		return err
	}
	parameters, err := parseCLIParameters(parameterValues)
	if err != nil {
		return err
	}
	result, resolution, err := operator.applyDeployment(ctx, OperatorPrincipal{ScopeID: *scopeID}, data, parameters, deployment.ApplyOptions{
		RequestID: *requestID, CorrelationID: *correlationID, ApproveDestructive: *confirm,
	})
	if result == nil {
		return err
	}
	resolved := resolution.Document()
	if writeErr := writeCLIResponse(output, &OperatorResponse{Apply: result, Resolution: &resolved}); writeErr != nil {
		return writeErr
	}
	return err
}

func runCLIGetApplyProgress(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("deployment apply-progress get")
	scopeID := set.String("scope", "", "operator scope")
	recordID := set.String("id", "", "apply progress ID")
	if err := set.Parse(args); err != nil || requireNoCLIArgs(set) != nil {
		return ErrInvalidCLIRequest
	}
	record, err := operator.GetApplyProgress(ctx, OperatorPrincipal{ScopeID: *scopeID}, *recordID)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{ApplyProgress: record})
}

func runCLIListApplyProgress(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("deployment apply-progress list")
	scopeID := set.String("scope", "", "operator scope")
	limit := set.Int("limit", MaxOperatorListLimit, "maximum apply progress records")
	if err := set.Parse(args); err != nil || requireNoCLIArgs(set) != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireCLIListLimit(*limit); err != nil {
		return err
	}
	records, err := operator.ListApplyProgress(ctx, OperatorPrincipal{ScopeID: *scopeID}, *limit)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{ApplyProgresses: records})
}

func runCLIGetApplyProgressByOperation(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("deployment apply-progress operation")
	scopeID := set.String("scope", "", "operator scope")
	operationID := set.String("id", "", "operation ID")
	if err := set.Parse(args); err != nil || requireNoCLIArgs(set) != nil {
		return ErrInvalidCLIRequest
	}
	record, err := operator.GetApplyProgressByOperation(ctx, OperatorPrincipal{ScopeID: *scopeID}, *operationID)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{ApplyProgress: record})
}

func runCLIGetRecovery(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("deployment recovery get")
	scopeID := set.String("scope", "", "operator scope")
	recordID := set.String("id", "", "recovery record ID")
	if err := set.Parse(args); err != nil || requireNoCLIArgs(set) != nil {
		return ErrInvalidCLIRequest
	}
	record, err := operator.GetRecovery(ctx, OperatorPrincipal{ScopeID: *scopeID}, *recordID)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{Recovery: record})
}

func runCLIListRecoveries(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("deployment recovery list")
	scopeID := set.String("scope", "", "operator scope")
	limit := set.Int("limit", MaxOperatorListLimit, "maximum recovery records")
	if err := set.Parse(args); err != nil || requireNoCLIArgs(set) != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireCLIListLimit(*limit); err != nil {
		return err
	}
	records, err := operator.ListRecoveries(ctx, OperatorPrincipal{ScopeID: *scopeID}, *limit)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{Recoveries: records})
}

func runCLIRunRecovery(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("deployment recovery run")
	scopeID := set.String("scope", "", "operator scope")
	requestID := set.String("request-id", "", "recovery request ID")
	applyProgressID := set.String("apply-progress-id", "", "apply progress ID")
	action := set.String("action", "", "recovery action")
	if err := set.Parse(args); err != nil || requireNoCLIArgs(set) != nil {
		return ErrInvalidCLIRequest
	}
	response, err := operator.Recover(ctx, OperatorPrincipal{ScopeID: *scopeID}, deployment.RecoveryRequest{
		RequestID: *requestID, ApplyProgressID: *applyProgressID, Action: models.RecoveryAction(*action),
	})
	if err != nil {
		return err
	}
	return writeCLIResponse(output, response)
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

func requireCLIListLimit(limit int) error {
	return validateOperatorListLimit(limit)
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

func runCLIListResources(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("resource list")
	scopeID := set.String("scope", "", "operator scope")
	limit := set.Int("limit", MaxOperatorListLimit, "maximum resources")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	if err := requireCLIListLimit(*limit); err != nil {
		return err
	}
	resources, err := operator.ListResources(ctx, OperatorPrincipal{ScopeID: *scopeID}, *limit)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{Resources: resources})
}

func runCLIDeleteResource(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("resource delete")
	scopeID := set.String("scope", "", "operator scope")
	resourceID := set.String("id", "", "resource ID")
	confirm := set.Bool("confirm", false, "confirm resource deletion")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	if !*confirm {
		return ErrDestructiveConfirmationRequired
	}
	if err := operator.DeleteResource(ctx, OperatorPrincipal{ScopeID: *scopeID}, *resourceID); err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{})
}

func runCLIAcquireResourceLock(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("resource lock acquire")
	scopeID := set.String("scope", "", "operator scope")
	resourceID := set.String("id", "", "resource ID")
	owner := set.String("owner", "", "lock owner")
	token := set.String("token", "", "lock token")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	lock := models.ResourceLock{Owner: *owner, Token: *token}
	if err := operator.AcquireResourceLock(ctx, OperatorPrincipal{ScopeID: *scopeID}, *resourceID, lock); err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{Lock: &lock})
}

func runCLIReleaseResourceLock(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("resource lock release")
	scopeID := set.String("scope", "", "operator scope")
	resourceID := set.String("id", "", "resource ID")
	owner := set.String("owner", "", "lock owner")
	token := set.String("token", "", "lock token")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	if err := operator.ReleaseResourceLock(ctx, OperatorPrincipal{ScopeID: *scopeID}, *resourceID, models.ResourceLock{Owner: *owner, Token: *token}); err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{})
}

func runCLIInspectResourceLock(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("resource lock inspect")
	scopeID := set.String("scope", "", "operator scope")
	resourceID := set.String("id", "", "resource ID")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	lock, err := operator.InspectResourceLock(ctx, OperatorPrincipal{ScopeID: *scopeID}, *resourceID)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{Lock: lock})
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

func runCLIVerifyBlob(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("blob verify")
	scopeID := set.String("scope", "", "operator scope")
	bucketID := set.String("bucket", "", "bucket resource ID")
	objectKey := set.String("key", "", "object key")
	if err := set.Parse(args); err != nil || requireNoCLIArgs(set) != nil {
		return ErrInvalidCLIRequest
	}
	response, err := operator.VerifyBlob(ctx, OperatorPrincipal{ScopeID: *scopeID}, *bucketID, *objectKey)
	if response != nil {
		if writeErr := writeCLIResponse(output, response); writeErr != nil {
			return writeErr
		}
	}
	return err
}

func runCLIRecoverBlob(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("blob recover")
	scopeID := set.String("scope", "", "operator scope")
	bucketID := set.String("bucket", "", "bucket resource ID")
	objectKey := set.String("key", "", "object key")
	expectedSHA256 := set.String("expected-sha256", "", "trusted content checksum")
	data := set.String("data", "", "trusted object content")
	requestID := set.String("request-id", "", "idempotency request ID")
	correlationID := set.String("correlation-id", "", "correlation ID")
	if err := set.Parse(args); err != nil || requireNoCLIArgs(set) != nil {
		return ErrInvalidCLIRequest
	}
	response, err := operator.RecoverBlob(ctx, OperatorPrincipal{ScopeID: *scopeID}, *bucketID, *objectKey, *expectedSHA256, []byte(*data), *requestID, *correlationID)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, response)
}

func runCLIListBlobs(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("blob list")
	scopeID := set.String("scope", "", "operator scope")
	bucketID := set.String("bucket", "", "bucket resource ID")
	limit := set.Int("limit", MaxOperatorListLimit, "maximum objects")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	if err := requireCLIListLimit(*limit); err != nil {
		return err
	}
	response, err := operator.ListBlobs(ctx, OperatorPrincipal{ScopeID: *scopeID}, *bucketID, *limit)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, response)
}

func runCLIDeleteBlob(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("blob delete")
	scopeID := set.String("scope", "", "operator scope")
	bucketID := set.String("bucket", "", "bucket resource ID")
	objectKey := set.String("key", "", "object key")
	confirm := set.Bool("confirm", false, "confirm object deletion")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	if !*confirm {
		return ErrDestructiveConfirmationRequired
	}
	if err := operator.DeleteBlob(ctx, OperatorPrincipal{ScopeID: *scopeID}, *bucketID, *objectKey); err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{})
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

func runCLIListOperations(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("operation list")
	scopeID := set.String("scope", "", "operator scope")
	resourceID := set.String("resource", "", "resource ID")
	limit := set.Int("limit", MaxOperatorListLimit, "maximum operations")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	if err := requireCLIListLimit(*limit); err != nil {
		return err
	}
	operations, err := operator.ListOperations(ctx, OperatorPrincipal{ScopeID: *scopeID}, *resourceID, *limit)
	if err != nil {
		return err
	}
	return writeCLIResponse(output, &OperatorResponse{Operations: operations})
}

func runCLIListAudit(ctx context.Context, operator *Operator, args []string, output io.Writer) error {
	set := newCLIFlagSet("audit list")
	scopeID := set.String("scope", "", "operator scope")
	resourceID := set.String("resource", "", "resource ID")
	limit := set.Int("limit", MaxOperatorListLimit, "maximum entries")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	if err := requireCLIListLimit(*limit); err != nil {
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
	confirm := set.Bool("confirm", false, "confirm reset")
	if err := set.Parse(args); err != nil {
		return ErrInvalidCLIRequest
	}
	if err := requireNoCLIArgs(set); err != nil {
		return err
	}
	if !*confirm {
		return ErrDestructiveConfirmationRequired
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
