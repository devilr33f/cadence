package catalog

import "time"

// SetSearchTimeout shortens a provider's search budget for a test.
func SetSearchTimeout(d time.Duration) (restore func()) {
	old := searchTimeout
	searchTimeout = d
	return func() { searchTimeout = old }
}

// SameTitle is how a found item matches a title by its ids and names, "" when it does not.
func SameTitle(ids ExternalIDs, names []string, it Item) string { return sameTitle(ids, names, it) }
