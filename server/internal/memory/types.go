// Package memory stores long-term memories with embeddings, conversation
// compactions and learning runs.
package memory

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	ScopeGlobal = "global"
	ScopeDevice = "device"

	KindPreference = "preference"
	KindFact       = "fact"
	KindAlias      = "alias"
	KindLesson     = "lesson"

	SourceLearned = "learned"
	SourceManual  = "manual"

	StatusActive  = "active"
	StatusPending = "pending"

	ProposalUpdate = "update"
	ProposalDelete = "delete"
)

var (
	ErrNotFound = errors.New("memory: not found")
	ErrInvalid  = errors.New("memory: invalid")
	// ErrDimension: the query vector and a stored vector of the same model differ in size.
	ErrDimension = errors.New("memory: vector dimension mismatch")
	// ErrChanged: the memory is gone or no longer as it was read.
	ErrChanged = errors.New("memory: changed since read")
)

type Memory struct {
	ID           string
	Scope        string
	DeviceID     string // empty for global
	Kind         string
	Text         string
	EmbedModel   string // empty without an embedding
	HasEmbedding bool
	Pinned       bool
	Hits         int
	LastHitAt    *time.Time
	Confidence   float32
	Evidence     []int64 // conversation message ids
	Source       string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// Replaces is set on a pending proposal to change the active memory it
	// names (approval mode), with Proposal saying how: approving an "update"
	// writes the proposal's text, kind, confidence and evidence onto that
	// memory and deletes the proposal; approving a "delete" deletes that
	// memory, and with it the proposal. A pending memory without it is a new
	// one: approving it makes it active.
	Replaces string
	Proposal string
}

type Scored struct {
	Memory
	Similarity float64
}

// NewMemory carries Vector and EmbedModel together or not at all.
type NewMemory struct {
	Scope      string
	DeviceID   string
	Kind       string
	Text       string
	Vector     []float32
	EmbedModel string
	Pinned     bool
	Confidence float32
	Evidence   []int64
	Source     string
	Status     string
	// Replaces and Proposal make a pending proposal; see Memory.
	Replaces string
	Proposal string
}

// Patch changes only the set fields. A Scope change takes DeviceID with it.
// A new Text drops the embedding: re-embedding is the caller's job.
type Patch struct {
	Text       *string
	Kind       *string
	Scope      *string
	DeviceID   string
	Pinned     *bool
	Status     *string
	Source     *string
	Confidence *float32
	Evidence   []int64
}

// Filter fields left zero match everything.
type Filter struct {
	Scope    string
	DeviceID string
	Kind     string
	Status   string
	Pinned   *bool
}

// Scopes selects the memories a device may see: the global ones, its own, or both.
type Scopes struct {
	Global   bool
	DeviceID string
}

type NewCompaction struct {
	DeviceID      string
	UptoMessageID int64
	Summary       string
	OpenThreads   []byte // json, default []
	TaskRefs      []byte // json, default []
	Model         string
}

type Compaction struct {
	ID            string
	DeviceID      string
	UptoMessageID int64
	Summary       string
	OpenThreads   []byte
	TaskRefs      []byte
	Model         string
	CreatedAt     time.Time
}

// Threads decodes the open threads; a value that is not a list of strings is none.
func (c Compaction) Threads() []string {
	var out []string
	if json.Unmarshal(c.OpenThreads, &out) != nil {
		return nil
	}
	return out
}

type NewRun struct {
	CompactionID string
	DeviceID     string
	Applied      int
	Dropped      int
	Pending      int
	MemoryIDs    []string
}

type Run struct {
	ID           string
	CompactionID string
	DeviceID     string
	Applied      int
	Dropped      int
	Pending      int
	MemoryIDs    []string
	CreatedAt    time.Time
}
