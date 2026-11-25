package cards

// Version is the vocabulary version this package writes and reads.
const Version = 1

// Versions lists the vocabulary versions this package renders, as a screen's
// hello advertises its own.
var Versions = []uint32{Version}

// Device limits (design r2 section 17). Text lengths are per field, in
// fields.go.
const (
	// MaxDepth is the nesting depth below the card: its children are at 1,
	// and a post's quote is one level below the post.
	MaxDepth = 4
	// MaxNodes counts every primitive of a card, its root included.
	MaxNodes = 64
	// MaxFocusables counts what the remote can focus in one card.
	MaxFocusables = 12
	// MaxCardBytes and MaxPageBytes bound the encoded protobuf.
	MaxCardBytes = 16 << 10
	MaxPageBytes = 128 << 10
	// MaxPageCards is a panel page's cards; MaxListItems a paged list's.
	MaxPageCards = 20
	MaxListItems = 20
	// MaxDrawerCards is how many cards the drawer shows at once; the device
	// keeps the newest.
	MaxDrawerCards = 6
	// MaxTTLSeconds bounds a card's ttl_s.
	MaxTTLSeconds = 86400
)
