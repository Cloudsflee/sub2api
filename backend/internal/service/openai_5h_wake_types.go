package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	OpenAI5hWakeTaskStatusPending          = "pending"
	OpenAI5hWakeTaskStatusRunning          = "running"
	OpenAI5hWakeTaskStatusSucceeded        = "succeeded"
	OpenAI5hWakeTaskStatusPartialSucceeded = "partial_succeeded"
	OpenAI5hWakeTaskStatusFailed           = "failed"
	OpenAI5hWakeTaskStatusCancelled        = "cancelled"

	OpenAI5hWakeItemStatusPending       = "pending"
	OpenAI5hWakeItemStatusRunning       = "running"
	OpenAI5hWakeItemStatusWoken         = "woken"
	OpenAI5hWakeItemStatusSkippedActive = "skipped_active"
	OpenAI5hWakeItemStatusFailed        = "failed"
	OpenAI5hWakeItemStatusCancelled     = "cancelled"

	OpenAI5hWakeEventLevelInfo  = "info"
	OpenAI5hWakeEventLevelWarn  = "warn"
	OpenAI5hWakeEventLevelError = "error"
)

var ErrOpenAI5hWakeTaskNotFound = infraerrors.NotFound("OPENAI_5H_WAKE_TASK_NOT_FOUND", "OpenAI 5h wake task not found")
var ErrOpenAI5hWakeNoEligiblePools = infraerrors.BadRequest("OPENAI_5H_WAKE_NO_ELIGIBLE_POOLS", "no eligible quota pools are available for OpenAI 5h wake")

type OpenAI5hWakeExclusions struct {
	APIKey          int `json:"api_key"`
	NonOAuth        int `json:"non_oauth"`
	SparkShadow     int `json:"spark_shadow"`
	NonGlobal       int `json:"non_global"`
	No5hEntitlement int `json:"no_5h_entitlement"`
	Disabled        int `json:"disabled"`
	Unschedulable   int `json:"unschedulable"`
	Expired         int `json:"expired"`
	RateLimited     int `json:"rate_limited"`
	CoolingDown     int `json:"cooling_down"`
	MissingIdentity int `json:"missing_identity"`
}

type OpenAI5hWakePreview struct {
	TotalOpenAIAccounts int                    `json:"total_openai_accounts"`
	EligibleAccounts    int                    `json:"eligible_accounts"`
	UniqueQuotaPools    int                    `json:"unique_quota_pools"`
	ActiveWindows       int                    `json:"active_windows"`
	EstimatedRequests   int                    `json:"estimated_requests"`
	Excluded            OpenAI5hWakeExclusions `json:"excluded"`
}

type OpenAI5hWakeTask struct {
	ID                    int64      `json:"id"`
	Status                string     `json:"status"`
	EligibleAccountCount  int        `json:"eligible_account_count"`
	ActiveWindowCount     int        `json:"active_window_count"`
	EstimatedRequestCount int        `json:"estimated_request_count"`
	TotalItems            int        `json:"total_items"`
	ProcessedItems        int        `json:"processed_items"`
	RunningItemCount      int        `json:"running_item_count"`
	WokenCount            int        `json:"woken_count"`
	SkippedActiveCount    int        `json:"skipped_active_count"`
	FailedCount           int        `json:"failed_count"`
	CancelledCount        int        `json:"cancelled_count"`
	EarliestResetAt       *time.Time `json:"earliest_reset_at,omitempty"`
	LatestResetAt         *time.Time `json:"latest_reset_at,omitempty"`
	AlignmentSpanSeconds  int64      `json:"alignment_span_seconds"`
	CancelRequestedAt     *time.Time `json:"cancel_requested_at,omitempty"`
	StartedAt             *time.Time `json:"started_at,omitempty"`
	FinishedAt            *time.Time `json:"finished_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`

	RequestedByUserID *int64     `json:"-"`
	RequestedByEmail  string     `json:"-"`
	LeaseOwner        string     `json:"-"`
	LeaseExpiresAt    *time.Time `json:"-"`
	HeartbeatAt       *time.Time `json:"-"`
}

func (t *OpenAI5hWakeTask) IsTerminal() bool {
	if t == nil {
		return false
	}
	switch t.Status {
	case OpenAI5hWakeTaskStatusSucceeded, OpenAI5hWakeTaskStatusPartialSucceeded, OpenAI5hWakeTaskStatusFailed, OpenAI5hWakeTaskStatusCancelled:
		return true
	default:
		return false
	}
}

func (t *OpenAI5hWakeTask) ComputeAlignmentSpanSeconds() int64 {
	if t == nil || t.EarliestResetAt == nil || t.LatestResetAt == nil {
		return 0
	}
	seconds := int64(t.LatestResetAt.Sub(*t.EarliestResetAt).Seconds())
	if seconds < 0 {
		return 0
	}
	return seconds
}

type OpenAI5hWakeTaskItem struct {
	ID                  int64      `json:"id"`
	TaskID              int64      `json:"task_id"`
	IdentityHash        string     `json:"identity_hash"`
	MemberAccountIDs    []int64    `json:"member_account_ids"`
	AttemptedAccountIDs []int64    `json:"attempted_account_ids"`
	SuccessfulAccountID *int64     `json:"successful_account_id,omitempty"`
	Status              string     `json:"status"`
	AttemptCount        int        `json:"attempt_count"`
	ErrorCode           string     `json:"error_code,omitempty"`
	ResetAt             *time.Time `json:"reset_at,omitempty"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// OpenAI5hWakeTaskEvent is a durable, administrator-visible execution event.
type OpenAI5hWakeTaskEvent struct {
	ID        int64     `json:"id"`
	TaskID    int64     `json:"task_id"`
	ItemID    *int64    `json:"item_id,omitempty"`
	Level     string    `json:"level"`
	Code      string    `json:"code"`
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type OpenAI5hWakeTaskEventParams struct {
	TaskID  int64
	ItemID  *int64
	Level   string
	Code    string
	Message string
}

type OpenAI5hWakeTaskItemSeed struct {
	IdentityHash     string
	MemberAccountIDs []int64
}

type OpenAI5hWakeCreateParams struct {
	EligibleAccountCount  int
	ActiveWindowCount     int
	EstimatedRequestCount int
	RequestedByUserID     *int64
	RequestedByEmail      string
	Items                 []OpenAI5hWakeTaskItemSeed
}

type OpenAI5hWakeCompleteItemParams struct {
	ItemID              int64
	Status              string
	AttemptedAccountIDs []int64
	SuccessfulAccountID *int64
	ResetAt             *time.Time
	ErrorCode           string
}

type OpenAI5hWakeTaskRepository interface {
	CreateOrGetActive(ctx context.Context, params OpenAI5hWakeCreateParams) (*OpenAI5hWakeTask, bool, error)
	GetTask(ctx context.Context, id int64) (*OpenAI5hWakeTask, error)
	GetLatestTask(ctx context.Context) (*OpenAI5hWakeTask, error)
	CountRunningTaskItems(ctx context.Context, taskID int64) (int, error)
	ListTaskItems(ctx context.Context, taskID int64, page, pageSize int) ([]*OpenAI5hWakeTaskItem, int64, error)
	ListTaskEvents(ctx context.Context, taskID int64, page, pageSize int) ([]*OpenAI5hWakeTaskEvent, int64, error)
	AppendTaskEvent(ctx context.Context, params OpenAI5hWakeTaskEventParams) error
	ClaimTask(ctx context.Context, owner string, now, leaseUntil time.Time) (*OpenAI5hWakeTask, error)
	HeartbeatTask(ctx context.Context, taskID int64, owner string, now, leaseUntil time.Time) (bool, error)
	RecoverTaskItems(ctx context.Context, taskID int64, owner string, maxAttempts int) (int, error)
	ClaimNextItem(ctx context.Context, taskID int64, owner string) (*OpenAI5hWakeTaskItem, error)
	CompleteItem(ctx context.Context, taskID int64, owner string, params OpenAI5hWakeCompleteItemParams) (bool, error)
	RequestCancel(ctx context.Context, taskID int64, now time.Time) (*OpenAI5hWakeTask, bool, error)
	IsCancelRequested(ctx context.Context, taskID int64) (bool, error)
	FinalizeTask(ctx context.Context, taskID int64, owner string, cancelled bool, now time.Time) (*OpenAI5hWakeTask, error)
	DeleteTerminalBefore(ctx context.Context, cutoff time.Time) (int64, error)
}
