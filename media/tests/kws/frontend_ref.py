"""Reference keyword frontend for the VK Capsula Mini board (numpy only).

The oracle for cadence's C++ keyword frontend: it turns 16 kHz mono int16 audio into the 40 values per frame that the
stock «Маруся» model, read from the device at runtime, takes as input. It is written from the facts recorded in the
reverse-engineering note, not from vendor code:

- sample rate 16000 Hz, mono; each int16 sample s becomes x = s / 32767 (32767, not 32768);
- frames of 640 samples (40 ms) every 160 samples (10 ms) over a continuous stream: no centring and no padding, so a
  stream of N samples has (N - 640) // 160 + 1 frames;
- window: periodic Hamming, w[n] = 0.54 - 0.46 * cos(2 * pi * n / 640), n = 0..639;
- FFT: the windowed frame fills the first 640 points of a 2048-point buffer and the rest is zero (the frame is not
  centred); real FFT, unnormalised, 1025 bins;
- power spectrum: P[k] = re^2 + im^2 (no magnitude, no 1/N);
- filterbank: 40 triangles on the HTK mel scale, mel(f) = 2595 * log10(1 + f / 700), from 0 to 8000 Hz; 42 edges evenly
  spaced in mel, h[j] = 700 * (10^(j * mel(8000) / 41 / 2595) - 1); bin k sits at f[k] = k * 8000 / 1024;
  W[i][k] = max(0, min((f[k] - h[i]) / (h[i+1] - h[i]), (h[i+2] - f[k]) / (h[i+2] - h[i+1]))), so each triangle
  peaks at 1 (no area normalisation);
- feature: 10 * log10(max(sum_k P[k] * W[i][k], 1e-10)), so the floor is -100 dB;
- no pre-emphasis, dither, DC removal, mean or variance normalisation, or PCEN;
- the model takes 8 consecutive frames per call as float32 [1, 8, 40], oldest frame first, mel index fastest; calls do
  not overlap.

Everything is computed in float64 and returned as float32. The device computes in float32 (a single-precision FFT), so
a float32 implementation matches these values closely but not bit for bit: a float32 numpy pipeline stays within
1.2e-4 dB on every test vector. Compare features with an absolute tolerance of 1e-3 dB (vectors/README.md).
"""

import numpy as np

SAMPLE_RATE = 16000
INT16_SCALE = 32767.0
FRAME_LENGTH = 640
FRAME_STEP = 160
FFT_SIZE = 2048
FFT_BINS = FFT_SIZE // 2 + 1
MEL_BANDS = 40
MEL_FMIN = 0.0
MEL_FMAX = SAMPLE_RATE / 2
LOG_FLOOR = 1e-10
FRAMES_PER_CALL = 8
FRAGMENT = 1280


def hamming_window():
    """The periodic Hamming window of FRAME_LENGTH points."""
    n = np.arange(FRAME_LENGTH, dtype=np.float64)
    return 0.54 - 0.46 * np.cos(2.0 * np.pi * n / FRAME_LENGTH)


def hz_to_mel(f):
    return 2595.0 * np.log10(1.0 + f / 700.0)


def mel_to_hz(m):
    return 700.0 * (10.0 ** (m / 2595.0) - 1.0)


def mel_filterbank():
    """The [FFT_BINS, MEL_BANDS] weight matrix: column i is triangle i."""
    bins = np.arange(FFT_BINS, dtype=np.float64) * MEL_FMAX / (FFT_SIZE // 2)
    edges = mel_to_hz(np.linspace(hz_to_mel(MEL_FMIN), hz_to_mel(MEL_FMAX), MEL_BANDS + 2))
    w = np.zeros((FFT_BINS, MEL_BANDS))
    for i in range(MEL_BANDS):
        rise = (bins - edges[i]) / (edges[i + 1] - edges[i])
        fall = (edges[i + 2] - bins) / (edges[i + 2] - edges[i + 1])
        w[:, i] = np.maximum(0.0, np.minimum(rise, fall))
    return w


_WINDOW = hamming_window()
_FILTERS = mel_filterbank()


def frame_count(n_samples):
    """The number of frames a stream of n_samples yields."""
    return 0 if n_samples < FRAME_LENGTH else (n_samples - FRAME_LENGTH) // FRAME_STEP + 1


def features(samples):
    """Features of a whole int16 signal: float32 [frame_count(len(samples)), MEL_BANDS]."""
    x = np.asarray(samples, dtype=np.int16).astype(np.float64) / INT16_SCALE
    n = frame_count(len(x))
    if n == 0:
        return np.zeros((0, MEL_BANDS), dtype=np.float32)
    starts = np.arange(n) * FRAME_STEP
    frames = x[starts[:, None] + np.arange(FRAME_LENGTH)[None, :]] * _WINDOW
    spectrum = np.fft.rfft(frames, n=FFT_SIZE, axis=1)
    power = spectrum.real**2 + spectrum.imag**2
    energy = power @ _FILTERS
    return (10.0 * np.log10(np.maximum(energy, LOG_FLOOR))).astype(np.float32)


class Stream:
    """The streaming frontend: int16 fragments in, the frames they complete out.

    It keeps the samples a frame still needs between fragments, so pushing a signal in pieces of any size yields the
    same frames as features() on the whole signal.
    """

    def __init__(self):
        self._pending = np.zeros(0, dtype=np.int16)

    def reset(self):
        self._pending = np.zeros(0, dtype=np.int16)

    def push(self, fragment):
        buf = np.concatenate([self._pending, np.asarray(fragment, dtype=np.int16)])
        n = frame_count(len(buf))
        out = features(buf[: (n - 1) * FRAME_STEP + FRAME_LENGTH])
        self._pending = buf[n * FRAME_STEP :]
        return out


class Calls:
    """Groups frames into the model's inputs: float32 [1, FRAMES_PER_CALL, MEL_BANDS], without overlap."""

    def __init__(self):
        self._frames = np.zeros((0, MEL_BANDS), dtype=np.float32)

    def reset(self):
        self._frames = np.zeros((0, MEL_BANDS), dtype=np.float32)

    def push(self, frames):
        self._frames = np.concatenate([self._frames, np.asarray(frames, dtype=np.float32)])
        out = []
        while len(self._frames) >= FRAMES_PER_CALL:
            out.append(self._frames[:FRAMES_PER_CALL][None])
            self._frames = self._frames[FRAMES_PER_CALL:]
        return out

    def queued(self):
        return len(self._frames)
