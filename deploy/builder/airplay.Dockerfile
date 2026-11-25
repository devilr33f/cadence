# the airplay runtime (M4d-D1, M4d-R4), built natively for the target platform: amd64, or under emulation arm64 for
# pickle (make airplay-runtime ARCH=arm64) and armhf for midi (ARCH=armhf, linux/arm/v7: M2m-D6). the runtime is named
# for the image's debian architecture. shairport sync and nqptp from their pinned tarballs, avahi from debian's
# pinned source package (M4d-R5) and our lgpl ffmpeg; assemble-airplay-runtime.sh takes everything else from pinned
# trixie debs and gates the tree. the ffmpeg comes from the named context ffmpeg-airplay
# (build/prebuilt/ffmpeg-airplay-<arch>, ensured by deploy/builder/prebuilt.sh), so a cache hit compiles no ffmpeg
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS base

# apt from the base image's snapshot and the build's packages (airplay-build-deps.sh, which device/Dockerfile.sim runs in
# the same steps for the sim's runtime, so the two share their cache)
COPY deploy/builder/airplay-build-deps.sh /tmp/airplay-build-deps.sh
RUN sh /tmp/airplay-build-deps.sh && rm /tmp/airplay-build-deps.sh

# our lgpl ffmpeg (M4b) plus the alac decoder, alone for the prebuilt cache (prebuilt.sh build
# ffmpeg-airplay-<arch>): shairport sync 5.6 decodes alac with ffmpeg, aac as well. the runtime's stages never build
# this one: they take the tar from the named context ffmpeg-airplay, checked against the sha256 prebuilt.sh put
# beside it
FROM base AS ffmpeg-build
COPY deploy/builder/build-ffmpeg.sh /tmp/build-ffmpeg.sh
RUN sh /tmp/build-ffmpeg.sh /opt/airplay --enable-decoder=alac && rm /tmp/build-ffmpeg.sh \
  && mkdir /out \
  && tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -cf /out/ffmpeg.tar -C /opt airplay
FROM scratch AS ffmpeg-export
COPY --from=ffmpeg-build /out/ffmpeg.tar /

FROM base AS build
COPY --from=ffmpeg-airplay /ffmpeg.tar /in/ffmpeg.tar
COPY --from=ffmpeg-airplay /ffmpeg.tar.sha256 /in/ffmpeg.tar.sha256
RUN cd /in && sha256sum --check --strict ffmpeg.tar.sha256 \
  && tar -xf ffmpeg.tar -C /opt && rm -rf /in
ENV AIRPLAY_SOURCES=/opt/airplay-sources
COPY deploy/builder/build-airplay.sh deploy/builder/airplay-runtime.pins /w/deploy/builder/
RUN sh /w/deploy/builder/build-airplay.sh /opt/airplay /w/deploy/builder/airplay-runtime.pins

# the runtime, its listing, and the tree as the tar unpacks for the smoke stage, with the loader under a fixed name
FROM build AS runtime
ENV AIRPLAY_DEBS=/opt/airplay-debs
COPY deploy/builder/assemble-airplay-runtime.sh deploy/builder/assemble-airplay-runtime.test.sh /w/deploy/builder/
RUN arch=$(dpkg --print-architecture) \
  && bash /w/deploy/builder/assemble-airplay-runtime.sh "$arch" /opt/airplay "/out/airplay-runtime-$arch.tar" \
  && tar -tvf "/out/airplay-runtime-$arch.tar" --numeric-owner \
  && mkdir -p /smoke/runtime \
  && tar -xf "/out/airplay-runtime-$arch.tar" -C /smoke/runtime \
  && cp /smoke/runtime/lib/ld-linux-* /smoke/ld.so

FROM runtime AS test
RUN arch=$(dpkg --print-architecture) \
  && bash /w/deploy/builder/assemble-airplay-runtime.test.sh "$arch" /opt/airplay "/out/airplay-runtime-$arch.tar"

# the four programs as audiod starts them, in an image of nothing else: no library of a host can stand in
FROM scratch AS smoke
COPY --from=runtime /smoke/ /
ENV LD_BIND_NOW=1
RUN ["/ld.so", "--library-path", "/runtime/lib", "/runtime/bin/shairport-sync", "-V"]
RUN ["/ld.so", "--library-path", "/runtime/lib", "/runtime/bin/nqptp", "-V"]
RUN ["/ld.so", "--library-path", "/runtime/lib", "/runtime/bin/dbus-daemon", "--version"]
RUN ["/ld.so", "--library-path", "/runtime/lib", "/runtime/bin/avahi-daemon", "--version"]
COPY --from=test /out/ /out/

# make airplay-runtime exports this: the tar, once its tests and the smoke passed
FROM scratch AS export
COPY --from=smoke /out/ /
