package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
)

const (
	maxT3Threads = 30
	// t3TitleRunes bounds a title made from the first message
	t3TitleRunes = 60
	t3Timeout    = 30 * time.Second
	// t3WorktreeBase is the ref a new worktree starts from: the project's
	// checkout as it is
	t3WorktreeBase = "HEAD"
)

const t3InstanceProp = `"instance": {"type": "string", "minLength": 1, "maxLength": 64}`

const t3ThreadsInput = `{
	"type": "object",
	"properties": {
		"query": {"type": "string", "minLength": 1, "maxLength": 200},
		"project": {"type": "string", "minLength": 1, "maxLength": 200},
		` + t3InstanceProp + `
	},
	"additionalProperties": false
}`

type t3ThreadView struct {
	Instance string `json:"instance"`
	ID       string `json:"id"`
	Project  string `json:"project"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Waiting  string `json:"waiting,omitempty"`
	Updated  string `json:"updated,omitempty"`
}

type t3ProjectView struct {
	Instance string `json:"instance"`
	ID       string `json:"id"`
	Title    string `json:"title"`
}

// waitingOn is what a thread waits on, by its pending request's kind.
func waitingOn(th t3code.ThreadShell) string {
	switch p := th.PendingRuntimeRequest; {
	case p == nil:
		return ""
	case p.Kind == t3code.RequestUserInput:
		return "question"
	case t3code.ApprovalKind(p.Kind):
		return "approval"
	}
	return "input"
}

// t3Threads lists projects and threads: titles are the user's and the
// agents' words in T3, outside text, so it taints the turn.
func t3Threads(d T3Deps) agent.Tool {
	return agent.Tool{
		Name: "t3_threads",
		Description: "List T3 Code projects and threads (newest first, at most 30): each thread's id, project, title, " +
			"status and what it waits on (a question or an approval). query keeps threads whose title contains it, " +
			"project those of one project (its id or title). Titles are T3's own text: after this, only reading tools " +
			"and subscription_reply can be called in this turn.",
		Input:   t3ThreadsInput,
		Effects: agent.Read, Output: agent.Untrusted,
		Available: t3Available,
		Run: func(_ context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct{ Query, Project, Instance string }
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			shells, e := d.shells(a.Instance)
			if e != nil {
				return nil, e
			}
			projectID, projectInstance := "", ""
			if a.Project != "" {
				p, e := d.project(a.Instance, a.Project)
				if e != nil {
					return nil, e
				}
				projectID, projectInstance = p.project.ID, p.instance
			}
			out := struct {
				Projects []t3ProjectView `json:"projects"`
				Threads  []t3ThreadView  `json:"threads"`
				More     int             `json:"more,omitempty"`
			}{Projects: []t3ProjectView{}, Threads: []t3ThreadView{}}
			q := fold(a.Query)
			for _, s := range shells {
				for _, p := range s.projects {
					if p.ArchivedAt == nil && (projectID == "" || p.ID == projectID && s.instance == projectInstance) {
						out.Projects = append(out.Projects, t3ProjectView{Instance: s.instance, ID: p.ID, Title: p.Title})
					}
				}
				for _, th := range s.threads {
					if projectID != "" && (th.ProjectID != projectID || s.instance != projectInstance) || q != "" && !strings.Contains(fold(th.Title), q) {
						continue
					}
					out.Threads = append(out.Threads, t3ThreadView{
						Instance: s.instance, ID: th.ID, Project: th.ProjectID, Title: th.Title, Status: th.Status,
						Waiting: waitingOn(th), Updated: th.UpdatedAt,
					})
				}
			}
			sort.SliceStable(out.Projects, func(i, j int) bool { return out.Projects[i].Title < out.Projects[j].Title })
			sort.SliceStable(out.Threads, func(i, j int) bool { return out.Threads[i].Updated > out.Threads[j].Updated })
			if n := len(out.Threads); n > maxT3Threads {
				out.More, out.Threads = n-maxT3Threads, out.Threads[:maxT3Threads]
			}
			return encode(out)
		},
	}
}

const t3LaunchInput = `{
	"type": "object",
	"properties": {
		"project": {"type": "string", "minLength": 1, "maxLength": 200},
		"text": {"type": "string", "minLength": 1, "maxLength": 8000},
		"title": {"type": "string", "minLength": 1, "maxLength": 120},
		"worktree": {"type": "boolean"},
		` + t3InstanceProp + `
	},
	"required": ["project", "text"],
	"additionalProperties": false
}`

// t3Launch starts a thread in a project with the user's words as its first
// message, its runtime approval-required (T3 asks before it acts), on the
// project's default model.
func t3Launch(d T3Deps) agent.Tool {
	return agent.Tool{
		Name: "t3_launch",
		Description: "Start a new T3 Code thread in a project (its id or title) with text as its first message, " +
			"from what the user asked; title is optional (made from the text otherwise), worktree true works in a new " +
			"git worktree. The thread asks before it runs commands or changes files. Only on the user's request.",
		Input:   t3LaunchInput,
		Effects: agent.Write, Output: agent.Trusted,
		RequiresWokenSession: true, Timeout: t3Timeout,
		Available: t3Available,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct {
				Project, Text, Title, Instance string
				Worktree                       bool
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			p, e := d.project(a.Instance, a.Project)
			if e != nil {
				return nil, e
			}
			if p.project.DefaultModelSelection == nil || p.project.DefaultModelSelection.Model == "" {
				return nil, &agent.ToolError{Code: "no_default_model", Message: "The project has no default model in T3 Code.", Hint: "Tell the user to set one in T3 Code."}
			}
			client, ok := d.Source.Client(p.instance)
			if !ok {
				return nil, t3Down()
			}
			in := t3code.LaunchInput{
				ProjectID: p.project.ID, Title: strings.TrimSpace(a.Title), ModelSelection: *p.project.DefaultModelSelection,
				Text: strings.TrimSpace(a.Text),
			}
			if in.Title == "" {
				in.Title, _ = headRunes(strings.Join(strings.Fields(in.Text), " "), t3TitleRunes)
				in.GenerateTitle = true
			}
			if a.Worktree {
				in.WorkspaceStrategy = t3code.WorkspaceStrategy{Type: t3code.WorkspaceWorktree, BaseRef: t3WorktreeBase}
			}
			res, err := client.LaunchThread(ctx, in)
			if err != nil {
				return nil, t3Refusal(err)
			}
			return encode(map[string]any{"instance": p.instance, "thread": res.ThreadID, "project": p.project.ID, "started": true})
		},
	}
}

const t3SendInput = `{
	"type": "object",
	"properties": {
		"thread": {"type": "string", "minLength": 1, "maxLength": 200},
		"text": {"type": "string", "minLength": 1, "maxLength": 8000},
		"mode": {"enum": ["auto", "queue", "steer"]},
		` + t3InstanceProp + `
	},
	"required": ["thread", "text"],
	"additionalProperties": false
}`

// t3Send sends the user's words to a thread: auto starts a run when it is
// idle and queues after the running one otherwise; queue always waits;
// steer turns the running one.
func t3Send(d T3Deps) agent.Tool {
	return agent.Tool{
		Name: "t3_send",
		Description: "Send a message to a T3 Code thread (its id or title), from what the user said. mode auto (default) " +
			"starts work when the thread is idle and waits after its current work otherwise; queue always waits; steer " +
			"redirects the work running now. Only on the user's request.",
		Input:   t3SendInput,
		Effects: agent.Write, Output: agent.Trusted,
		RequiresWokenSession: true, Timeout: t3Timeout,
		Available: t3Available,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct{ Thread, Text, Mode, Instance string }
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			th, e := d.thread(a.Instance, a.Thread)
			if e != nil {
				return nil, e
			}
			running := ""
			if th.shell.ActiveRunID != nil {
				running = *th.shell.ActiveRunID
			}
			mode := t3code.DispatchMode{Type: t3code.DispatchQueueAfterActive}
			switch a.Mode {
			case "", "auto":
				if running == "" {
					mode.Type = t3code.DispatchStartImmediately
				}
			case "steer":
				if running == "" {
					return nil, &agent.ToolError{Code: "not_running", Message: "The thread runs nothing to steer.", Hint: "Send it with mode auto."}
				}
				mode = t3code.DispatchMode{Type: t3code.DispatchSteerActive, TargetRunID: running}
			}
			client, ok := d.Source.Client(th.instance)
			if !ok {
				return nil, t3Down()
			}
			if _, err := client.DispatchCommand(ctx, t3code.DispatchMessage{ThreadID: th.shell.ID, Text: strings.TrimSpace(a.Text), DispatchMode: mode}, ""); err != nil {
				return nil, t3Refusal(err)
			}
			return encode(map[string]any{"instance": th.instance, "thread": th.shell.ID, "mode": mode.Type, "sent": true})
		},
	}
}

const t3StopInput = `{
	"type": "object",
	"properties": {
		"thread": {"type": "string", "minLength": 1, "maxLength": 200},
		` + t3InstanceProp + `
	},
	"required": ["thread"],
	"additionalProperties": false
}`

// t3Stop stops a thread's work (thread.stop: its run and its delegated
// tasks).
func t3Stop(d T3Deps) agent.Tool {
	return agent.Tool{
		Name:        "t3_stop",
		Description: "Stop a T3 Code thread's work (its id or title). Only on the user's request.",
		Input:       t3StopInput,
		Effects:     agent.Write, Output: agent.Trusted,
		RequiresWokenSession: true, Timeout: t3Timeout,
		Available: t3Available,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct{ Thread, Instance string }
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			th, e := d.thread(a.Instance, a.Thread)
			if e != nil {
				return nil, e
			}
			client, ok := d.Source.Client(th.instance)
			if !ok {
				return nil, t3Down()
			}
			if _, err := client.DispatchCommand(ctx, t3code.StopThread{ThreadID: th.shell.ID, Reason: "stopped by voice"}, ""); err != nil {
				return nil, t3Refusal(err)
			}
			return encode(map[string]any{"instance": th.instance, "thread": th.shell.ID, "stopped": true})
		},
	}
}
