package agent

import (
	"strings"
	"testing"
)

// the rules the owner's turns showed missing: claimed actions with no tool
// call, a follow-up refused before any tool said wake_required, made-up refs,
// internals in speech and misheard titles
func TestSystemPromptRules(t *testing.T) {
	p := systemPrompt("", "ru", InterimDrop)
	for _, want := range []string{
		"You are Cadence", "Reply in Russian.", "unless its tool answered ok in this turn",
		"Only when a tool answered wake_required", "catalog_search", "never made-up ones",
		"Never say ids, refs, error codes", "misheard", noTextWithTools, citeRule, retryRule,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	// the old rule made the model refuse follow-ups on its own
	if strings.Contains(p, "wake you first") {
		t.Error("prompt still refuses before a tool did")
	}
	if strings.Contains(systemPrompt("", "ru", InterimSpeak), noTextWithTools) {
		t.Error("a speak mode asks for no text")
	}
	if strings.Contains(p, personaHead) {
		t.Error("no persona still frames one")
	}
}

// a persona is the owner's words for one speaker: it comes last, after every
// rule, so it may change the tone and the length, and it never replaces the
// identity or the rules that keep tools honest
func TestSystemPromptPersona(t *testing.T) {
	persona := "Тебя зовут Ада. Говори с бабушкой медленно и тепло, шути, если просят — расскажи анекдот."
	got := systemPrompt("  "+persona+"\n", "ru", InterimDrop)
	if !strings.HasPrefix(got, "You are Cadence") {
		t.Errorf("persona replaced the identity: %q", got)
	}
	if !strings.HasSuffix(got, personaHead+persona) {
		t.Errorf("persona not last, or not trimmed: %q", got)
	}
	if strings.Index(got, retryRule) > strings.Index(got, persona) {
		t.Error("a rule follows the persona")
	}
	if systemPrompt(" \n ", "ru", InterimDrop) != systemPrompt("", "ru", InterimDrop) {
		t.Error("a blank persona changed the prompt")
	}
}
