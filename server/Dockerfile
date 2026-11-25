FROM golang:1.27-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80 AS build
RUN apt-get update \
  && apt-get install --yes --no-install-recommends libopus-dev pkg-config \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.work go.work.su[m] ./
COPY pkg/ pkg/
COPY server/ server/
COPY device/ device/
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod,id=cadence-gomod,sharing=locked \
  --mount=type=cache,target=/root/.cache/go-build,id=cadence-gobuild-server,sharing=locked \
  cd server && CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/cadence-server ./cmd/cadence-server
# libopus must be linked in: the runtime image has none
RUN ! ldd /out/cadence-server | grep -q libopus

FROM build AS test-cgo
RUN cd server && go test ./internal/audio/... ./internal/providers/uniproxy/...

# yt-dlp for the yt-dlp music player: python-build-standalone (its readline module is built on libedit, so no gnu
# readline) from uv, which checks the download's sha-256, then the hash-locked wheels of server/ytdlp, and deno, the
# javascript runtime yt-dlp needs for youtube's challenges, pinned by sha-256. a yt-dlp update is a new pin in
# requirements.txt (uv pip compile) and a new image
FROM ghcr.io/astral-sh/uv:0.13.7-python3.14-trixie-slim@sha256:e80c05a310400cfc90af00b5b7271ef9ad24816e3265e1162af8a678e1aad099 AS ytdlp
ARG TARGETARCH
ARG PYTHON_VERSION=3.14.10
ARG DENO_VERSION=2.9.8
ENV UV_PYTHON_INSTALL_DIR=/opt/python UV_PYTHON_PREFERENCE=only-managed UV_NO_CACHE=1
RUN apt-get update \
  && apt-get install --yes --no-install-recommends curl ca-certificates unzip \
  && rm -rf /var/lib/apt/lists/*
COPY server/ytdlp/requirements.txt /tmp/requirements.txt
RUN uv python install "$PYTHON_VERSION" \
  && uv venv --python "$PYTHON_VERSION" /opt/yt-dlp \
  && uv pip install --python /opt/yt-dlp/bin/python --require-hashes --no-deps --only-binary :all: -r /tmp/requirements.txt \
  && /opt/yt-dlp/bin/yt-dlp --version
RUN set -eu; \
  case "$TARGETARCH" in \
    amd64) deno=deno-x86_64-unknown-linux-gnu.zip; sum=e890975bc2549807bbcc0b584ffafda72cc7c93349dd99c80483dd784f6c4609 ;; \
    arm64) deno=deno-aarch64-unknown-linux-gnu.zip; sum=d49596c20b9788cf8f5d8ddf644103d4bde56ed239753f0bdb92ba9361d51905 ;; \
    *) echo "no deno for $TARGETARCH" >&2; exit 1 ;; \
  esac; \
  curl -fsSLo /tmp/deno.zip "https://github.com/denoland/deno/releases/download/v${DENO_VERSION}/$deno"; \
  echo "$sum  /tmp/deno.zip" | sha256sum -c -; \
  mkdir -p /opt/deno; unzip -q /tmp/deno.zip -d /opt/deno; rm /tmp/deno.zip; \
  /opt/deno/deno --version

# the ring sounds' ffmpeg (server/internal/ringsounds): one static lgpl program, cross-built with zig on the build
# machine's own cpu for the image's (deploy/builder/build-server-ffmpeg.sh), so nothing compiles under emulation.
# ffmpeg-server is what the image takes: /ffmpeg.tar and its sha256. a build given the named context ffmpeg-server
# (build/prebuilt/ffmpeg-server-<arch>, which deploy/builder/prebuilt.sh fetched from the registry or built once)
# takes that in place of this stage, so a cache hit compiles nothing; a plain build compiles it here
FROM --platform=$BUILDPLATFORM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS ffmpeg-server-build
ARG BUILDARCH
ARG TARGETARCH
RUN apt-get update \
  && apt-get install --yes --no-install-recommends ca-certificates curl xz-utils make nasm pkg-config \
    binutils-x86-64-linux-gnu binutils-aarch64-linux-gnu \
  && rm -rf /var/lib/apt/lists/*
# zig 0.15.2 for the build machine's cpu, pinned by its release's sha256 (device.Dockerfile's), in /opt/zig
RUN set -e; case $BUILDARCH in \
    amd64) zig=zig-x86_64-linux-0.15.2 sum=02aa270f183da276e5b5920b1dac44a63f1a49e55050ebde3aecc9eb82f93239 ;; \
    arm64) zig=zig-aarch64-linux-0.15.2 sum=958ed7d1e00d0ea76590d27666efbf7a932281b3d7ba0c6b01b0ff26498f667f ;; \
    *) echo "no zig pinned for $BUILDARCH" >&2; exit 1 ;; \
  esac; \
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output /tmp/zig.tar.xz \
    "https://ziglang.org/download/0.15.2/$zig.tar.xz"; \
  echo "$sum  /tmp/zig.tar.xz" | sha256sum --check --strict -; \
  tar -xJf /tmp/zig.tar.xz -C /opt; rm /tmp/zig.tar.xz; mv "/opt/$zig" /opt/zig; \
  ln -s /opt/zig/zig /usr/local/bin/zig; zig version
COPY deploy/builder/build-ffmpeg.sh deploy/builder/build-server-ffmpeg.sh /w/
RUN sh /w/build-server-ffmpeg.sh /opt/ffmpeg "$TARGETARCH" \
  && mkdir /out \
  && tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -cf /out/ffmpeg.tar -C /opt ffmpeg \
  && cd /out && sha256sum ffmpeg.tar >ffmpeg.tar.sha256
FROM scratch AS ffmpeg-server
COPY --from=ffmpeg-server-build /out/ /

# the tar checked against the sha256 beside it, whichever of the two gave it
FROM --platform=$BUILDPLATFORM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS ffmpeg
COPY --from=ffmpeg-server /ffmpeg.tar /ffmpeg.tar.sha256 /in/
RUN cd /in && sha256sum --check --strict ffmpeg.tar.sha256 && tar -xf ffmpeg.tar -C /opt && rm -rf /in \
  && mkdir -p /opt/wakeword

FROM gcr.io/distroless/cc-debian13:nonroot@sha256:08476c244888bfb425bae1ae4dd7c17f093e972aab8426883d97f3c6f280f7c1
COPY --from=build /out/cadence-server /cadence-server
COPY --from=ytdlp /opt/python /opt/python
COPY --from=ytdlp /opt/yt-dlp /opt/yt-dlp
COPY --from=ytdlp /opt/deno/deno /usr/local/bin/deno
COPY --from=ffmpeg /opt/ffmpeg/bin/ffmpeg /usr/local/bin/ffmpeg
COPY --from=ffmpeg /opt/ffmpeg/share/licenses/ffmpeg /licenses/ffmpeg
# the yt-dlp player runs it; /tmp must be writable (compose mounts a tmpfs on a read-only root)
ENV CADENCE_YTDLP=/opt/yt-dlp/bin/yt-dlp
# alarm music's transcoder: song on stdin, sound on stdout, nothing written
ENV CADENCE_FFMPEG=/usr/local/bin/ffmpeg
# the custom wake word's negatives (CADENCE_WAKEWORD_NEGATIVES): a volume there starts owned by nonroot
COPY --from=ffmpeg --chown=65532:65532 /opt/wakeword /var/lib/cadence/wakeword
USER nonroot
ENTRYPOINT ["/cadence-server"]
