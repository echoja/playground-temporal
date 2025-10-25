package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	temporalworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	syncTaskQueue          = "worker-sync-task-queue"
	syncWorkflowName       = "worker.sync.site"
	syncUsersActivityName  = "worker.sync.users"
	syncOrdersActivityName = "worker.sync.orders"
)

// SyncActivities hosts the activity implementations that reuse the existing server logic.
type SyncActivities struct {
	server *Server
	logger *slog.Logger
}

func NewSyncActivities(server *Server, logger *slog.Logger) *SyncActivities {
	return &SyncActivities{server: server, logger: logger}
}

// SyncUsersActivity pulls users from the builder and stores events.
func (a *SyncActivities) SyncUsersActivity(ctx context.Context, input SyncWorkflowInput) (SyncSummary, error) {
	site, err := a.server.store.GetSite(ctx, input.SiteID)
	if err != nil {
		return SyncSummary{}, err
	}
	summary, err := a.server.syncSite(ctx, site, input.Page, input.Start, input.End, a.server.fetchUsersPage)
	if err != nil {
		a.logger.Error("activity sync users failed", "site_id", input.SiteID, "error", err, "reason", input.Reason)
		return summary, err
	}
	a.logger.Info("activity sync users", "site_id", input.SiteID, "inserted", summary.Inserted, "skipped", summary.Skipped, "pages", summary.Pages, "reason", input.Reason)
	return summary, nil
}

// SyncOrdersActivity pulls orders from the builder and stores events.
func (a *SyncActivities) SyncOrdersActivity(ctx context.Context, input SyncWorkflowInput) (SyncSummary, error) {
	site, err := a.server.store.GetSite(ctx, input.SiteID)
	if err != nil {
		return SyncSummary{}, err
	}
	summary, err := a.server.syncSite(ctx, site, input.Page, input.Start, input.End, a.server.fetchOrdersPage)
	if err != nil {
		a.logger.Error("activity sync orders failed", "site_id", input.SiteID, "error", err, "reason", input.Reason)
		return summary, err
	}
	a.logger.Info("activity sync orders", "site_id", input.SiteID, "inserted", summary.Inserted, "skipped", summary.Skipped, "pages", summary.Pages, "reason", input.Reason)
	return summary, nil
}

// SyncSiteWorkflow orchestrates users/orders sync sequentially, guaranteeing all I/O flows through Temporal.
func SyncSiteWorkflow(ctx workflow.Context, input SyncWorkflowInput) (SyncWorkflowResult, error) {
	logger := workflow.GetLogger(ctx)
	if input.SiteID == "" {
		return SyncWorkflowResult{}, errors.New("site_id required")
	}
	options := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:        5,
			InitialInterval:        time.Second,
			BackoffCoefficient:     2.0,
			MaximumInterval:        30 * time.Second,
			NonRetryableErrorTypes: []string{"InvalidAccessKey", "SiteNotFound"},
		},
	}
	ctx = workflow.WithActivityOptions(ctx, options)

	result := SyncWorkflowResult{StartedAt: workflow.Now(ctx)}
	logger.Info("sync workflow started", "site_id", input.SiteID, "include_users", input.IncludeUsers, "include_orders", input.IncludeOrders, "reason", input.Reason)

	if input.IncludeUsers {
		var summary SyncSummary
		if err := workflow.ExecuteActivity(ctx, syncUsersActivityName, input).Get(ctx, &summary); err != nil {
			logger.Error("users activity failed", "error", err)
			return result, err
		}
		result.Users = &summary
	}

	if input.IncludeOrders {
		var summary SyncSummary
		if err := workflow.ExecuteActivity(ctx, syncOrdersActivityName, input).Get(ctx, &summary); err != nil {
			logger.Error("orders activity failed", "error", err)
			return result, err
		}
		result.Orders = &summary
	}

	result.CompletedAt = workflow.Now(ctx)
	logger.Info("sync workflow finished", "site_id", input.SiteID, "include_users", input.IncludeUsers, "include_orders", input.IncludeOrders, "reason", input.Reason)
	return result, nil
}

// RegisterSyncWorker wires up the Temporal worker consuming the sync task queue.
func RegisterSyncWorker(c client.Client, srv *Server, logger *slog.Logger) temporalworker.Worker {
	w := temporalworker.New(c, syncTaskQueue, temporalworker.Options{})
	w.RegisterWorkflowWithOptions(SyncSiteWorkflow, workflow.RegisterOptions{Name: syncWorkflowName})
	activities := NewSyncActivities(srv, logger.With("component", "sync.activities"))
	w.RegisterActivityWithOptions(activities.SyncUsersActivity, activity.RegisterOptions{Name: syncUsersActivityName})
	w.RegisterActivityWithOptions(activities.SyncOrdersActivity, activity.RegisterOptions{Name: syncOrdersActivityName})
	return w
}

// TemporalOrchestrator starts workflows through the Temporal client so every sync flows through the same pipeline.
type TemporalOrchestrator struct {
	client client.Client
	logger *slog.Logger
}

func NewTemporalOrchestrator(c client.Client, logger *slog.Logger) *TemporalOrchestrator {
	return &TemporalOrchestrator{client: c, logger: logger.With("component", "sync.orchestrator")}
}

func (o *TemporalOrchestrator) RunSync(ctx context.Context, input SyncWorkflowInput) (SyncWorkflowResult, error) {
	workflowID := fmt.Sprintf("sync-%s-%d", input.SiteID, time.Now().UnixNano())
	options := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                syncTaskQueue,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowExecutionTimeout: 30 * time.Minute,
	}
	we, err := o.client.ExecuteWorkflow(ctx, options, SyncSiteWorkflow, input)
	if err != nil {
		o.logger.Error("start workflow failed", "site_id", input.SiteID, "error", err)
		return SyncWorkflowResult{}, err
	}
	var result SyncWorkflowResult
	if err := we.Get(ctx, &result); err != nil {
		o.logger.Error("wait workflow failed", "workflow_id", we.GetID(), "error", err)
		result.WorkflowID = we.GetID()
		result.RunID = we.GetRunID()
		return result, err
	}
	result.WorkflowID = we.GetID()
	result.RunID = we.GetRunID()
	o.logger.Info("workflow completed", "workflow_id", result.WorkflowID, "run_id", result.RunID, "site_id", input.SiteID, "include_users", input.IncludeUsers, "include_orders", input.IncludeOrders)
	return result, nil
}

func (o *TemporalOrchestrator) RunSyncAsync(ctx context.Context, input SyncWorkflowInput) (string, error) {
	workflowID := fmt.Sprintf("sync-%s-%d", input.SiteID, time.Now().UnixNano())
	options := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                syncTaskQueue,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowExecutionTimeout: 30 * time.Minute,
	}
	we, err := o.client.ExecuteWorkflow(ctx, options, SyncSiteWorkflow, input)
	if err != nil {
		o.logger.Error("start workflow async failed", "site_id", input.SiteID, "error", err)
		return "", err
	}
	o.logger.Info("workflow dispatched", "workflow_id", we.GetID(), "run_id", we.GetRunID(), "site_id", input.SiteID)
	return we.GetID(), nil
}

// SyncTaskQueue exposes the queue name so callers can reference it in metrics/tests.
func SyncTaskQueue() string {
	return syncTaskQueue
}
