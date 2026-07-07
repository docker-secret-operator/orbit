package stack

import (
	"fmt"
	"time"

	"go.uber.org/zap"
)

// RolloutTransaction represents an atomic rollout operation with rollback capability.
type RolloutTransaction struct {
	service       string
	client        DockerClient
	rollout       *StackRollout
	log           *zap.Logger
	operations    []RolloutOperation
	state         TransactionState
	startedAt     time.Time
	completedAt   time.Time
	savedOldState *ServiceRolloutState
}

// TransactionState represents the state of a transaction.
type TransactionState string

const (
	TxnPending    TransactionState = "pending"
	TxnInProgress TransactionState = "in_progress"
	TxnCompleted  TransactionState = "completed"
	TxnFailed     TransactionState = "failed"
	TxnRolledBack TransactionState = "rolled_back"
)

// RolloutOperation represents a single operation in a rollout transaction.
type RolloutOperation struct {
	Name         string
	Execute      func() error
	Rollback     func() error
	Executed     bool
	ExecutedAt   time.Time
	RolledBack   bool
	RolledBackAt time.Time
	Error        error
}

// NewRolloutTransaction creates a new atomic rollout transaction.
func NewRolloutTransaction(service string, client DockerClient, rollout *StackRollout, log *zap.Logger) *RolloutTransaction {
	if log == nil {
		log = zap.NewNop()
	}

	return &RolloutTransaction{
		service:    service,
		client:     client,
		rollout:    rollout,
		log:        log,
		operations: make([]RolloutOperation, 0),
		state:      TxnPending,
		startedAt:  time.Now(),
	}
}

// AddOperation adds an operation to the transaction.
func (t *RolloutTransaction) AddOperation(name string, execute, rollback func() error) {
	t.operations = append(t.operations, RolloutOperation{
		Name:     name,
		Execute:  execute,
		Rollback: rollback,
	})
}

// Execute runs all operations in sequence. If any fails, rolls back all previous.
func (t *RolloutTransaction) Execute() error {
	if t.state != TxnPending {
		return fmt.Errorf("transaction already in progress or completed")
	}

	// Save current state for rollback
	t.rollout.mu.Lock()
	if state, ok := t.rollout.state.ServiceStates[t.service]; ok {
		t.savedOldState = &ServiceRolloutState{
			Service:      state.Service,
			Status:       state.Status,
			OldContainer: state.OldContainer,
			NewContainer: state.NewContainer,
		}
	}
	t.rollout.mu.Unlock()

	t.state = TxnInProgress
	t.log.Info("transaction started",
		zap.String("service", t.service),
		zap.Int("operation_count", len(t.operations)))

	// Execute all operations
	for i, op := range t.operations {
		t.log.Debug("executing operation",
			zap.String("service", t.service),
			zap.String("operation", op.Name),
			zap.Int("step", i+1),
			zap.Int("total", len(t.operations)))

		if err := op.Execute(); err != nil {
			t.log.Error("operation failed, starting rollback",
				zap.String("service", t.service),
				zap.String("operation", op.Name),
				zap.Error(err))

			t.operations[i].Error = err
			t.operations[i].Executed = true
			t.operations[i].ExecutedAt = time.Now()

			// Rollback all previous operations
			if err := t.rollback(i); err != nil {
				t.state = TxnFailed
				t.rollout.UpdateServiceStatus(t.service, StatusFailed, err)
				return err
			}

			t.state = TxnRolledBack
			return fmt.Errorf("transaction rolled back: %w", err)
		}

		t.operations[i].Executed = true
		t.operations[i].ExecutedAt = time.Now()

		t.log.Debug("operation succeeded",
			zap.String("service", t.service),
			zap.String("operation", op.Name))
	}

	t.state = TxnCompleted
	t.completedAt = time.Now()

	t.log.Info("transaction completed successfully",
		zap.String("service", t.service),
		zap.Duration("duration", time.Since(t.startedAt)))

	return nil
}

// rollback rolls back all executed operations in reverse order.
func (t *RolloutTransaction) rollback(failedIndex int) error {
	t.log.Warn("rolling back transaction",
		zap.String("service", t.service),
		zap.Int("operations_to_rollback", failedIndex))

	// Rollback in reverse order
	for i := failedIndex - 1; i >= 0; i-- {
		op := &t.operations[i]
		if !op.Executed {
			continue
		}

		if op.Rollback == nil {
			t.log.Warn("operation has no rollback",
				zap.String("service", t.service),
				zap.String("operation", op.Name))
			continue
		}

		t.log.Debug("rolling back operation",
			zap.String("service", t.service),
			zap.String("operation", op.Name))

		if err := op.Rollback(); err != nil {
			t.log.Error("rollback failed",
				zap.String("service", t.service),
				zap.String("operation", op.Name),
				zap.Error(err))
			// Continue rolling back other operations even if one fails
		} else {
			op.RolledBack = true
			op.RolledBackAt = time.Now()
			t.log.Debug("operation rolled back",
				zap.String("service", t.service),
				zap.String("operation", op.Name))
		}
	}

	// Restore saved state if we have it
	if t.savedOldState != nil {
		t.rollout.mu.Lock()
		current := t.rollout.state.ServiceStates[t.service]
		current.Status = t.savedOldState.Status
		current.OldContainer = t.savedOldState.OldContainer
		current.NewContainer = t.savedOldState.NewContainer
		t.rollout.mu.Unlock()
	}

	t.log.Warn("transaction rolled back",
		zap.String("service", t.service))

	return nil
}

// Status returns the current transaction state.
func (t *RolloutTransaction) Status() TransactionState {
	return t.state
}

// Operations returns all operations and their status.
func (t *RolloutTransaction) Operations() []RolloutOperation {
	return t.operations
}

// Summary returns a human-readable summary of the transaction.
func (t *RolloutTransaction) Summary() string {
	duration := time.Since(t.startedAt)
	if !t.completedAt.IsZero() {
		duration = t.completedAt.Sub(t.startedAt)
	}

	executed := 0
	rolledBack := 0
	failed := 0

	for _, op := range t.operations {
		if op.Executed {
			executed++
		}
		if op.RolledBack {
			rolledBack++
		}
		if op.Error != nil {
			failed++
		}
	}

	return fmt.Sprintf(
		"Transaction[%s]: service=%s state=%s executed=%d rollback=%d failed=%d duration=%v",
		t.service, t.service, t.state, executed, rolledBack, failed, duration,
	)
}

// TransactionBuilder provides a fluent interface for building transactions.
type TransactionBuilder struct {
	transaction *RolloutTransaction
}

// NewTransactionBuilder creates a new transaction builder.
func NewTransactionBuilder(service string, client DockerClient, rollout *StackRollout, log *zap.Logger) *TransactionBuilder {
	return &TransactionBuilder{
		transaction: NewRolloutTransaction(service, client, rollout, log),
	}
}

// AddCreateContainer adds a create container operation.
func (b *TransactionBuilder) AddCreateContainer(opts *RunOptions) *TransactionBuilder {
	var containerID string

	b.transaction.AddOperation(
		"create_container",
		func() error {
			id, err := b.transaction.client.CreateContainer(opts)
			if err != nil {
				return err
			}
			containerID = id

			b.transaction.rollout.mu.Lock()
			state := b.transaction.rollout.state.ServiceStates[b.transaction.service]
			state.NewContainer = containerID
			b.transaction.rollout.mu.Unlock()
			return nil
		},
		func() error {
			if containerID != "" {
				return b.transaction.client.RemoveContainer(containerID, true)
			}
			return nil
		},
	)

	return b
}

// AddStartContainer adds a start container operation.
func (b *TransactionBuilder) AddStartContainer() *TransactionBuilder {
	b.transaction.AddOperation(
		"start_container",
		func() error {
			b.transaction.rollout.mu.Lock()
			containerID := b.transaction.rollout.state.ServiceStates[b.transaction.service].NewContainer
			b.transaction.rollout.mu.Unlock()
			if containerID == "" {
				return fmt.Errorf("no container to start")
			}
			return b.transaction.client.StartContainer(containerID)
		},
		func() error {
			b.transaction.rollout.mu.Lock()
			containerID := b.transaction.rollout.state.ServiceStates[b.transaction.service].NewContainer
			b.transaction.rollout.mu.Unlock()
			if containerID != "" {
				return b.transaction.client.StopContainer(containerID, 5*time.Second)
			}
			return nil
		},
	)

	return b
}

// AddHealthCheck adds a health check operation.
func (b *TransactionBuilder) AddHealthCheck(timeout time.Duration) *TransactionBuilder {
	b.transaction.AddOperation(
		"health_check",
		func() error {
			b.transaction.rollout.mu.Lock()
			containerID := b.transaction.rollout.state.ServiceStates[b.transaction.service].NewContainer
			b.transaction.rollout.mu.Unlock()
			if containerID == "" {
				return fmt.Errorf("no container for health check")
			}

			deadline := time.Now().Add(timeout)
			for {
				if time.Now().After(deadline) {
					return fmt.Errorf("health check timeout")
				}

				health, err := b.transaction.client.GetContainerHealth(containerID)
				if err != nil {
					return err
				}

				switch health {
				case HealthHealthy:
					b.transaction.rollout.MarkServiceHealthy(b.transaction.service)
					return nil
				case HealthUnhealthy:
					return fmt.Errorf("health check failed")
				case HealthStarting, HealthUnknown:
					time.Sleep(1 * time.Second)
				}
			}
		},
		func() error {
			// No rollback needed for health check
			return nil
		},
	)

	return b
}

// AddSwitchTraffic adds a traffic switch operation.
func (b *TransactionBuilder) AddSwitchTraffic() *TransactionBuilder {
	b.transaction.AddOperation(
		"switch_traffic",
		func() error {
			// In real implementation, this would update load balancer/proxy
			// For now, just mark transition. state.OldContainer already holds
			// the previously-active container that AddCleanup should remove;
			// state.NewContainer is the container taking over traffic.
			return nil
		},
		func() error {
			// Rollback would revert traffic to old container
			// In real implementation, this would call load balancer/proxy
			return nil
		},
	)

	return b
}

// AddDrainConnections adds a connection drain operation.
func (b *TransactionBuilder) AddDrainConnections(timeout time.Duration) *TransactionBuilder {
	b.transaction.AddOperation(
		"drain_connections",
		func() error {
			b.transaction.rollout.mu.Lock()
			oldContainer := b.transaction.rollout.state.ServiceStates[b.transaction.service].OldContainer
			b.transaction.rollout.mu.Unlock()
			if oldContainer == "" {
				return nil // No old container to drain
			}

			_, err := b.transaction.client.WaitForContainer(oldContainer, timeout)
			return err
		},
		nil, // No rollback for drain
	)

	return b
}

// AddCleanup adds a cleanup operation.
func (b *TransactionBuilder) AddCleanup() *TransactionBuilder {
	b.transaction.AddOperation(
		"cleanup",
		func() error {
			b.transaction.rollout.mu.Lock()
			oldContainer := b.transaction.rollout.state.ServiceStates[b.transaction.service].OldContainer
			b.transaction.rollout.mu.Unlock()

			if oldContainer != "" {
				if err := b.transaction.client.RemoveContainer(oldContainer, true); err != nil {
					b.transaction.log.Warn("cleanup failed",
						zap.String("container_id", oldContainer),
						zap.Error(err))
					// Don't fail the transaction on cleanup error
				}
				b.transaction.rollout.mu.Lock()
				b.transaction.rollout.state.ServiceStates[b.transaction.service].OldContainer = ""
				b.transaction.rollout.mu.Unlock()
			}
			return nil
		},
		nil, // No rollback for cleanup
	)

	return b
}

// Build returns the configured transaction.
func (b *TransactionBuilder) Build() *RolloutTransaction {
	return b.transaction
}
