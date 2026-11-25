package agent

import (
	"reflect"
	"testing"
)

func TestEnabledToolsIntersect(t *testing.T) {
	cases := []struct {
		mode, device, want []string
	}{
		{nil, nil, nil},
		{nil, []string{"weather"}, []string{"weather"}},
		{[]string{"weather", "web_search"}, nil, []string{"weather", "web_search"}},
		{[]string{"weather", "web_search"}, []string{"web_search", "device_volume"}, []string{"web_search"}},
		{[]string{"weather"}, []string{"device_volume"}, []string{}},
		{[]string{}, nil, []string{}},
		{nil, []string{}, []string{}},
	}
	for _, c := range cases {
		got := enabledTools(c.mode, c.device)
		// nil offers every tool and an empty list none, so the two must not mix
		if !reflect.DeepEqual(got, c.want) || (got == nil) != (c.want == nil) {
			t.Errorf("enabledTools(%#v, %#v) = %#v, want %#v", c.mode, c.device, got, c.want)
		}
	}
}
