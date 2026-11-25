package catalog

// GroupKind is a choice group's kind. The UI renders whatever groups an item
// has, the same before playing and as the player's switcher.
type GroupKind string

const (
	// GroupSource picks a title group's variant: a torrplayer source
	GroupSource GroupKind = "source"
	// GroupVoiceover picks a stream's translator (voice, sub or raw)
	GroupVoiceover GroupKind = "voiceover"
	GroupQuality   GroupKind = "quality"
	// GroupTorrent picks a torrent that covers the episode
	GroupTorrent       GroupKind = "torrent"
	GroupAudioLanguage GroupKind = "audio_language"
	GroupSubtitles     GroupKind = "subtitles"
	// GroupFile picks a file inside a torrent
	GroupFile GroupKind = "file"
)

// Selection is the choice made per group, by Choice.ID. A group left out takes
// the remembered choice (ChoiceMemory), then the provider's default.
type Selection map[GroupKind]string

// Merge answers s with every group it leaves out taken from fallback.
func (s Selection) Merge(fallback Selection) Selection {
	out := Selection{}
	for k, v := range fallback {
		out[k] = v
	}
	for k, v := range s {
		out[k] = v
	}
	return out
}

type OptionsRequest struct {
	Key Key
	// Episode is the label of the episode to play, "" for an item without
	Episode   string
	Selection Selection
}

// Options are an item's choice groups for one episode, in the order the UI
// shows them, consistent with the Selection asked with.
type Options struct {
	Groups []ChoiceGroup
	// Selected is what Resolve would use now: the request's selection, then
	// the remembered one, then the defaults
	Selected Selection
	// Remembered marks the groups whose choice came from the title's
	// remembered one, set by the catalog service (never by a provider)
	Remembered []GroupKind
}

type ChoiceGroup struct {
	Kind    GroupKind
	Label   string
	Choices []Choice
	// Hidden counts choices the owner's filters hid ("12 more hidden")
	Hidden int
}

// Choice is one option. ID is the provider's, stable within the item. A
// choice that cannot play this episode is listed with Unavailable and a
// Reason code, so the UI says why a voiceover is missing.
type Choice struct {
	ID          string
	Label       string
	Detail      string
	Badges      []Badge
	Unavailable bool
	Reason      FailureCode
	// one of these describes what the choice is
	Stream  *StreamSource
	Torrent *TorrentSource
}

// TransType is a stream's voiceover kind.
type TransType string

const (
	TransVoice TransType = "voice"
	TransSub   TransType = "sub"
	TransRaw   TransType = "raw"
)

// StreamSource is a streaming site's offer for an episode.
type StreamSource struct {
	Translator string
	TransType  TransType
	Language   string
	// Quality is the vertical resolution: 480, 720, 1080
	Quality int
}

// TorrentSource is a torrent covering an episode.
type TorrentSource struct {
	// Episodes is the range it covers ("1-12"), "" for the whole title
	Episodes  string
	SizeBytes int64
	Seeders   int
	Leechers  int
	Quality   int
	Codec     string
	Hardsub   bool
}
