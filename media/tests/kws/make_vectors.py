"""Writes the keyword frontend's test vectors: python3 -I media/tests/kws/make_vectors.py [out_dir].

Each vector is our own synthetic int16 signal and the features frontend_ref.py computes for it, in one file
vectors/<name>.f32 of little-endian float32 values:

- a 4-value header: format version (1), n_samples, n_frames, n_mels (40), each an exact integer;
- n_samples input samples: the int16 values, exact in float32;
- n_frames * n_mels features, frame-major (frame 0's 40 values first).

Every length is a multiple of the 1280-sample fragment, so streaming the input in fragments must give the same frames.
The signals use no random library (the noise comes from a fixed integer generator), so a re-run on one machine writes
the same bytes. Another numpy or platform may change the features' last bits, so consumers compare the inputs exactly
and the features within 1e-3 dB, never by hash (vectors/README.md).
"""

import sys
from pathlib import Path

import numpy as np

sys.path.insert(0, str(Path(__file__).resolve().parent))
import frontend_ref as fr

VERSION = 1


def tone(freqs_amps, n):
    t = np.arange(n, dtype=np.float64) / fr.SAMPLE_RATE
    x = sum(a * np.sin(2.0 * np.pi * f * t) for f, a in freqs_amps)
    return np.round(x * 32767.0).astype(np.int16)


def chirp(f0, f1, amp, n):
    t = np.arange(n, dtype=np.float64) / fr.SAMPLE_RATE
    dur = n / fr.SAMPLE_RATE
    phase = 2.0 * np.pi * (f0 * t + (f1 - f0) * t * t / (2.0 * dur))
    return np.round(amp * 32767.0 * np.sin(phase)).astype(np.int16)


def lcg_noise(n, lo, hi, seed):
    # numerical recipes' 32-bit lcg: the same values everywhere, whatever numpy's generators do
    out = np.empty(n, dtype=np.int16)
    state = seed
    span = hi - lo + 1
    for i in range(n):
        state = (1664525 * state + 1013904223) & 0xFFFFFFFF
        out[i] = lo + (state >> 8) % span
    return out


def full_scale_square(n, period):
    half = period // 2
    return np.where((np.arange(n) // half) % 2 == 0, 32767, -32768).astype(np.int16)


def signals():
    f = fr.FRAGMENT
    return {
        "silence": np.zeros(3 * f, dtype=np.int16),
        "sine-1k": tone([(1000.0, 0.5)], 6 * f),
        "two-tone": tone([(250.0, 0.25), (3150.0, 0.25)], 6 * f),
        "chirp": chirp(50.0, 7950.0, 0.3, 16 * f),
        "noise": lcg_noise(6 * f, -8192, 8191, seed=12345),
        "quiet": lcg_noise(6 * f, -1, 1, seed=777),
        "edge-full-scale": full_scale_square(3 * f, period=32),
    }


def check_stream(samples, want):
    stream = fr.Stream()
    got = [stream.push(samples[i : i + fr.FRAGMENT]) for i in range(0, len(samples), fr.FRAGMENT)]
    got = np.concatenate(got)
    # blas may block a matrix product differently for another row count, so allow the last bits to differ
    if got.shape != want.shape or not np.allclose(got, want, rtol=0.0, atol=1e-4):
        raise SystemExit("streaming and whole-signal features differ")


def main():
    out = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parent / "vectors"
    out.mkdir(parents=True, exist_ok=True)
    for name, samples in signals().items():
        feats = fr.features(samples)
        check_stream(samples, feats)
        header = np.array([VERSION, len(samples), len(feats), fr.MEL_BANDS], dtype="<f4")
        blob = np.concatenate([header, samples.astype("<f4"), feats.astype("<f4").ravel()])
        (out / f"{name}.f32").write_bytes(blob.astype("<f4").tobytes())
        print(f"{name}: {len(samples)} samples, {len(feats)} frames, features {feats.min():.2f}..{feats.max():.2f} dB")


if __name__ == "__main__":
    main()
