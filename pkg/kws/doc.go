// Package kws is Cadence's custom wake word: openWakeWord's front end (a log-mel spectrogram and Google's
// speech_embedding net, giving a 96-value embedding of the last 775 ms every 80 ms) and a small per-word head over the
// last 16 embeddings (1.28 s), which the server trains from a few takes of the owner's word and cadenced runs on the
// microphone. Pure Go: the server and the device share it, and the device's build has no cgo.
//
// The feature models' weights (assets/features.bin) are openWakeWord's released melspectrogram and embedding models
// (Apache-2.0, assets/NOTICE), converted by tools/kws/export.py; the computation here is Cadence's own, checked against
// onnxruntime's outputs (testdata/golden.bin). The negative features training needs are CC BY-NC-SA 4.0 and never
// part of the repository: the server reads them from a file the owner's install fetched (ReadNegatives).
//
// Nothing here stores or logs audio: callers hand samples in and get embeddings, scores and models back.
package kws
