package deepgram

import (
	"context"
	"net/http"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// probe reads the key's own details (GET /v1/auth/token), the call Deepgram
// documents as the test of a key: details for a valid one, invalid credentials
// otherwise, under no project. A key whose scopes refuse it would answer auth;
// that no scope does is not verified (an owner check). No audio is sent and
// nothing is synthesized, so nothing is billed.
func probe(ctx context.Context, v any) error {
	var base, key string
	var client *http.Client
	switch d := v.(type) {
	case *flux:
		base, key, client = d.cfg.BaseURL, d.key, d.client
	case *aura:
		base, key, client = d.base, d.key, d.client
	default:
		return providers.ProbeBadResponse
	}
	_, err := providers.ProbeGET(ctx, client, restBase(base)+"/v1/auth/token", http.Header{"Authorization": {"Token " + key}})
	return err
}

// restBase is the websocket base's https (or http) twin.
func restBase(base string) string {
	base = strings.TrimRight(base, "/")
	if rest, ok := strings.CutPrefix(base, "wss://"); ok {
		return "https://" + rest
	}
	if rest, ok := strings.CutPrefix(base, "ws://"); ok {
		return "http://" + rest
	}
	return base
}
