package temporal

import (
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// WorkerOptions tunes the worker.
type WorkerOptions struct {
	// MaxConcurrentActivityExecutions caps in-flight activities. Default 100.
	MaxConcurrentActivityExecutions int

	// MaxConcurrentWorkflowTaskExecutions caps in-flight workflow tasks. Default 100.
	MaxConcurrentWorkflowTaskExecutions int
}

// Worker wraps an SDK worker with our defaults applied.
type Worker struct {
	w worker.Worker
}

// NewWorker creates a worker bound to the given task queue. The caller must
// register workflows + activities before calling Start.
func NewWorker(cli client.Client, taskQueue string, opts WorkerOptions) *Worker {
	if opts.MaxConcurrentActivityExecutions == 0 {
		opts.MaxConcurrentActivityExecutions = 100
	}
	if opts.MaxConcurrentWorkflowTaskExecutions == 0 {
		opts.MaxConcurrentWorkflowTaskExecutions = 100
	}
	w := worker.New(cli, taskQueue, worker.Options{
		MaxConcurrentActivityExecutionSize:     opts.MaxConcurrentActivityExecutions,
		MaxConcurrentWorkflowTaskExecutionSize: opts.MaxConcurrentWorkflowTaskExecutions,
	})
	return &Worker{w: w}
}

// RegisterWorkflow makes a workflow function executable on this task queue.
func (w *Worker) RegisterWorkflow(workflow any) {
	w.w.RegisterWorkflow(workflow)
}

// RegisterActivity makes an activity function executable on this task queue.
func (w *Worker) RegisterActivity(activity any) {
	w.w.RegisterActivity(activity)
}

// Start begins polling the task queue. Non-blocking.
func (w *Worker) Start() error {
	if err := w.w.Start(); err != nil {
		return fmt.Errorf("temporal worker: start: %w", err)
	}
	return nil
}

// Stop gracefully drains in-flight tasks and shuts down.
func (w *Worker) Stop() {
	w.w.Stop()
}

// Run blocks until interruptCh receives or the worker fails. Use this when
// the worker is the main process; otherwise prefer Start + Stop.
func (w *Worker) Run(interruptCh <-chan any) error {
	if err := w.w.Run(interruptCh); err != nil {
		return fmt.Errorf("temporal worker: run: %w", err)
	}
	return nil
}
