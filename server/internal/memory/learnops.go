package memory

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
)

const (
	maxOps = 10
	// maxEvidence bounds the message ids a memory keeps, the newest
	maxEvidence = 50
)

// why an op was dropped: counted and logged, never with the op's text
const (
	dropSchema    = "schema"
	dropLimit     = "limit"
	dropSecret    = "secret"
	dropScope     = "scope"
	dropAlias     = "alias"
	dropManual    = "manual"
	dropPinned    = "pinned"
	dropEvidence  = "evidence"
	dropUntrusted = "untrusted"
	dropCap       = "cap"
	dropChanged   = "changed"
	dropStore     = "store"
)

// extra fields are ignored; scope and kind are the memory's own on update
var opSchema = mustCompile(`{"type":"object","required":["op"],"properties":{` +
	`"op":{"enum":["add","update","delete"]},"id":{"type":"string"},` +
	`"scope":{"enum":["global","device"]},"kind":{"enum":["preference","fact","alias","lesson"]},` +
	`"text":{"type":"string","maxLength":300,"pattern":"\\S"},` +
	`"evidence":{"type":"array","items":{"type":"integer","minimum":1}},` +
	`"confidence":{"type":"number","minimum":0,"maximum":1}},` +
	`"allOf":[` +
	`{"if":{"properties":{"op":{"const":"add"}}},"then":{"required":["scope","kind","text","evidence","confidence"]}},` +
	`{"if":{"properties":{"op":{"const":"update"}}},"then":{"required":["id","text","evidence","confidence"]}},` +
	`{"if":{"properties":{"op":{"const":"delete"}}},"then":{"required":["id"]}}]}`)

// plannedOp is an op that passed the checks; target is the memory an update
// or a delete names.
type plannedOp struct {
	op       string
	target   *Memory
	scope    string
	kind     string
	text     string
	evidence []int64
	conf     float32
}

// check keeps the first maxOps ops that pass, in order, and counts the rest
// as dropped.
func (r *run) check(raw []json.RawMessage, known []Memory) []plannedOp {
	var plan []plannedOp
	for i, item := range raw {
		if i >= maxOps {
			r.drop(dropLimit)
			continue
		}
		p, reason := r.checkOne(item, known)
		if reason != "" {
			r.drop(reason)
			continue
		}
		plan = append(plan, p)
	}
	return plan
}

func (r *run) checkOne(item json.RawMessage, known []Memory) (plannedOp, string) {
	if _, ok := jsonschemax.Check(opSchema, item); !ok {
		return plannedOp{}, dropSchema
	}
	var o struct {
		Op         string    `json:"op"`
		ID         string    `json:"id"`
		Scope      string    `json:"scope"`
		Kind       string    `json:"kind"`
		Text       string    `json:"text"`
		Evidence   []float64 `json:"evidence"`
		Confidence float64   `json:"confidence"`
	}
	if json.Unmarshal(item, &o) != nil {
		return plannedOp{}, dropSchema
	}
	p := plannedOp{op: o.Op, scope: o.Scope, kind: o.Kind, text: strings.TrimSpace(o.Text), conf: float32(o.Confidence)}
	if o.Op != "delete" && looksSecret(p.text) {
		return plannedOp{}, dropSecret
	}
	if o.Op == "add" && !r.allowed(o.Scope) {
		return plannedOp{}, dropScope
	}
	if o.Op != "add" {
		// the model saw only memories of the allowed scopes
		m, ok := byAlias(o.ID, known)
		if !ok {
			return plannedOp{}, dropAlias
		}
		if reason := untouchable(m); reason != "" {
			return plannedOp{}, reason
		}
		p.target = &m
	}
	p.evidence = r.inSegment(o.Evidence)
	if len(p.evidence) == 0 {
		return plannedOp{}, dropEvidence
	}
	// tool results and the assistant's own words can be steered by others:
	// a change needs the user's word
	if !slices.ContainsFunc(p.evidence, func(id int64) bool { return r.seg[id] == "user" }) {
		return plannedOp{}, dropUntrusted
	}
	return p, ""
}

// byAlias maps "m<n>" back to the nth memory the model saw.
func byAlias(alias string, known []Memory) (Memory, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(alias, "m"))
	if err != nil || n < 1 || n > len(known) || alias != "m"+strconv.Itoa(n) {
		return Memory{}, false
	}
	return known[n-1], true
}

// inSegment keeps the ids of messages the model saw, once each, in order,
// the newest maxEvidence of them.
func (r *run) inSegment(ids []float64) []int64 {
	var in []int64
	for _, f := range ids {
		if id := int64(f); float64(id) == f && r.seg[id] != "" {
			in = append(in, id)
		}
	}
	return mergeEvidence(nil, in)
}

// mergeEvidence is the union of both, in order, the newest maxEvidence of it.
func mergeEvidence(a, b []int64) []int64 {
	out := slices.Concat(a, b)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		out = []int64{}
	}
	return out[max(0, len(out)-maxEvidence):]
}
