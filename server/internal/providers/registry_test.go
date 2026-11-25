package providers_test

import (
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	_ "git.shino.boo/devilreef/cadence/server/internal/providers/fake"
)

func TestFakesOnlyWithDevFakes(t *testing.T) {
	ids := []string{"fake-stt", "fake-llm", "fake-tts", "fake-embed"}
	off := providers.NewRegistry(false)
	on := providers.NewRegistry(true)
	for _, id := range ids {
		if _, ok := off.Module(id); ok {
			t.Errorf("%s present without devFakes", id)
		}
		if _, ok := on.Module(id); !ok {
			t.Errorf("%s missing with devFakes", id)
		}
	}
}

func TestExtraModulesAlwaysPresent(t *testing.T) {
	r := providers.NewRegistry(false, providers.Module{ID: "x", Kind: providers.KindSTT})
	if _, ok := r.Module("x"); !ok {
		t.Fatal("module x missing")
	}
}
