package sources

import "time"

// SetTailTimings sets how often a tail reads and how often it looks for a missing file; before Run.
func SetTailTimings(t *Tail, every, retry time.Duration) { t.every, t.retry = every, retry }

// TailOpened is whether the tail holds its file open.
func TailOpened(t *Tail) bool { return t.opened.Load() }

// TailOffset is how far into its file the tail read.
func TailOffset(t *Tail) int64 { return t.offset.Load() }
