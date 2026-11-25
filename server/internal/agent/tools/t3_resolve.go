package tools

import (
	"errors"
	"slices"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// T3Deps are the T3 Code tools' collaborators. The tools log nothing: titles
// and texts reach only the model, the conversation store and the live feed.
type T3Deps struct {
	Source *t3code.Source
}

// t3Available offers the tools only while the t3code setting names an
// instance.
func t3Available(v settings.Values) bool { return len(v.T3Code) > 0 }

// T3Code are the tools that list and steer T3 Code threads: t3_threads reads
// (its titles are outside text, so it taints), t3_launch, t3_send and t3_stop
// act in a session the user began. A thread or project is named by its id or
// by its title; a name that fits several answers their ids, never acts.
func T3Code(d T3Deps) []agent.Tool {
	return []agent.Tool{t3Threads(d), t3Launch(d), t3Send(d), t3Stop(d)}
}

// t3Shell is one running instance's shell.
type t3Shell struct {
	instance string
	projects []t3code.Project
	threads  []t3code.ThreadShell
}

// shells are the running instances' shells, of one instance when named.
func (d T3Deps) shells(instance string) ([]t3Shell, *agent.ToolError) {
	names := d.Source.Instances()
	if instance != "" {
		if !slices.Contains(names, instance) {
			return nil, &agent.ToolError{Code: "unknown_instance", Message: "No T3 Code instance of that name is followed.", Candidates: names}
		}
		names = []string{instance}
	}
	var out []t3Shell
	for _, n := range names {
		p, t, ok := d.Source.Shell(n)
		if !ok {
			continue
		}
		// an id that could carry words (a hostile server's) never reaches the
		// model, not even as a refusal's candidate
		p = slices.DeleteFunc(p, func(x t3code.Project) bool { return !t3code.ValidID(x.ID) })
		t = slices.DeleteFunc(t, func(x t3code.ThreadShell) bool {
			return !t3code.ValidID(x.ID) || x.ProjectID != "" && !t3code.ValidID(x.ProjectID)
		})
		out = append(out, t3Shell{instance: n, projects: p, threads: t})
	}
	if len(out) == 0 {
		return nil, t3Down()
	}
	return out, nil
}

func t3Down() *agent.ToolError {
	return &agent.ToolError{Code: "t3_unavailable", Message: "T3 Code is not connected now.", Retryable: true}
}

// t3Ambiguous lists the ids that fit a name (ids, never titles: the
// refusal stays free of outside text).
func t3Ambiguous(what string, ids []string) *agent.ToolError {
	return &agent.ToolError{
		Code: "ambiguous", Message: "Several " + what + "s fit that name; nothing was done.",
		Hint: "Ask the user which one, or call t3_threads to see their titles, then name it by id.", Candidates: ids,
	}
}

func t3NotFound(what string) *agent.ToolError {
	return &agent.ToolError{
		Code: "not_found", Message: "No " + what + " fits that name.", Hint: "Call t3_threads to see what there is.",
	}
}

// fold is a name as matching compares it.
func fold(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// pick resolves a name among candidates: an id, else one title equal to it,
// else one title containing it. More than one is ambiguous.
func pick[T any](items []T, name string, id, title func(T) string) (T, []string, bool) {
	var zero T
	for _, it := range items {
		if id(it) == name {
			return it, nil, true
		}
	}
	want := fold(name)
	if want == "" {
		return zero, nil, false
	}
	for _, test := range []func(string) bool{
		func(t string) bool { return t == want },
		func(t string) bool { return strings.Contains(t, want) },
	} {
		var hits []T
		for _, it := range items {
			if test(fold(title(it))) {
				hits = append(hits, it)
			}
		}
		switch {
		case len(hits) == 1:
			return hits[0], nil, true
		case len(hits) > 1:
			ids := make([]string, len(hits))
			for i, h := range hits {
				ids[i] = id(h)
			}
			return zero, ids, false
		}
	}
	return zero, nil, false
}

// t3Thread is a thread found by name, with its instance.
type t3Thread struct {
	instance string
	shell    t3code.ThreadShell
}

func (d T3Deps) thread(instance, name string) (t3Thread, *agent.ToolError) {
	shells, e := d.shells(instance)
	if e != nil {
		return t3Thread{}, e
	}
	var all []t3Thread
	for _, s := range shells {
		for _, th := range s.threads {
			all = append(all, t3Thread{instance: s.instance, shell: th})
		}
	}
	got, ids, ok := pick(all, name, func(t t3Thread) string { return t.shell.ID }, func(t t3Thread) string { return t.shell.Title })
	switch {
	case ok:
		return got, nil
	case len(ids) > 0:
		return t3Thread{}, t3Ambiguous("thread", ids)
	}
	return t3Thread{}, t3NotFound("thread")
}

// t3Project is a project found by name, with its instance.
type t3Project struct {
	instance string
	project  t3code.Project
}

func (d T3Deps) project(instance, name string) (t3Project, *agent.ToolError) {
	shells, e := d.shells(instance)
	if e != nil {
		return t3Project{}, e
	}
	var all []t3Project
	for _, s := range shells {
		for _, p := range s.projects {
			if p.ArchivedAt == nil {
				all = append(all, t3Project{instance: s.instance, project: p})
			}
		}
	}
	got, ids, ok := pick(all, name, func(p t3Project) string { return p.project.ID }, func(p t3Project) string { return p.project.Title })
	switch {
	case ok:
		return got, nil
	case len(ids) > 0:
		return t3Project{}, t3Ambiguous("project", ids)
	}
	return t3Project{}, t3NotFound("project")
}

// t3Refusal is the model's view of a failed call: its code, never T3's text.
func t3Refusal(err error) *agent.ToolError {
	var te *t3code.Error
	if errors.As(err, &te) {
		switch te.Code {
		case t3code.CodeUnavailable, t3code.CodeTimeout:
			return t3Down()
		case t3code.CodeRefused:
			return &agent.ToolError{Code: "refused", Message: "T3 Code refused it.", Hint: "The thread may have moved on; call t3_threads to see it now."}
		case t3code.CodeForbidden, t3code.CodeAuthFailed:
			return &agent.ToolError{Code: "forbidden", Message: "Cadence's T3 Code token may not do that."}
		}
	}
	return failed("t3_failed")
}
