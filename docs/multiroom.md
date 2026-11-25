# Multiroom groups and stereo pairs

Cadence plays one source on several devices at once, sample-aligned: a **group** (speakers and screens in one or more
rooms) or a **stereo pair** (two speakers, one left and one right). This document covers what it does, how to use it,
and how it works inside.

## In the dashboard

**Groups** in the sidebar lists the groups; **New group** creates one.

| Setting | What it does |
| --- | --- |
| Kind | `Group` (every member plays both channels), or `Stereo pair` (two speakers, left and right). |
| Members | The devices, up to 8. A device belongs to one group at a time. |
| Preferred leader | Which member leads (streams to the others). Without one, the server prefers a speaker over a screen. |
| Channel | Per member: stereo, left, right or mono. |
| Trim | Per output: an extra delay (negative plays earlier), set by calibration. The member's hand-set trim is the fallback for an output never calibrated. |
| Latency | How far ahead of being heard each frame is sent: room for Wi-Fi hiccups. 200–5000 ms. |
| Bitrate / PCM | Opus at up to 510 kbit/s (256 is transparent), or uncompressed PCM for wired links. |
| Start wait | How long a start waits for every member to be ready before playing without the slow one. |
| Screens show the video together | A video started on one of the group's screens opens on its other screens too (see [Video](#video)). On by default. |
| Live tracking (ultrasonic) | While music plays, an inaudible 18–20 kHz signal lets the speakers' microphones measure output-timing drift; only timing numbers are kept. Off by default. Needs an applied calibration that showed ultrasound reaches at least two active outputs; Bluetooth cannot carry it. |

**Latency**, **Bitrate** and **Start wait** each have an *automatic* switch, on by default (see
[Auto-tune](#auto-tune)). With it on, the number you set is where latency starts, and the ceiling for bitrate and start
wait. The group page shows the values in force ("620 ms · auto").

Once a group exists, play anything on any member (a voice request, AirPlay, Spotify Connect, a URL, Home Assistant,
a video on a screen): the whole group plays it. The member you played it on leads while it plays (see
[Who leads](#who-leads)). Pause, resume, skip and seek on any member act on the group; each member keeps its own
volume. A wake word on any member ducks the whole group, and only that member answers.

The group page's **Leader** line names the member that plays. When another member keeps the group clock, it names
both: "Playing from Bedroom TV · clock: Bedroom Station 2".

The group page shows each member live:

| Column | Meaning |
| --- | --- |
| Output | Under the device: its active `speaker`, `hdmi`, `bluetooth` or `other` output; Bluetooth also shows the delay it reports. |
| Trim | The active output's trim, with every stored output id and trim below it. |
| State | `idle`, `syncing` (clock not settled yet), `buffering`, `playing`, `rejoining` (catching up after falling behind). |
| Synced | The member's clock error is within the group's budget: 100 µs for a stereo pair, 1 ms in one room, 5 ms across rooms. |
| Clock error | How well the member's clock matches the leader's, in µs. Tens of µs over Wi-Fi is normal. |
| Queue | How much audio the member holds ahead of playback. The leader encodes up to latency + 2 s ahead. |
| Rejoins | How many times the member fell behind and caught up. |

## Video

### A video on a TV

A video on a screen that is in a group (an anime, a film, an AirPlay cast or mirror) plays its sound on the whole
group: the TV and a speaker make a stereo pair for the film, or the film's sound fills the house.

- **Sound:** the screen's player sends its audio into the group, as any other source does. The screen leads while
  it plays.
- **Picture:** each frame is shown when the group hears its sound, one group latency after it was decoded. Each
  member's trim moves its sound to that instant, so the sound and the picture land together on every device. A
  start waits for the group (the first frame holds until the group plays), so a video starts about one latency
  later than it would alone.
- **AirPlay mirroring** on the TV works the same way: its pictures wait for the group's latency too.
- **Passthrough stays local:** a passthrough session (a bitstream sent to the TV's own decoder, such as Dolby or
  DTS) cannot be mixed or streamed. It plays on the TV alone and the group is left idle (`group_source_local
  reason=direct`).

### Several screens in step

In a group with **Screens show the video together** on, a video started on one screen opens on every other online
screen of the group too:

- each follower opens the same catalog item, or the same YouTube video, and resolves its own stream (its own
  quality and relay token, so a follower's quality switch is its own; a quality or audio switch on the leading
  screen sends the followers nothing new);
- followers are muted: their sound is the group's, from the leading screen;
- each follower's picture follows the group's clock: it seeks to the group's position at the start and after a
  seek, then steers its playback speed (within 5 %) to stay on it. An error past 300 ms is corrected with a seek. A
  pause of the group pauses it;
- pause, seek and resume on a follower's remote act on the group, so any screen drives everyone's video;
- next and previous on a follower act on the leading screen's item: the follower is offered the leader's next,
  previous and up next (and its item actions and panels) as its own, and pressing next moves the leading screen to
  the next episode (the server's, with the same voiceover), which every follower then opens. The followers stay on
  the player while the leader moves, and the lead stays (the 30 s grace). A follower counts no up next down at an
  episode's end: the leading screen's countdown moves the group;
- a follower's own play, or leaving the player, drops it out of following for that item. The item's end, a stop,
  or the leader leaving the item stops the followers;
- speakers in the group just play the sound.

What the server played is followed: a catalog item, and a YouTube video (a link, or a music video on a screen). A
play no other screen can open, an AirPlay cast or mirror, or a video the server holds nothing of, plays on its
screen alone with its sound on the group (`follow_skipped reason=cast|not_reissuable`).

## Calibration

Clocks agree to tens of microseconds, but devices differ in how long their own output takes (a DAC, a TV's HDMI chain,
a soundbar), and sound takes about 3 ms per metre to reach you. A member's **trim** corrects both. **Calibrate** on
the group page measures it; pause the group's music first. Each speaker plays a short coded probe (a 1–6 kHz Gold-code
waveform, about 208 ms, 20 dB under its own volume) **four times**, the speakers taking turns each round.

A run belongs to the **outputs active when it was requested**. Each device reports one in its `media.group` state:
`speaker` and `hdmi` for those fixed outputs, `bt:<lowercase mac>` for a Bluetooth speaker, or another stable id of kind
`other`. The trim is stored per group, device and output id, so switching routes selects that output's calibration; the
member's older `trimUs` remains the fallback for an output never calibrated. Calibration results name the output each
trim belongs to.

The output also gives the leader a latency prior and a search half-width, in ms:

| Output | Prior ± half-width |
| --- | --- |
| Built-in speaker | 0 ± 150 |
| HDMI | 80 ± 200 |
| Bluetooth with `org.bluez.MediaTransport1.Delay` | its delay report ± 100 (BlueZ's units of 1/10 ms are converted to µs) |
| Bluetooth without a delay report | 200 ± 250 |
| Other, or a mixed group member that has not reported an output | 0 ± 250 |

The slot spacing grows to hold the widest window; the listener schedule's offsets use that actual spacing. Four
unhinted Bluetooth outputs still fit 16 slots and the 60 s recording bound. A group with no output reports at all keeps
the legacy request, for members from before output endpoints.

Every speaker has its own code, and every slot carries it. The detector correlates the recording with all members'
codes and rejects a peak that another code explains better. A speaker whose code is not found is therefore `missed`:
it is never credited with a louder neighbour's sound, as could happen with identical chirps.

Each round's direct-path arrival is measured and combined. Arrivals close together are clustered, and the rounds kept
give both a spread and a 95 % confidence interval for the offset. The interval must fit the group's tolerance: **250 µs
for a stereo pair**, **2 ms otherwise**; otherwise the member is `unstable` and nothing applies. The results show the
spread, the **95 % range** (`±X ms`) and the rounds kept. A small range with results that still move between runs means
the device's own output delay changes (a TV's HDMI path), which no calibration can fix.

During a rolling update, a member advertises calibration version 2 in its initial and periodic group `Status`. The
leader uses coded probes only when every connected member supports them. With even one older member it runs the legacy
identical-chirp plan unchanged, so a mixed group still calibrates (without code identity or the coded 95 % range).
Version 3 adds the ultrasonic capability round and live tracking below; either is used only when every connected member
supports it. A member also reports its active output `kind` in `Status`, so a Bluetooth output can be left out.

With version 3, a speaker-microphones calibration plays one extra **ultrasonic round** after the audible rounds. It uses
the same member order, output priors and slot spacing. Every 48 kHz recorder analyses it; each member's result gains
`ultrasonic`: `ok` when a recorder heard it confidently, `weak` when it was heard without a confident stable arrival,
`missed` when nobody heard it, or `unsupported`. A 44.1 or 16 kHz recorder cannot hear the band. Bluetooth cannot carry
it either, so that member's ultrasonic slot is omitted and its answer is `unsupported`. A run with no 48 kHz recorder
marks every member `unsupported`. The capability round never changes the audible run's offsets or trims.

**Retry louder.** When a speaker was missed, heard faintly, unstable or below 50 % confidence, **Retry louder** runs
the calibration again with only those speakers' chirps 6 dB louder (up to 14 dB, i.e. −6 dBFS of the music input).
It never changes a device's volume: the chirp stays under the speaker's own level (rule 7). Turn the speaker up
yourself if it is still not heard.

### This device's microphone (recommended)

Open the dashboard on your phone (over https, so the browser allows the microphone), stand where you listen, and press
**Calibrate**, then this option.

- The phone starts recording, then asks the leader to schedule the chirps: the first 1.5 s after the request, then at
  least 1.2 s apart (wider when an output prior needs it), the leader first in every round.
- The browser finds each chirp in its recording with a matched filter, to a fraction of a sample, and combines the
  rounds as above.
- It sends only the arrival times to the server. The recording never leaves the phone and is dropped right after.
- The server subtracts each speaker's slot and centres the result on the members' mean. Each member's trim is the
  negative of its offset. Nothing is applied unless every member was scheduled and measured, and no trim exceeds
  ±500 ms.
- Meanwhile every speaker with a microphone records the same chirps, as in the next mode. The server fetches that
  result when the phone posts its own, and shows it beside it: **Speaker mics** (the speakers' own output delays) and
  **Path to you** (the difference: how much longer each speaker's sound takes to reach you than the others', with
  its distance at 343 m/s). It is shown, never applied; if the path looks wrong (a speaker you sit next to with a long
  path), measure again.

This measures what you hear: output delays and distances together. It works for any group, including members with no
microphone (TV sticks). Calibrate again if you move a speaker or change the TV's sound settings.

### Speaker microphones

Every member records its microphones while all members chirp in turn. On boards with the vendor's audio engine (the
Yandex stations) these are up to the first four raw microphones, before the echo canceller; reference channels are left
out. The audible run's recording is resampled to 16 kHz as it arrives, including a 48 kHz vendor tap. A version-3 run
instead keeps one native-rate recording for both bands: the audible detector decimates its own path to 16 kHz, while
the ultrasonic detector uses the original 48 kHz samples and never alters the wake/vqe stream. Each coded probe is found on
every microphone and their median taken. A recording is kept in memory only, up to 8 MB (fewer channels on a long run),
and dropped after it is analysed. For each pair of members, the **in-recording difference** subtracts the recorder's own
arrival from the other member's in the same recording and round. The two opposite differences cancel the distance and
the recorders' input delays; a variance-weighted least-squares solve combines the pairs and produces each member's 95 %
range. Search windows follow the output priors above, while the coded identity still decides which speaker was heard.

When its recording reaches the analysis worker, a member acknowledges that to the leader with an analysis budget
(20 s by default). The leader finishes as soon as everyone answered. Otherwise a member without that acknowledgement
gets the original 3 s after the recording window; one that acknowledged gets its own budget, capped at 30 s. A recorded
member that still does not answer is `analysis_timeout`, told apart from `no_answer` (no recording acknowledgement and
no answer).

It needs **every member to have a microphone**. A member with none (`no_capture`) leaves the others `isolated` (no
other microphone heard them), and nothing is applied. Use the phone option for such groups. It measures the speakers'
own delays, not the distance to where you sit.

### Results

| Code | Meaning |
| --- | --- |
| `ok` | Measured; the trim applies. |
| `unstable` | Heard, but its rounds disagree or its 95 % range exceeds the tolerance (250 µs for a stereo pair, 2 ms otherwise). Try again, keep the room quiet, or retry louder. |
| `no_self` | The member did not hear its own probe. |
| `isolated` | No other microphone heard this member. |
| `no_capture` | The member has no microphone. |
| `no_answer` | The member did not acknowledge its recording or answer within 3 s of the window's end. |
| `analysis_timeout` | The member acknowledged its recording but its analysis did not answer within its budget (at most 30 s). |
| `missed` | The member's own coded probe was not found. No other member's arrival is attributed to it. Retry louder. |
| `weak` | Phone mode: the probe was too faint (under 15 dB above the noise). Retry louder. |
| `offline` | The member was not connected to the leader. |
| `inconsistent` | Speaker microphones: the pairs disagree by more than 1 ms. Try again. |
| `out_of_range` | A trim beyond ±500 ms (a chirp mistaken for another). Try again. |
| `expired` | The speakers' measure of a phone run was no longer held by the leader (another calibration started). |

Applying a trim pushes a new config but does **not** restart the group's stream or clock links. While audio plays, a
change of at most 50 ms slews the due-time offset by at most 1 ms per second (0.1 %, a pitch change of 1.7 cents, too
small to hear); the resampler carries the same rate change, continuously and without a splice, so no frame is cut or
repeated for the trim. An idle playout or a larger change (typically a route switch to Bluetooth) jumps directly. `group_trim` records the change and `mode=slew|jump`.

## Live tracking

**Off by default, and ultrasound only.** The group page's **Live tracking (ultrasonic)** switch stores the group's
`trackingEnabled` opt-in (false by default). While it is on and the state is `tracking`, the dashboard shows
**Microphones listening**; this is the only microphone-use indication, not an LED. Each member's row also keeps its
active output's last applied `ultrasonic` result, and shows the live offset, its 95 % interval and the last correction
while tracking is on.

The server's `media.group` config carries
`"tracking":{"enabled":true,"members":["leader-id","member-id"]}`. The server puts only members whose active output's
last applied calibration was `ultrasonic: "ok"` in `members`, and only the leader reads that list. Fewer than two such
members (one cannot be timed against another) pushes `enabled:false` even when the opt-in is on. Nothing audible is ever
added to music automatically. Manual one-shot calibration remains audible as before.

A probe is the same 1023-chip Gold code as a coded calibration, at 2500 chips/s on a 19.25 kHz cosine carrier: a
0.4156 s waveform in the 18–20.5 kHz band. Its guard spectrum is inside 17.5–21 kHz and its leakage below 16 kHz is
under −60 dB relative to the passband peak. It plays at **−30 dBFS on the music input**. This is low enough to leave
headroom and high enough for the code's correlation gain; it is not a volume setting, and the device's own volume
remains the ceiling (rule 7).

While music plays, the leader starts one cycle every **10 s**. Each tracked member has its own slot, using the coded
plan's TDMA spacing and output priors. A member mixes only its own probe into its local playout after placement and
before the output, so it follows the real output path and the active trim exactly as music does; it is not encoded into
the group stream. The leader announces a cycle only when every connected member is version 3, so a legacy member makes
the group `unsupported` and no tracking envelope is sent.

Members with a 48 kHz raw microphone record only the current cycle. On pickle and midi this is the vendor engines' raw
microphone tap before the vqe, kept at 48 kHz rather than resampled; the wake and voice-uplink path is untouched. A
44.1 or 16 kHz recorder answers `unsupported`, and a Bluetooth output has no slot. The recording is a memory-only ring
for one cycle, dropped after analysis or cancellation. Mute drops it immediately, and a voice session or a calibration
cancels the cycle and future mixing. Microphone use is visible only as the dashboard's tracking state, not as an LED.

The detector down-converts at 19.25 kHz, low-passes, decimates to an 8 kHz complex baseband and correlates against the
baseband Gold references. It does not decimate away the ultrasonic band. Candidate identity, direct-path clustering,
in-recording differences and robust arrival statistics are the coded detector's. Each recorder sends only relative
arrival numbers, never audio (rule 8).

The leader solves each cycle with `solveV2` and keeps the last **6 solutions**. Once those offsets agree within the
deadband, their between-cycle MAD also contributes to the confidence interval; a persistent route-latency step is the
signal being measured, not treated as noise. Each member's latest valid offset then passes an exponential filter with **α = 0.6** (new solution weight 0.6, previous
estimate 0.4); its confidence interval uses the same coefficient. This reaches 93.6 % of a real step by its third
window without letting one cycle decide a correction. `GroupTracked` is emitted after every window and cadenced
publishes:

```json
"tracking":{"state":"tracking","members":[{"deviceId":"member-id","offsetUs":926,"ci95Us":40,"cycles":6,"correctionUs":-926,"correctionId":1}]}
```

`state` is `off`, `idle` (enabled but not measuring), `tracking`, `needs_calibration`, or `unsupported`. `members` is
always an array; `correctionUs` is omitted when zero. `correctionId` is a per-member counter the leader increments only
for a new correction and keeps on later snapshots, so the server applies a replayed correction exactly once. The
calibration result's per-member capability is simply `"ultrasonic":"ok"|"weak"|"missed"|"unsupported"`.

A correction is only a **suggestion to the server**, which owns the per-output trim store and applies it through the
usual live-slew path. Audiod never applies it. It is emitted only when all of these hold for one member:

- the smoothed absolute offset exceeds the deadband: **250 µs for a stereo pair, 1000 µs otherwise**;
- that member's own self-arrival has moved past the deadband from its stable baseline, so a neighbour's shift of the
  solution's mean never earns the unchanged member a correction;
- its 95 % interval is under half that deadband for **3 consecutive windows**;
- no correction went to that member in the last **60 s**;
- its magnitude is at most **5 ms**, and the correction is the negative offset clamped to that bound.

Past 5 ms the state is `needs_calibration` and no correction is suggested. An unchanged member inside the deadband gets
none.

The server applies a new correction to that member's **active output's** stored trim, never the fallback or another
route. It also admits at most one correction per member per 60 s. A result past the trim range ±500 ms is refused and
logged; otherwise the new config goes out and the device slews the change live as above. `group_tracking_correction`
logs the group, device and output ids, `correction_us`, the resulting `trim_us` and its code. The group view's
`tracking` object carries `state` and, per member, `offsetUs`, `ci95Us`, `cycles`, `lastCorrectionUs` and
`lastCorrectionAt`; the existing `groups` feed topic's `group_state` event invalidates it.

## Auto-tune

The leader tunes three settings while the group plays, each only when its automatic switch is on:

| Setting | Down | Up |
| --- | --- | --- |
| Latency | After 5 min of playing with every member holding at least half the latency in reserve: by 15 % (at least 100 ms), never below what the margin seen needs, nor 200 ms. | At once after a member underruns or rejoins, or the leader's own playout underruns: to at least 1.5× (+250 ms minimum), at most once per 10 s, up to 5000 ms. |
| Bitrate | When a member's unsent backlog passes 500 ms of audio, or it rejoins twice within a minute: one step along 256 → 192 → 128 → 96, at most once per 15 s. | One step after 2 min with neither, back to your setting. |
| Start wait | Set from how long members actually took to be ready at the last 8 starts: the slowest × 1.5 + 300 ms, between 500 ms and your setting. | |

- Latency only changes at the start of a track, a seek or a resume, so the audio never jumps.
- Bitrate changes on the next encoded frame.
- Uncompressed PCM is never tuned.
- Each change is logged on the leader as `group_tuned what=<latency|bitrate> from=<old> to=<new> reason=<underrun|rejoin|margin|backlog|clean>`.

## How it works

### The group clock

Each member runs an NTP-style exchange with the leader over UDP, 10 times a second.

- **What is measured:** each exchange gives four timestamps: send, leader receive, leader reply, member receive. The
  receive times come from the kernel's socket timestamps (`SO_TIMESTAMPNS`), so a busy CPU does not skew them.
- **The fit:** the member keeps a 30 s window, uses the fastest tenth of exchanges (the ones least delayed by Wi-Fi), and
  fits the leader's clock as offset plus drift.
- **Applying it:** the mapping is slewed (at most 50 ppm), so it never jumps. It is stepped only for errors over 5 ms.
- **Losing it:** with no answer for 3 s, the member mutes and starts the fit again.
- **What it achieves:** tens of microseconds over Wi-Fi.
- **Who serves it:** the leader, unless the config names another member the **clock master** (`clock_host` and
  `clock_master` in `GroupConfig`; `clock` and `clockMaster` in `media.group`). The master answers the probes and
  plays by its own clock. The leader then fits the master's clock as a member does and stamps its frames in the
  master's time:
  - it starts no epoch until its fit is synced (state `syncing`);
  - a lost fit mutes its own output and holds its timeline: the frames already sent stay valid, so the members play
    them out;
  - once the fit is back, the group plays on, or starts a new epoch where the last one ended if the members ran out.

  This keeps the clock on a speaker when a screen streams (a screen's clock replies are 4–6× noisier).

### Who leads

The server elects two roles per group:

- **Clock master:** the preferred leader while it can lead, else the first member by position, speakers before
  screens. It never moves for what plays.
- **Leader** (the stream source): the member that plays a source of its own, so a cast, a Spotify Connect pick or a
  film started on any member plays on the whole group. Each device's `media.group` state says `localSource` while it
  plays a URL, file or stream, Soloist, AirPlay, or an external session on its mixer (a screen's player), but not the
  group's own stream, a passthrough (direct) session, which plays on its device alone, or Bluetooth.
  1. the member that raised `localSource` last;
  2. else the last leader for 30 s after its source went idle, so a pause or the next episode does not move it;
  3. else the clock master.

When the two differ, the leader and the other members dial the master's clock, and only the stream moves:

- **Handover:** the new leader binds the stream, the members reconnect to it and a new epoch starts. Every member's
  clock fit is kept, so the cost is about a second of the new source on its own device alone, then the group joins
  it. The old leader's own source is displaced by the group's stream: the latest source wins, as on one device.
- **Grace:** a leader whose source went idle keeps the lead for 30 s, so a pause or the next episode does not move it.
- **Flap hold:** a move to a new source within 10 s of the last move waits for the 10 s to pass, so a source that
  flaps cannot bounce the group (`group_leader_held reason=flapping`).

Media commands sent to any member go to the leader.

On the leader, each source plays into the group's feed instead of the device's own music input (`group_source
target=feed`): Soloist, AirPlay, a URL or file, and a screen's player, whose audio a ring pump reads into the feed
(`group_ring_pump`). When the device stops leading, each goes back to its music input (`target=music`).

### The stream

The leader streams over TCP on the same port: 48 kHz stereo, 20 ms frames, Opus (or PCM).

- Every frame carries the leader-clock time at which its first sample must be heard: the start time plus the latency
  plus n × 20 ms.
- The leader encodes up to latency + 2 s ahead and keeps the last 10 s, so a member that falls behind can catch up from
  it.
- Start, pause and stop are explicit messages:
  - a start waits until every member reports ready (or the start wait passes);
  - a pause stops every member on the same sample;
  - a resume restarts from the sample that was actually heard.
- What plays (title, artist, album, artwork url, length, playing or not) goes to the members with the stream; each
  member's session runs at the position its playout hears. A member's screen gets the cover from the server
  (`GET /v2/session-artwork`, its device token): the server's view of a member's session borrows the stream source's
  cover (an AirPlay upload, an artwork url) and details while both name the same title and artist, which also gives
  Home Assistant and the dashboard the member's cover. Speakers without a screen fetch nothing.

### Placement and drift

Each member, the leader included, plays frames through the same placement code.

- **Placement:** it converts each frame's leader time to its own clock and places the frame on its active output at that
  instant, that output's trim added (or the member fallback when the output has none). A trim change alone never
  rebuilds the member or leader object; small live changes slew through the resampler as described above.
- **Drift:** the two crystals' drift (tens of ppm) is absorbed by a controller that runs the resampler fractionally
  faster or slower. The audio is never cut for drift.
- **Cuts:** a placement error over 5 ms is corrected with a short crossfade.
- **Rejoin:** after an error over 200 ms, an underrun or a lost clock, the member fades out, clears, and asks the leader
  for frames from a point just ahead. It then rejoins on that exact frame while the others play on.

### Security

- **Group key:** the server makes a 32-byte key per group, pushed to members as a secret and rotated when membership
  changes. Every clock packet and stream message is authenticated with it (HMAC-SHA256; the stream with a per-session
  key and per-direction counters).
- **Not encrypted:** the audio itself is music on your own network.
- **Network:** the leader listens on port 7701, TCP and UDP, only while it leads a group. With a separate clock
  master, the leader binds TCP alone and the master binds UDP.

### Where it lives

| Part | Code |
| --- | --- |
| Clock, stream, playout, tuner, calibration | `media/lib/src/sync/` |
| audiod's group handlers and the calibration recording | `media/audiod/group-*`, `media/audiod/capture.cpp` |
| The device's `media.group` capability | `device/internal/group/` |
| Output routes and BlueZ's delay report | `device/boards/{rpi,android-tv,yandex-*}`, `device/internal/bluetooth/` |
| Groups, clock master and leader election, config push, calibration apply | `server/internal/groups/` |
| Screens following a video (the fan-out of plays and stops) | `server/internal/follow/` |
| A screen's player feeding the group (the ring pump) | `media/audiod/group-feed.*` |
| A follower's picture steering, the mirror's group lead | `screen/player/video-follow.*`, `screen/player/mirror-schedule.*` |
| The group pages, phone recorder and chirp detector | `dashboard/src/components/groups/`, `dashboard/src/lib/{listener-recorder,listener-calibration,chirp-detector}.ts` |
| Wire formats | `proto/cadence/sync/v1/sync.proto` (stream), `media/lib/src/sync/clock-wire.h` (clock), `proto/cadence/audiod/v1/audiod.proto` (`Group*`), `pkg/capschema/group.go` |

## API

Admin API, under `/api/v1`. The live feed's `groups` topic carries `group_changed` and `group_state`
(`deploy/server/README.md`, Live feed topics).

| Operation | Request |
| --- | --- |
| `list-groups`, `get-group` | `GET /groups`, `GET /groups/{id}` (with `trackingEnabled`, the in-memory `tracking` view and `tuning`: the leader's latency, bitrate and start wait in force). Each member's `status.output` is its active output; `trims` lists `{outputId,trimUs,ultrasonic}` beside the fallback `trimUs` |
| `create-group`, `update-group`, `delete-group` | `POST /groups`, `PUT /groups/{id}`, `DELETE /groups/{id}`; create and update take `trackingEnabled` (false when omitted) |
| `join-device-group`, `leave-device-group` | `POST /devices/{id}/group`, `DELETE /devices/{id}/group` |
| `calibrate-group` | `POST /groups/{id}/calibrate`, body `{"mode":"mics"}` (default) or `{"mode":"listener"}`, optionally `"boost":[{"deviceId","gainDb"}]` (0–14 dB). Listener mode answers `scheduled` with a `calibrationId` valid once for 120 s, `startsInMs`, `rounds`, `chirp` and `slots` (one per round and member, each with its `round` and Gold `code`). A coded run also carries `probe: {"kind":"gold","bandLoHz","bandHiHz","fadeUs"}` for the browser's detector; results carry each member's `ci95Us`, `ultrasonic` and the `output` snapshotted for its trim. |
| `apply-group-calibration` | `POST /groups/{id}/calibrations/{calibrationId}`, body `{"members":[{"deviceId","code":"ok"\|"weak"\|"missed"\|"unstable","arrivalUs","confidence","spreadUs","rounds"}]}`, where `arrivalUs` is the chirp's median arrival relative to the leader's round-0 chirp; the answer adds the speakers' own measure (`micsCode`, `micsOffsetUs`, `micsSpreadUs`, `pathUs`) when the leader had one |

## Logs

Logs carry ids, codes and numbers only (no titles, no audio). A member's `group_rejoin` and `group_clock` go out at
most once per reason per 5 s; `suppressed` says how many of that reason were held back since its last line, and
`group_summary` counts them all. Delays are in 48 kHz frames (960 is 20 ms).

| Line | Where | Meaning |
| --- | --- | --- |
| `group_config_sent` / `group_started` / `group_stopped` | cadenced, audiod | The group config reached audiod; the role started or stopped. `group_started`'s `clock` says whether this device `serves` the group clock or `dials` it. |
| `group_trim` | audiod | A live trim changed from `from_us` to `to_us`; `mode=slew` (at most 1 ms per second while playing) or `jump` (idle, or a change past 50 ms). The group link does not restart. |
| `group_tracking` | audiod (leader) | One ultrasonic cycle finished: `id`, `slots` and `answers`. Numbers only; no recording or arrival samples leave the analysis path. |
| `group_tracked` | audiod (leader) | A tracking window or state change: `state`, `members` and how many bounded `corrections` were suggested. |
| `group_tracking_correction` | server | A new correction was applied to an active output (`code=ok`) or refused (`out_of_range`): group, device and output ids, `correction_us` and `trim_us`. |
| `group_state` | cadenced | The member's state changed (with `local_source`). |
| `group_source` | audiod | The playing session's source (`kind=url\|soloist\|airplay\|external\|...`) now plays into the group's feed or the device's own music input (`target=feed\|music`). |
| `group_source_local` | cadenced, audiod | A passthrough session plays on this device alone (`reason=direct`). |
| `group_ring_pump` | audiod (leader) | A screen's player audio is read into the group's feed (`state=on`), or no longer (`off`). |
| `group_ring_started` / `group_ring_flushed` | audiod (leader) | An epoch of the screen's audio started: the group first heard it `delay_ms` after the pump took it (the start delay); a seek flushed the pumped audio. |
| `group_leader_moved` / `group_leader_held` | server | The leader moved (`from`, `to`, `reason=source\|grace\|preferred`), or a move to a source waited (`reason=flapping`). |
| `video_follow` | the screen (follower) | Every 10 s: its picture's `error_ms` to the group's position, its `speed` and its `seeks` so far. |
| `follow_play` / `follow_stop` / `follow_left` / `follow_failed` | server | A follower screen was sent the leader's item, stopped with it, left it (its own play) or never played it (`code=play\|unseen`). |
| `follow_skipped` | server | The leading screen plays something no follower can open (`reason=cast\|not_reissuable`); once per session. |
| `follow_control` | server | A follower's transport (`media.control`, its next or previous) or item action went to its leader (`leader_id`). |
| `video_follow_play` / `video_follow_play_failed` | server | A follower resolved a YouTube video for itself (`renditions`, `ms`), or could not (`code`). |
| `group_tuned` | audiod (leader) | Auto-tune changed a setting. |
| `group_calibrating` / `calibration_record` / `calibration_recorded` / `group_calibrated` | both | A calibration's run, what was recorded (mode, rate, length), its result. |
| `calibration_analysed` | audiod (member, and the leader's own recording) | The analysis took `ms`, over `frames` at `rate` on `channels`; `id` names the run and `code` its answer. |
| `calibration_member` | audiod (leader) | At finish, one line per member with its `code` and `answered_ms` after the recording window (`0` for an early answer, `-1` for none). |
| `group_control` | audiod (leader) | A member's transport command acted on the group. |
| `group_rejoin` | audiod (member) | The member's audio dropped and it rejoins the stream. `reason`: `placement` (an error past 200 ms), `underrun`, `clock_lost` (the clock's mapping withdrawn mid-play), `clock_step` (the mapping stepped), `gap` (more than 25 lost frames in a row) or `link` (the leader's link dropped and came back; `link` names how: `closed`, `unreachable`, `protocol`, ...). With `error_us` (heard minus due), the output's delay `delay_frames` at the drop and `delay_min_frames`/`delay_max_frames` over the run it ended (a spread there is the output's delay jumping, not the clock), `queue_ms`, `played_ms` (how long the run lasted), `clock_error_us`, the totals `rejoins` and `underruns`, and `suppressed`. |
| `group_clock` | audiod (member) | The fit to the clock master changed: `reason=lost` (no reply for 3 s), `step` (the estimate jumped past 5 ms; `step_us`), `unsynced` (its error passed `budget_us`: the state turns `syncing` while the audio plays on) or `synced` (under it again, or the first fit). With `error_us`, `rtt_min_us`, `rtt_p50_us` (the window's exchanges), `samples`, `ppm` (the clocks' skew) and `suppressed`. |
| `group_summary` | audiod (member) | Every 30 s while the member plays: the count of each `group_rejoin` and `group_clock` reason in the period (held back or not), `clock_error_p50_us`/`clock_error_max_us`, `rtt_min_us`/`rtt_p50_us`, `placement_error_max_us` and `delay_min_frames`/`delay_max_frames`. |

## Limits

- One group per device. At most 8 members.
- A screen leading a group is fine, but a speaker leads with a steadier clock.
- Members must reach the leader on the local network (port 7701, TCP and UDP).
- A track's end reaches the members as a pause on its last frame. At a latency of 500 ms or less (auto-tune can go
  down to 200), the leader sees the end only after the members ran out. They still go idle cleanly, but a moment
  later.
- A member that becomes active only after a start gave up waiting for it counts one rejoin.
- A leadership handover plays about a second of the new source on its own device alone before the group joins.
- Video:
  - a resume after a pause skips about one group latency of the video;
  - a follower screen's pause and resume may land up to about 100 ms off the group's;
  - a next pressed on two screens at once moves the group twice;
  - AirPlay mirroring on a TV that leads a group whose latency passes about 2.7 s overflows the mirror's queue;
  - an AirPlay cast or mirror on a screen is not followed by the group's other screens;
  - a passthrough session plays on its own device only (see [A video on a TV](#a-video-on-a-tv)).
