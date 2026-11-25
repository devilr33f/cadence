#!/usr/bin/env python3
# a visualizer render's analysis as a timeline image (viz-render --features): what drove each frame, to line up with
# the song by ear. usage: plot-features.py <features.csv> <out.png> [title]; needs matplotlib
import csv
import sys

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402


def main() -> None:
    with open(sys.argv[1]) as fh:
        rows = [r for r in csv.DictReader(fh) if None not in r.values() and all(r.values())]
    col = {k: [float(r[k]) for r in rows] for k in rows[0]}
    t = col["t"]
    fig, ax = plt.subplots(4, 1, figsize=(max(12, len(t) / 120), 9), sharex=True,
                           gridspec_kw={"height_ratios": [2, 2, 1.4, 1]})
    title = sys.argv[3] if len(sys.argv) > 3 else ""
    fig.suptitle(title)

    a = ax[0]
    a.set_title("beats: each kick's pulse (what punches), strong beats marked", loc="left", fontsize=9)
    a.plot(t, col["kick_pulse"], color="#d9480f", lw=0.8, label="pulse")
    kicks = [(x, p) for x, k, p in zip(t, col["kick"], col["kick_pulse"]) if k > 0]
    a.vlines([x for x, _ in kicks], 0, [p for _, p in kicks], color="#d9480f", lw=1.2, alpha=0.7)
    strong = [x for x, b in zip(t, col["beat"]) if b > 0]
    a.plot(strong, [1.05] * len(strong), "v", color="#c92a2a", ms=3, label="strong beat")
    a.set_ylim(0, 1.15)
    a.legend(loc="upper right", fontsize=8)

    a = ax[1]
    a.set_title("levels against the track (0..1)", loc="left", fontsize=9)
    a.plot(t, col["energy"], color="#1c7ed6", lw=1, label="energy")
    a.plot(t, col["bass"], color="#5f3dc4", lw=0.8, label="bass")
    a.plot(t, col["mid"], color="#2b8a3e", lw=0.6, alpha=0.7, label="mid")
    a.plot(t, col["treble"], color="#e67700", lw=0.6, alpha=0.7, label="treble")
    a.set_ylim(0, 1.05)
    a.legend(loc="upper right", fontsize=8, ncol=4)

    a = ax[2]
    a.set_title("bass presence (0: a build-up or breakdown), build-up, drop intensity and its hit", loc="left", fontsize=9)
    a.fill_between(t, col["bass_presence"], color="#5f3dc4", alpha=0.25, lw=0, label="bass presence")
    a.plot(t, col["build"], color="#e8590c", lw=1.2, label="build-up")
    a.plot(t, col["intensity"], color="#0b7285", lw=1, label="intensity")
    if "drop_pulse" in col:
        a.plot(t, col["drop_pulse"], color="#c2255c", lw=1.2, label="drop")
    if "drop_level" in col:
        a.plot(t, col["drop_level"], color="#c2255c", lw=1, alpha=0.5, ls="--", label="drop section")
    a.set_ylim(0, 1.05)
    a.legend(loc="upper right", fontsize=8, ncol=3)

    a = ax[3]
    a.set_title("tempo (bpm, 0 while none holds)", loc="left", fontsize=9)
    a.plot(t, col["bpm"], color="#495057", lw=1)
    a.set_xlabel("s")
    top = max(col["bpm"]) if max(col["bpm"]) > 0 else 200
    a.set_ylim(0, top * 1.1)

    for a in ax:
        a.grid(axis="x", alpha=0.25)
        a.set_xlim(t[0], t[-1])
    fig.tight_layout()
    fig.savefig(sys.argv[2], dpi=110)


if __name__ == "__main__":
    main()
