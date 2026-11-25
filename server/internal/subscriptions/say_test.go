package subscriptions

import "testing"

func item(event, kind, title string) InboxItem {
	return InboxItem{Event: event, Kind: kind, Title: title}
}

func TestComposeSingle(t *testing.T) {
	q := item(EventQuestion, "thread", "Fix login")
	q.Question = &Question{Kind: QuestionClarify, Token: "t1", Text: "Which\nbranch?"}
	appr := item(EventApproval, "thread", "Deploy")
	appr.Question = &Question{Kind: QuestionApproval, Token: "t2", Text: "Run kubectl apply?"}
	for _, c := range []struct {
		it       InboxItem
		lang, is string
	}{
		{item(EventStopped, "thread", "Fix login"), "ru", "Тред „Fix login“ остановился."},
		{item(EventStopped, "thread", "Fix login"), "en", "Thread “Fix login” stopped."},
		{q, "ru", "Тред „Fix login“ спрашивает: Which branch?"},
		{q, "en", "Thread “Fix login” asks: Which branch?"},
		{appr, "ru", "Тред „Deploy“ просит разрешения: Run kubectl apply?"},
		{item(EventError, "thread", "X"), "ru", "Тред „X“ сообщает об ошибке."},
		{item(EventDone, "task", "Backups"), "ru", "Задача „Backups“ завершена."},
		{item(EventStopped, "thread", ""), "ru", "Тред остановился."},
		{item(EventChanged, "lamp", "Hall"), "en", "Update “Hall” changed."},
	} {
		if got := compose([]InboxItem{c.it}, c.lang); got != c.is {
			t.Fatalf("%s %s: %q, want %q", c.lang, c.it.Event, got, c.is)
		}
	}
}

// several updates are counted by event, an event of one naming its title;
// a question's text is said only alone
func TestComposeCoalesced(t *testing.T) {
	q := item(EventQuestion, "thread", "Login")
	q.Question = &Question{Kind: QuestionClarify, Token: "t", Text: "SECRET-ish long text"}
	items := []InboxItem{item(EventStopped, "thread", "A"), item(EventStopped, "thread", "B"), q, item(EventStopped, "thread", "C")}
	if got := compose(items, "ru"); got != "Три треда остановились, тред „Login“ спрашивает." {
		t.Fatalf("ru %q", got)
	}
	if got := compose(items, "en"); got != "Three threads stopped, thread “Login” asks." {
		t.Fatalf("en %q", got)
	}
	var many []InboxItem
	for range 12 {
		many = append(many, item(EventStopped, "thread", "x"))
	}
	if got := compose(many, "ru"); got != "12 тредов остановились." {
		t.Fatalf("many %q", got)
	}
	mixed := []InboxItem{item(EventDone, "thread", "a"), item(EventDone, "task", "b")}
	if got := compose(mixed, "ru"); got != "Два обновления завершены." {
		t.Fatalf("mixed %q", got)
	}
}

func TestClip(t *testing.T) {
	long := ""
	for range 300 {
		long += "я"
	}
	if got := []rune(clip(long, maxSaidText)); len(got) != maxSaidText+1 || got[maxSaidText] != '…' {
		t.Fatalf("%d", len(got))
	}
	if got := clip(" a\n\tb\x00c ", 10); got != "a b c" {
		t.Fatalf("%q", got)
	}
}
