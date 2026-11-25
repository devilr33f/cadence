package mcp

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// maxLocalName is the longest tool name OpenAI-compatible providers take
	maxLocalName = 64
	// maxRemoteName is the spec's limit; a longer name is skipped
	maxRemoteName = 128
	maxPrefix     = 16
	hashHex       = 6
	// fallbackPrefix names an instance whose name has no letter to start one
	fallbackPrefix = "srv"
)

// DerivePrefix is the tool prefix of an instance without a toolPrefix: its
// name lowercased, other characters than [a-z0-9_] as _, starting at its
// first letter, at most 16 long.
func DerivePrefix(instance string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(instance) {
		switch {
		case b.Len() == 0 && (r < 'a' || r > 'z'):
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	p := strings.TrimRight(b.String()[:min(b.Len(), maxPrefix)], "_")
	if p == "" {
		return fallbackPrefix
	}
	return p
}

// validRemote is a remote name the catalog can keep and call back exactly:
// 1 to 128 code points of valid UTF-8 without control characters.
func validRemote(name string) bool {
	return name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= maxRemoteName &&
		strings.IndexFunc(name, unicode.IsControl) < 0
}

// sanitize keeps [A-Za-z0-9_-] and turns every other character into _.
func sanitize(remote string) string {
	var b strings.Builder
	for _, r := range remote {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// plainName is mcp_<prefix>_<sanitized remote>, empty when over 64.
func plainName(prefix, remote string) string {
	n := "mcp_" + prefix + "_" + sanitize(remote)
	if len(n) > maxLocalName {
		return ""
	}
	return n
}

// hashedName is the sanitized remote cut to fit 64 with _ and 6 hex of its
// sha256 after it: what a remote takes when its plain name is too long or
// taken.
func hashedName(prefix, remote string) string {
	sum := sha256.Sum256([]byte(remote))
	head := "mcp_" + prefix + "_"
	tool := sanitize(remote)
	room := maxLocalName - len(head) - 1 - hashHex
	return head + tool[:min(len(tool), room)] + "_" + hex.EncodeToString(sum[:])[:hashHex]
}

// assignNames gives each remote of an instance its local name. stored is the
// instance's stored names by remote, taken those the other instances hold. A
// name already stored for a remote stays while it is still one of its two
// forms under prefix; otherwise it takes its plain name, or its hashed one,
// whichever no other remote holds, of this instance or another, stored or in
// this listing. Clean names (their own sanitized form) go first, then by
// remote name, so the outcome does not depend on listing order. A remote left
// without a name maps to "".
func assignNames(prefix string, remotes []string, stored map[string]string, taken map[string]bool) map[string]string {
	owner := make(map[string]string, len(stored))
	for remote, local := range stored {
		owner[local] = remote
	}
	order := slices.Clone(remotes)
	slices.SortFunc(order, func(a, b string) int {
		ca, cb := sanitize(a) == a, sanitize(b) == b
		if ca != cb {
			if ca {
				return -1
			}
			return 1
		}
		return cmp.Compare(a, b)
	})
	free := func(local, remote string) bool {
		o, ok := owner[local]
		return local != "" && !taken[local] && (!ok || o == remote)
	}
	out := make(map[string]string, len(remotes))
	for _, remote := range order {
		plain, hashed := plainName(prefix, remote), hashedName(prefix, remote)
		pick := ""
		if s := stored[remote]; s != "" && (s == plain || s == hashed) {
			pick = s
		} else if free(plain, remote) {
			pick = plain
		} else if free(hashed, remote) {
			pick = hashed
		}
		// a name the remote leaves stays its own until the next listing: its row
		// holds it until this listing's write
		if pick != "" {
			owner[pick] = remote
		}
		out[remote] = pick
	}
	return out
}
