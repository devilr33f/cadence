package t3code

// ShellUpdate is what the shell alone tells about a thread: a run that
// stopped (Update.Kind stopped, plus error for a failed one, ErrorCode from
// lastErrorCode), or a new pending request (Kind question or approval with
// RequestID and NeedsDetail: its prompt is read with GetThreadProjection and
// PendingFromProjection, or by following the thread).
type ShellUpdate struct {
	Update
	ProjectID   string
	NeedsDetail bool
}

// ShellTracker turns subscribeShell's items into ShellUpdates and keeps the
// projects and thread shells it saw. A snapshot sets the baseline (nothing
// already ended or pending is told). Not safe for concurrent use.
type ShellTracker struct {
	Projects map[string]Project
	Threads  map[string]ThreadShell
}

func NewShellTracker() *ShellTracker {
	return &ShellTracker{Projects: map[string]Project{}, Threads: map[string]ThreadShell{}}
}

func (s *ShellTracker) Apply(it ShellItem) []ShellUpdate {
	switch it.Kind {
	case ShellSnapshotKind:
		if it.Snapshot == nil {
			return nil
		}
		s.Projects = map[string]Project{}
		for _, p := range it.Snapshot.Projects {
			s.Projects[p.ID] = p
		}
		old := s.Threads
		s.Threads = map[string]ThreadShell{}
		var out []ShellUpdate
		for _, th := range it.Snapshot.Threads {
			if prev, ok := old[th.ID]; ok {
				out = append(out, diffShell(prev, th)...)
			}
			s.Threads[th.ID] = th
		}
		return out
	case ShellProjectUpdated:
		if it.Project != nil {
			s.Projects[it.Project.ID] = *it.Project
		}
	case ShellProjectRemoved:
		delete(s.Projects, it.ProjectID)
	case ShellThreadUpdated:
		if it.Thread == nil {
			return nil
		}
		if it.Location == "archive" {
			delete(s.Threads, it.Thread.ID)
			return nil
		}
		prev, ok := s.Threads[it.Thread.ID]
		s.Threads[it.Thread.ID] = *it.Thread
		if !ok {
			// a new thread: a request it opened at once is still news
			prev = ThreadShell{ID: it.Thread.ID, Status: "idle"}
		}
		return diffShell(prev, *it.Thread)
	case ShellThreadRemoved:
		delete(s.Threads, it.ThreadID)
	}
	return nil
}

func diffShell(prev, cur ThreadShell) []ShellUpdate {
	base := Update{ThreadID: cur.ID, Title: cur.Title}
	var out []ShellUpdate
	run := deref(cur.LatestRunID)
	if Terminal(cur.Status) && (cur.Status != prev.Status || run != deref(prev.LatestRunID)) {
		u := base
		u.Kind, u.RunID, u.Status = KindStopped, run, cur.Status
		out = append(out, ShellUpdate{Update: u, ProjectID: cur.ProjectID})
		if cur.Status == RunFailed {
			e := u
			e.Kind, e.ErrorCode, e.ErrorClass = KindError, deref(cur.LastErrorCode), deref(cur.LastErrorClass)
			out = append(out, ShellUpdate{Update: e, ProjectID: cur.ProjectID})
		}
	}
	if p := cur.PendingRuntimeRequest; p != nil && (prev.PendingRuntimeRequest == nil || prev.PendingRuntimeRequest.ID != p.ID) {
		u := base
		u.RequestID, u.RunID = p.ID, run
		switch {
		case p.Kind == RequestUserInput:
			u.Kind = KindQuestion
		case ApprovalKind(p.Kind):
			u.Kind = KindApproval
		default:
			return out
		}
		out = append(out, ShellUpdate{Update: u, ProjectID: cur.ProjectID, NeedsDetail: true})
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
