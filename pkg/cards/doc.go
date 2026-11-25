// Package cards is Cadence Cards, vocabulary version 1 (TV design r2 section
// 17), over the protobuf messages of proto/cadence/cards/v1 (Go in
// pkg/gen/cadence/cards/v1, C++ through nanopb in media/gen): a closed set of
// display primitives the server sends and a screen draws from its own
// tokens.
//
// Two uses share the vocabulary: a tool result is one Card pushed into the
// assistant drawer, and a media or task panel is a Page of cards with a next
// action.
//
// The server builds cards with the generated types and the small builders
// here (N wraps a primitive in a Node, NewCard an envelope, Act an action),
// then encodes them with EncodeCard or EncodePage, which fill in the card's
// features and check what the device will check: depth 4 below the card, 64
// nodes, 12 focusables, 16 KB a card and 128 KB a page encoded, every text
// length, required fields, ids, enum values, the accent rule, ranges, one
// level of post quote, and no unknown fields. DecodeCard and DecodePage check
// bytes from elsewhere the same way.
//
// Forward compatibility replaces the JSON rule "unknown field, fallback":
// protobuf skips unknown fields silently, so a card lists in features every
// primitive and optional field it uses (Features), and a device that lacks
// one draws the card's fallback_text with an "unavailable on this screen"
// meta line. A change of an existing field's meaning is vocabulary version 2;
// a screen advertises the versions it renders in its hello.
//
// Card text is localized and formatted by the server; the device formats only
// a countdown. Text from third parties (titles, posts, snippets, task output)
// is content, never the assistant's voice, and never reaches a log: an
// Error's Reason may quote it, its Code and Path never do.
package cards
