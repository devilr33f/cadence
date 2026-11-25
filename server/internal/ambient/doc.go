// Package ambient serves a screen's idle backgrounds (tv plan item 15): Apple's Aerial clips from the manifest Apple
// publishes (resources-16.tar: entries.json and the TVIdleScreenStrings captions), fetched weekly from an allow-listed
// host, with the rendition chosen per device profile; and images, from the owner's list or the catalog's artwork.
// It answers the device's rpc ambient.next. The screen streams a clip from Apple's cdn itself: nothing is re-hosted,
// and nothing of Apple's is committed (the tests build a synthetic tar).
package ambient
