package t3code

import "encoding/json"

// orchestration protocol 2 method names (ORCHESTRATION_V2_WS_METHODS)
const (
	MethodDispatchCommand     = "orchestration.dispatchCommand"
	MethodGetThreadProjection = "orchestration.getThreadProjection"
	MethodLaunchThread        = "orchestration.launchThread"
	MethodSubscribeShell      = "orchestration.subscribeShell"
	MethodSubscribeThread     = "orchestration.subscribeThread"
)

// ProtocolVersion is the orchestration protocol this client speaks, sent as
// /ws?orchestrationProtocol= and the HTTP header ProtocolHeader.
const (
	ProtocolVersion = "2"
	ProtocolHeader  = "x-t3-orchestration-protocol"
)

// The types below hold the fields this package reads of T3's contracts
// (packages/contracts/src/orchestrationV2.ts); every other field is ignored,
// so newer servers decode. Times stay T3's ISO strings.

// Project is an OrchestrationProjectShell.
type Project struct {
	ID                    string          `json:"id"`
	Title                 string          `json:"title"`
	WorkspaceRoot         string          `json:"workspaceRoot"`
	DefaultModelSelection *ModelSelection `json:"defaultModelSelection"`
	ArchivedAt            *string         `json:"archivedAt,omitempty"`
}

// ModelSelection names a provider instance and model; options stay raw.
type ModelSelection struct {
	InstanceID string          `json:"instanceId"`
	Model      string          `json:"model"`
	Options    json.RawMessage `json:"options,omitempty"`
}

// PendingRequest is a thread shell's pendingRuntimeRequest summary.
type PendingRequest struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"createdAt"`
	// Blocking nil means an older server that did not say
	Blocking *bool `json:"blocking,omitempty"`
	UserOnly *bool `json:"userOnly,omitempty"`
}

// ThreadShell is an OrchestrationV2ThreadShell. Status is idle or a run status.
type ThreadShell struct {
	ID                    string          `json:"id"`
	ProjectID             string          `json:"projectId"`
	Title                 string          `json:"title"`
	Status                string          `json:"status"`
	LatestRunID           *string         `json:"latestRunId"`
	ActiveRunID           *string         `json:"activeRunId"`
	PendingRuntimeRequest *PendingRequest `json:"pendingRuntimeRequest"`
	LastErrorCode         *string         `json:"lastErrorCode,omitempty"`
	LastErrorClass        *string         `json:"lastErrorClass,omitempty"`
	ModelSelection        *ModelSelection `json:"modelSelection,omitempty"`
	RuntimeMode           string          `json:"runtimeMode,omitempty"`
	InteractionMode       string          `json:"interactionMode,omitempty"`
	Branch                *string         `json:"branch,omitempty"`
	WorktreePath          *string         `json:"worktreePath,omitempty"`
	UpdatedAt             string          `json:"updatedAt"`
	ArchivedAt            *string         `json:"archivedAt,omitempty"`
	DeletedAt             *string         `json:"deletedAt,omitempty"`
}

// ShellSnapshot is an OrchestrationV2ShellSnapshot.
type ShellSnapshot struct {
	SnapshotSequence int64         `json:"snapshotSequence"`
	Projects         []Project     `json:"projects"`
	Threads          []ThreadShell `json:"threads"`
	ArchivedThreads  []ThreadShell `json:"archivedThreads"`
}

// shell stream item kinds (OrchestrationV2ShellStreamItem)
const (
	ShellSynchronized     = "synchronized"
	ShellSnapshotRequired = "snapshot-required"
	ShellSnapshotKind     = "snapshot"
	ShellProjectUpdated   = "project.updated"
	ShellProjectRemoved   = "project.removed"
	ShellThreadUpdated    = "thread.updated"
	ShellThreadRemoved    = "thread.removed"
)

// ShellItem is one item of subscribeShell. Kinds this package does not know
// arrive with only Kind and Sequence set.
type ShellItem struct {
	Kind      string         `json:"kind"`
	Sequence  int64          `json:"sequence"`
	Snapshot  *ShellSnapshot `json:"snapshot,omitempty"`
	Project   *Project       `json:"project,omitempty"`
	ProjectID string         `json:"projectId,omitempty"`
	Location  string         `json:"location,omitempty"`
	Thread    *ThreadShell   `json:"thread,omitempty"`
	ThreadID  string         `json:"threadId,omitempty"`
}

// cursor is the sequence a resume continues after; 0 for items that have none.
func (s ShellItem) cursor() int64 {
	if s.Kind == ShellSnapshotKind && s.Snapshot != nil {
		return s.Snapshot.SnapshotSequence
	}
	return s.Sequence
}

// Run is an OrchestrationV2Run.
type Run struct {
	ID            string  `json:"id"`
	ThreadID      string  `json:"threadId"`
	Ordinal       int     `json:"ordinal"`
	Status        string  `json:"status"`
	UserMessageID string  `json:"userMessageId"`
	RequestedAt   string  `json:"requestedAt"`
	CompletedAt   *string `json:"completedAt"`
}

// run statuses (OrchestrationV2RunStatus)
const (
	RunCompleted   = "completed"
	RunInterrupted = "interrupted"
	RunFailed      = "failed"
	RunCancelled   = "cancelled"
	RunRolledBack  = "rolled_back"
)

// Terminal reports a run status that ends the run.
func Terminal(status string) bool {
	switch status {
	case RunCompleted, RunInterrupted, RunFailed, RunCancelled, RunRolledBack:
		return true
	}
	return false
}

// RuntimeRequest is an OrchestrationV2RuntimeRequest: what the agent waits
// for. Its question or approval detail is the turn item with its id.
type RuntimeRequest struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// runtime request kinds and statuses
const (
	RequestUserInput = "user_input"
	RequestPending   = "pending"
)

// approvalKinds are the ProviderRequestKind values, the requests a user
// approves or declines.
var approvalKinds = map[string]bool{"command": true, "file-read": true, "file-change": true, "mcp-elicitation": true, "permission": true}

// ApprovalKind reports a runtime request kind that is an approval.
func ApprovalKind(kind string) bool { return approvalKinds[kind] }

// Message is an OrchestrationV2ConversationMessage.
type Message struct {
	ID        string  `json:"id"`
	ThreadID  string  `json:"threadId"`
	RunID     *string `json:"runId"`
	Role      string  `json:"role"`
	Text      string  `json:"text"`
	Streaming bool    `json:"streaming"`
}

// QuestionOption is one option of a user input question.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Value       string `json:"value,omitempty"`
}

// Question is an OrchestrationV2UserInputQuestion; answers are keyed by ID.
type Question struct {
	ID                string           `json:"id"`
	Header            string           `json:"header"`
	Question          string           `json:"question"`
	Options           []QuestionOption `json:"options"`
	MultiSelect       bool             `json:"multiSelect,omitempty"`
	AllowCustomAnswer *bool            `json:"allowCustomAnswer,omitempty"`
}

// CustomAllowed reports whether the question takes a free answer: T3 allows
// one unless allowCustomAnswer is false.
func (q Question) CustomAllowed() bool { return q.AllowCustomAnswer == nil || *q.AllowCustomAnswer }

// ApprovalOption is a ProviderApprovalOption.
type ApprovalOption struct {
	Decision string `json:"decision"`
	Label    string `json:"label"`
}

// Failure is an OrchestrationV2ProviderFailure.
type Failure struct {
	Class   string  `json:"class"`
	Message string  `json:"message"`
	Code    *string `json:"code"`
}

// turn item types this package reads
const (
	ItemUserInputRequest = "user_input_request"
	ItemApprovalRequest  = "approval_request"
	ItemError            = "error"
)

// TurnItem is one OrchestrationV2TurnItem. Only the types above have their
// own fields read: other types may reuse a name with another shape.
type TurnItem struct {
	ID       string  `json:"id"`
	ThreadID string  `json:"threadId"`
	RunID    *string `json:"runId"`
	Type     string  `json:"type"`
	Status   string  `json:"status"`
	// user_input_request and approval_request
	RequestID string `json:"requestId,omitempty"`
	// user_input_request; ResponseMode message is answered by a message to
	// the thread, not runtime-request.respond
	Questions    []Question `json:"questions,omitempty"`
	ResponseMode string     `json:"responseMode,omitempty"`
	// approval_request
	RequestKind string           `json:"requestKind,omitempty"`
	Prompt      string           `json:"prompt,omitempty"`
	Reason      string           `json:"reason,omitempty"`
	Options     []ApprovalOption `json:"options,omitempty"`
	// error
	Failure *Failure `json:"failure,omitempty"`
}

type turnItemBase struct {
	ID       string  `json:"id"`
	ThreadID string  `json:"threadId"`
	RunID    *string `json:"runId"`
	Type     string  `json:"type"`
	Status   string  `json:"status"`
}

func (t *TurnItem) UnmarshalJSON(b []byte) error {
	var base turnItemBase
	if err := json.Unmarshal(b, &base); err != nil {
		return err
	}
	switch base.Type {
	case ItemUserInputRequest, ItemApprovalRequest, ItemError:
		type plain TurnItem
		var p plain
		if err := json.Unmarshal(b, &p); err != nil {
			return err
		}
		*t = TurnItem(p)
	default:
		*t = TurnItem{ID: base.ID, ThreadID: base.ThreadID, RunID: base.RunID, Type: base.Type, Status: base.Status}
	}
	return nil
}

// AppThread is an OrchestrationV2AppThread.
type AppThread struct {
	ID              string          `json:"id"`
	ProjectID       string          `json:"projectId"`
	Title           string          `json:"title"`
	ModelSelection  *ModelSelection `json:"modelSelection,omitempty"`
	RuntimeMode     string          `json:"runtimeMode,omitempty"`
	InteractionMode string          `json:"interactionMode,omitempty"`
}

// ThreadProjection is an OrchestrationV2ThreadProjection.
type ThreadProjection struct {
	Thread          AppThread        `json:"thread"`
	Runs            []Run            `json:"runs"`
	RuntimeRequests []RuntimeRequest `json:"runtimeRequests"`
	Messages        []Message        `json:"messages"`
	TurnItems       []TurnItem       `json:"turnItems"`
}

// domain event types this package reads
const (
	EventRunCreated            = "run.created"
	EventRunUpdated            = "run.updated"
	EventRuntimeRequestUpdated = "runtime-request.updated"
	EventMessageUpdated        = "message.updated"
	EventTurnItemUpdated       = "turn-item.updated"
)

// Event is one OrchestrationV2DomainEvent; Payload is decoded by Type.
type Event struct {
	ID       string          `json:"id"`
	ThreadID string          `json:"threadId"`
	RunID    string          `json:"runId,omitempty"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

// thread stream item kinds (OrchestrationV2ThreadStreamItem)
const (
	ThreadSynchronized = "synchronized"
	ThreadSnapshotKind = "snapshot"
	ThreadEventKind    = "event"
)

// ThreadItem is one item of subscribeThread.
type ThreadItem struct {
	Kind             string            `json:"kind"`
	SnapshotSequence int64             `json:"snapshotSequence"`
	Projection       *ThreadProjection `json:"projection,omitempty"`
	Sequence         int64             `json:"sequence"`
	Event            *Event            `json:"event,omitempty"`
}

func (t ThreadItem) cursor() int64 {
	switch t.Kind {
	case ThreadSnapshotKind:
		return t.SnapshotSequence
	case ThreadEventKind:
		return t.Sequence
	}
	return 0
}
