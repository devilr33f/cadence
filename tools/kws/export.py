"""Convert openWakeWord's released feature models into pkg/kws's own tensor file, and write the golden vectors its
tests compare against.

    python -P tools/kws/export.py <cache dir>

Run by `make kws-assets` in the pinned python image (numpy, onnx and onnxruntime at pinned versions); nothing here
runs on the host. The two models are openWakeWord v0.5.1's release assets (Apache-2.0, github.com/dscripka/openWakeWord),
fetched into the cache dir and checked against their sha256 before anything reads them.

Writes:
  pkg/kws/assets/features.bin  the mel filterbank, the stft window and the embedding net's 20 convolutions
  pkg/kws/testdata/golden.bin  a deterministic test signal, onnxruntime's mel frames for each 80 ms step of it, and
                               the embedding of each full 76-frame window, as openWakeWord streams them

The tensor file: b"CKW1", u32 count, then per tensor u8 name length, name (ascii), u8 rank, u32 dims, float32 data;
all little-endian.
"""

import hashlib
import struct
import sys
import urllib.request
from pathlib import Path

import numpy as np
import onnx
import onnxruntime as ort
from onnx import numpy_helper

ROOT = Path(__file__).resolve().parents[2]
RELEASE = "https://github.com/dscripka/openWakeWord/releases/download/v0.5.1/"
MODELS = {
    "melspectrogram.onnx": "ba2b0e0f8b7b875369a2c89cb13360ff53bac436f2895cced9f479fa65eb176f",
    "embedding_model.onnx": "70d164290c1d095d1d4ee149bc5e00543250a7316b59f31d056cff7bd3075c1f",
}
STEP = 1280
CONTEXT = 480
WINDOW = 76


def fetch(cache: Path, name: str) -> Path:
    path = cache / name
    if not path.exists():
        with urllib.request.urlopen(RELEASE + name) as r:
            path.write_bytes(r.read())
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    if digest != MODELS[name]:
        path.unlink()
        raise SystemExit(f"{name}: sha256 {digest}, want {MODELS[name]}")
    return path


def write(path: Path, tensors):
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "wb") as f:
        f.write(b"CKW1" + struct.pack("<I", len(tensors)))
        for name, arr in tensors:
            arr = np.ascontiguousarray(arr, dtype="<f4")
            raw = name.encode("ascii")
            f.write(struct.pack("<B", len(raw)) + raw + struct.pack("<B", arr.ndim))
            f.write(struct.pack(f"<{arr.ndim}I", *arr.shape))
            f.write(arr.tobytes())


def signal() -> np.ndarray:
    """4 s at 16 kHz on the int16 scale: a rising chirp, noise bursts, near silence and a vowel-like buzz, so every
    mel band and the top-80 dB clamp both get exercised"""
    rng = np.random.default_rng(20261120)
    t = np.arange(16000 * 4) / 16000.0
    chirp = np.sin(2 * np.pi * (100 * t + 900 * t * t)) * 6000 * (t < 1.0)
    noise = rng.normal(0, 1, t.size) * 2500 * ((t >= 1.0) & (t < 2.0)) * (np.sin(2 * np.pi * 3 * t) > 0)
    quiet = rng.normal(0, 1, t.size) * 3 * ((t >= 2.0) & (t < 2.5))
    f0 = 140 + 20 * np.sin(2 * np.pi * 2 * t)
    phase = np.cumsum(2 * np.pi * f0 / 16000.0)
    buzz = sum(np.sin(k * phase) / k for k in range(1, 30)) * 3000 * (t >= 2.5)
    return np.clip(np.round(chirp + noise + quiet + buzz), -32768, 32767).astype(np.float32)


def main(cache: Path):
    cache.mkdir(parents=True, exist_ok=True)
    mel_path = fetch(cache, "melspectrogram.onnx")
    emb_path = fetch(cache, "embedding_model.onnx")

    mel_model = onnx.load(mel_path)
    mi = {t.name: numpy_helper.to_array(t) for t in mel_model.graph.initializer}
    stft_real = mi["0.stft.conv_real.weight"][:, 0, :]
    window = stft_real[0]
    # the stft's kernels are the window times the dft's basis: pkg/kws runs an fft with this window instead
    k = np.arange(stft_real.shape[0])[:, None]
    n = np.arange(stft_real.shape[1])[None, :]
    assert np.abs(stft_real - window * np.cos(2 * np.pi * k * n / 512)).max() < 1e-6
    tensors = [("mel.window", window), ("mel.filters", mi["1.melW"])]

    emb_model = onnx.load(emb_path)
    ei = {t.name: numpy_helper.to_array(t) for t in emb_model.graph.initializer}
    convs = [node for node in emb_model.graph.node if node.op_type == "Conv"]
    assert len(convs) == 20
    for i, node in enumerate(convs):
        w = ei[node.input[1]]
        b = ei[node.input[2]] if len(node.input) > 2 else np.zeros(w.shape[0], np.float32)
        tensors.append((f"conv{i}.w", w))  # out, in, time, mel
        tensors.append((f"conv{i}.b", b))
    write(ROOT / "pkg/kws/assets/features.bin", tensors)

    audio = signal()
    mel_session = ort.InferenceSession(str(mel_path))
    emb_session = ort.InferenceSession(str(emb_path))
    padded = np.concatenate([np.zeros(CONTEXT, np.float32), audio])
    steps = audio.size // STEP
    frames = []
    for i in range(steps):
        chunk = padded[i * STEP : i * STEP + STEP + CONTEXT]
        out = mel_session.run(None, {"input": chunk[None, :]})[0]
        frames.append(out.reshape(-1, 32) / 10 + 2)
    mel = np.concatenate(frames)
    embs = []
    # windows end on a step's last frame: 80, 88, ... (76 frames are first complete at the 10th step)
    for end in range(8 * ((WINDOW + 7) // 8), mel.shape[0] + 1, 8):
        window_frames = mel[end - WINDOW : end][None, :, :, None].astype(np.float32)
        embs.append(emb_session.run(None, {"input_1": window_frames})[0].reshape(96))
    write(
        ROOT / "pkg/kws/testdata/golden.bin",
        [("audio", audio), ("mel", mel), ("emb", np.stack(embs))],
    )
    print(f"steps {steps}, mel {mel.shape}, embeddings {len(embs)}")


if __name__ == "__main__":
    main(Path(sys.argv[1]))
