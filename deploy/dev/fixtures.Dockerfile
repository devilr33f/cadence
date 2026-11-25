FROM golang:1.27-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80
RUN apt-get update \
  && apt-get install --yes --no-install-recommends espeak-ng ffmpeg libopus-dev pkg-config \
  && rm -rf /var/lib/apt/lists/*
