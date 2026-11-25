package tools

import (
	"strconv"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	// a result stays playable this long, so a follow-up ("the second one")
	// still finds it
	musicResultTTL = 30 * time.Minute
	// a device keeps at most this many results, oldest dropped first
	maxMusicResults = 40
)

// musicResults are the songs and lists music_search found, per device, by the
// short id the model sees. music_play takes only these ids: the model never
// names a url, so an instruction in a title can at worst pick another result.
type musicResults struct {
	mu     sync.Mutex
	now    func() time.Time
	device map[string]*deviceResults
}

type deviceResults struct {
	next  int
	items []storedResult
}

type storedResult struct {
	id   string
	at   time.Time
	item found
}

// found is one search result: a song, or an album, a playlist or an artist
// (coll) with the provider instance that reads its songs
type found struct {
	track         providers.Track
	coll          *providers.Collection
	instance      string
	favoriteQuery string
}

func (f found) source() string {
	if f.coll != nil {
		return f.coll.Source
	}
	return f.track.Source
}

func newMusicResults(now func() time.Time) *musicResults {
	if now == nil {
		now = time.Now
	}
	return &musicResults{now: now, device: map[string]*deviceResults{}}
}

// known is whether the device's ids were seeded in this process
func (r *musicResults) known(deviceID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.device[deviceID] != nil
}

// seed starts the device's ids after n, the highest its history holds, unless they already started
func (r *musicResults) seed(deviceID string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.device[deviceID] == nil {
		r.device[deviceID] = &deviceResults{next: n}
	}
}

// add keeps items for the device and answers their ids, in order
func (r *musicResults) add(deviceID string, items []found) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	d := r.device[deviceID]
	if d == nil {
		d = &deviceResults{}
		r.device[deviceID] = d
	}
	r.prune(d, now)
	ids := make([]string, len(items))
	for i, it := range items {
		d.next++
		ids[i] = "r" + strconv.Itoa(d.next)
		d.items = append(d.items, storedResult{id: ids[i], at: now, item: it})
	}
	if over := len(d.items) - maxMusicResults; over > 0 {
		d.items = d.items[over:]
	}
	return ids
}

func (r *musicResults) get(deviceID, id string) (found, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.device[deviceID]
	if d == nil {
		return found{}, false
	}
	r.prune(d, r.now())
	for _, it := range d.items {
		if it.id == id {
			return it.item, true
		}
	}
	return found{}, false
}

func (r *musicResults) prune(d *deviceResults, now time.Time) {
	i := 0
	for i < len(d.items) && now.Sub(d.items[i].at) > musicResultTTL {
		i++
	}
	d.items = d.items[i:]
}
