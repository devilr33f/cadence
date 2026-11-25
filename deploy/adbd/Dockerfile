# the adb server the device-update service speaks to (deploy/adbd/README.md): no adb binary
# anywhere in our own code, just this sidecar owning the key the devices already trust
FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends adb \
    && rm -rf /var/lib/apt/lists/*
USER 65534
ENV HOME=/tmp
EXPOSE 5037
# adb generates a random user key when $HOME/.android/adbkey is missing and presents it first, so
# every probed device would see an unknown fingerprint; the mounted owner key (/keys/adbkey, the
# secret) becomes the user key and the one identity offered
ENTRYPOINT ["/bin/sh", "-c", "mkdir -p $HOME/.android && ln -sf /keys/adbkey $HOME/.android/adbkey && exec adb -a nodaemon server"]
