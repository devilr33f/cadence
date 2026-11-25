package tasks

import (
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

// ReplyRequest answers a task question.
type ReplyRequest struct {
	Message string `json:"message" minLength:"1" maxLength:"2000"`
	Token   string `json:"token" doc:"the question token, from the task"`
}

// ApproveRequest answers a task approval question.
type ApproveRequest struct {
	Decision string `json:"decision" enum:"once,deny"`
	Token    string `json:"token" doc:"the question token, from the task"`
}

var statuses = []string{StatusQueued, StatusRunning, StatusWaitingInput, StatusDone, StatusFailed, StatusCancelled, StatusUnknown}

var docs = map[string]api.Doc{
	"list-task-kinds": {Summary: "List the task kinds and their runners"},
	"list-tasks": {Summary: "List tasks", Query: []api.Query{
		{Name: "kind", Schema: api.Text(64)},
		{Name: "status", Note: "comma separated: " + strings.Join(statuses, ", "), Schema: api.Text(128)},
		{Name: "device", Schema: api.UUID()},
		{Name: "limit", Note: "default " + strconv.Itoa(defaultPage), Schema: api.Int(1, maxPage)},
	}},
	"get-task": {Summary: "Get a task with its history"},
	"inspect-task": {Summary: "Inspect a running task", Query: []api.Query{
		{Name: "detail", Schema: api.Enum(string(DetailSummary), string(DetailTranscript))},
	}},
	"cancel-task":   {Summary: "Cancel a task"},
	"reply-to-task": {Summary: "Reply to a task question", Body: ReplyRequest{}, BodyRequired: true},
	"approve-task":  {Summary: "Answer a task approval question", Body: ApproveRequest{}, BodyRequired: true},
}
