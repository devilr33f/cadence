package music

import (
	"math/rand/v2"
	"slices"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// Repeat is what the queue does at its end, or when a song ends: off stops
// (or tops the queue up with radio, when the queue started from one song),
// all starts the queue again, one plays the song again.
type Repeat string

const (
	RepeatOff Repeat = "off"
	RepeatAll Repeat = "all"
	RepeatOne Repeat = "one"
)

func ValidRepeat(r Repeat) bool { return r == RepeatOff || r == RepeatAll || r == RepeatOne }

const (
	// MaxQueue is the most songs a queue holds; radio stops topping it up there
	MaxQueue = 200
	// a song ended by itself when its last known position was this close
	// to its end
	endSlack = 8 * time.Second
)

// queue is one device's songs: items in the order they were added, order
// the play order (items' indexes, shuffled or not), pos the playing one's
// place in order. A queue whose session the device no longer plays stays,
// so "play the next one" still works once it ended.
type queue struct {
	items   []providers.Track
	order   []int
	pos     int
	shuffle bool
	repeat  Repeat
	// radio tops the queue up from the playing song's radio at its end
	radio bool
	// radioStarted is whether the station's radioStarted event was sent
	radioStarted bool
	// previous is the displaced song's skip, sent once the new queue starts
	previous *providers.ListeningEvent
	// the collection this queue started from, when there was one
	name       string
	artworkURL string
	// started is when the playing song's play command was sent
	started time.Time
	// last is the device's session while it played one of these songs
	last lastSeen
	// shown is the title a screen's player shows for the playing song (its video's), "" when a url session plays it
	// under the song's own
	shown string
}

type lastSeen struct {
	ours       bool
	playing    bool
	positionMs uint64
	at         time.Time
	durationMs uint64
}

func newQueue(tracks []providers.Track, start int, shuffle bool, repeat Repeat, o Options) *queue {
	q := &queue{items: slices.Clone(tracks), shuffle: shuffle, repeat: repeat, radio: o.Radio, name: o.Name, artworkURL: o.ArtworkURL}
	if len(q.items) > MaxQueue {
		q.items = q.items[:MaxQueue]
	}
	start = min(max(start, 0), len(q.items)-1)
	q.order = make([]int, len(q.items))
	for i := range q.order {
		q.order[i] = i
	}
	if shuffle {
		q.reshuffle(start)
	} else {
		q.pos = start
	}
	return q
}

func (q *queue) current() (providers.Track, bool) {
	if q == nil || len(q.order) == 0 {
		return providers.Track{}, false
	}
	return q.items[q.order[q.pos]], true
}

func (q *queue) setCurrent(t providers.Track) {
	if q != nil && len(q.order) > 0 {
		q.items[q.order[q.pos]] = t
	}
}

// reshuffle puts the item at index keep first and the rest in random order
func (q *queue) reshuffle(keep int) {
	rest := make([]int, 0, len(q.items))
	for i := range q.items {
		if i != keep {
			rest = append(rest, i)
		}
	}
	// a play order is no secret
	rand.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] }) //nolint:gosec // see above
	q.order = append([]int{keep}, rest...)
	q.pos = 0
}

// setShuffle keeps the playing song and reorders what follows it
func (q *queue) setShuffle(on bool) {
	if q.shuffle == on || len(q.order) == 0 {
		q.shuffle = on
		return
	}
	cur := q.order[q.pos]
	q.shuffle = on
	if on {
		q.reshuffle(cur)
		return
	}
	for i := range q.order {
		q.order[i] = i
	}
	q.pos = cur
}

// step moves to the next (+1) or previous (-1) song; at an end, repeat all
// wraps around, else ok is false and nothing moves
func (q *queue) step(dir int) bool {
	if len(q.order) == 0 {
		return false
	}
	next := q.pos + dir
	switch {
	case next >= 0 && next < len(q.order):
	case q.repeat == RepeatAll && next >= len(q.order):
		if q.shuffle {
			q.reshuffle(q.order[rand.IntN(len(q.order))]) //nolint:gosec // a play order is no secret
			return true
		}
		next = 0
	case q.repeat == RepeatAll && next < 0:
		next = len(q.order) - 1
	default:
		return false
	}
	q.pos = next
	return true
}

// add appends tracks after the playing song (next) or at the end; it
// answers how many fit
func (q *queue) add(tracks []providers.Track, next bool) int {
	room := MaxQueue - len(q.items)
	if room <= 0 {
		return 0
	}
	if len(tracks) > room {
		tracks = tracks[:room]
	}
	at := len(q.order)
	if next {
		at = q.pos + 1
	}
	idx := make([]int, len(tracks))
	for i, t := range tracks {
		q.items = append(q.items, t)
		idx[i] = len(q.items) - 1
	}
	q.order = slices.Insert(q.order, at, idx...)
	return len(tracks)
}

// upcoming and history are the songs after and before the playing one, in
// play order, at most n each
func (q *queue) upcoming(n int) []providers.Track {
	var out []providers.Track
	for i := q.pos + 1; i < len(q.order) && len(out) < n; i++ {
		out = append(out, q.items[q.order[i]])
	}
	return out
}

func (q *queue) history(n int) []providers.Track {
	var out []providers.Track
	for i := q.pos - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, q.items[q.order[i]])
	}
	return out
}

// endedByItself says whether the session that just went away was this
// queue's song playing to its end, not stopped or replaced half way
func (q *queue) endedByItself(now time.Time) bool {
	l := q.last
	if !l.ours || !l.playing || l.durationMs == 0 {
		return false
	}
	pos := msDuration(l.positionMs) + now.Sub(l.at)
	return pos >= msDuration(l.durationMs)-endSlack
}

// msDuration is ms milliseconds, a day at most: no song is longer
func msDuration(ms uint64) time.Duration {
	return time.Duration(min(ms, uint64(24*time.Hour/time.Millisecond))) * time.Millisecond //nolint:gosec // bounded above
}
