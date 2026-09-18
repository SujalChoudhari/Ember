package ember

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

var ErrOperatorQueueUnavailable = errors.New("operator queue unavailable")

func (operator *Operator) queueRuntime(principal OperatorPrincipal) (*queue.FileQueue, error) {
	if operator == nil || operator.workQueue == nil {
		return nil, ErrOperatorQueueUnavailable
	}
	if err := principal.validate(); err != nil {
		return nil, err
	}
	return operator.workQueue, nil
}

func (operator *Operator) EnqueueOperatorMessage(ctx context.Context, principal OperatorPrincipal, correlationID string, payload []byte) (string, error) {
	workQueue, err := operator.queueRuntime(principal)
	if err != nil {
		return "", err
	}
	return workQueue.Enqueue(ctx, correlationID, payload)
}

func (operator *Operator) ReceiveOperatorMessage(ctx context.Context, principal OperatorPrincipal) (queue.ReceivedMessage, error) {
	workQueue, err := operator.queueRuntime(principal)
	if err != nil {
		return queue.ReceivedMessage{}, err
	}
	return workQueue.Receive(ctx)
}

func (operator *Operator) AcknowledgeOperatorMessage(ctx context.Context, principal OperatorPrincipal, receipt string) error {
	workQueue, err := operator.queueRuntime(principal)
	if err != nil {
		return err
	}
	return workQueue.Acknowledge(ctx, receipt)
}

func (operator *Operator) QueueRuntimeStatus() string {
	if operator == nil || operator.workQueue == nil {
		return "unavailable"
	}
	return "file-persistent"
}
