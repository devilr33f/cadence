SHELL := /bin/sh

.PHONY: fixtures proto proto-nanopb proto-check sqlc sqlc-check openapi openapi-check dashboard-client dashboard-check lint test test-go test-386 test-linux test-cgo media-test media-test-privileged airplay-runtime device-limits-test device-build dist libairplay mpv tools-test ha-test ha-package ha-image cross dev-up dev-down e2e live-turn ci screen-sim screen-sim-test screen-sim-run sim prebuilt-publish proto-buf-check proto-nanopb-check server-image kws-assets

BUF_IMAGE := bufbuild/buf:1.75.0@sha256:adf82c7170be6629ec81dc907921ac1699407cd11c4f7fd7521b13c74152f849
LINT_IMAGE := golangci/golangci-lint:v2.15.0@sha256:fbfcfd12a6f8d7f1b97b3362ca80fc2a244d46863148c73cb88e7793af96c481
GO_IMAGE := golang:1.27-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80
NANOPB_IMAGE := python@sha256:09a17cad02668ae61ece136b4fd36ac3849e14f670ebedb637ea3e15580525bc
NANOPB_VERSION := 0.4.9.1
SQLC_IMAGE := sqlc/sqlc:1.31.1@sha256:70f53171d27b2424e9358869975455a6e955a5aa8e58a998a270a6e34e525537
NODE_IMAGE := node:24.21.0-trixie-slim@sha256:173f125896c3b47ddf056734c7ea789d04595a6a08769a8f78e0df642781fb66
BUF :=deploy/dev/in-docker.sh $(BUF_IMAGE) buf


proto: proto-nanopb
	$(BUF) generate

# nanopb output needs the .options file, which buf remote plugins cannot read; one run per options file, as a second -f
# replaces the first. screen/v1 takes the cadence.v2 files it imports along, under its own options
proto-nanopb:
	deploy/dev/in-docker.sh $(NANOPB_IMAGE) sh -c "pip install --quiet --disable-pip-version-check --target /tmp/np nanopb==$(NANOPB_VERSION) grpcio-tools==1.76.0 && PYTHONPATH=/tmp/np python -m nanopb.generator.nanopb_generator -q -I proto -D media/gen -f proto/cadence/audiod/v1/audiod.options cadence/audiod/v1/audiod.proto && PYTHONPATH=/tmp/np python -m nanopb.generator.nanopb_generator -q -I proto -D media/gen -f proto/cadence/screen/v1/screen.options cadence/screen/v1/screen.proto cadence/screen/v1/ui.proto cadence/screen/v1/media.proto cadence/v2/assistant.proto cadence/v2/rpc.proto cadence/v2/screen.proto cadence/v2/media.proto cadence/v2/session.proto && PYTHONPATH=/tmp/np python -m nanopb.generator.nanopb_generator -q -I proto -D media/gen -f proto/cadence/cards/v1/cards.options cadence/cards/v1/cards.proto && PYTHONPATH=/tmp/np python -m nanopb.generator.nanopb_generator -q -I proto -D media/gen -f proto/cadence/sync/v1/sync.options cadence/sync/v1/sync.proto"

# the custom wake word's feature models (pkg/kws): openWakeWord's released onnx models, sha256-pinned, converted to
# pkg/kws/assets/features.bin, and the golden vectors onnxruntime gives for pkg/kws's tests; all in the pinned python
# image, the downloads cached in build/kws. only after a change to tools/kws/export.py
kws-assets:
	mkdir -p build/kws
	deploy/dev/in-docker.sh $(NANOPB_IMAGE) sh -c "pip install --quiet --disable-pip-version-check --target /tmp/kws numpy==2.3.4 onnx==1.19.1 onnxruntime==1.23.2 && PYTHONPATH=/tmp/kws python -P tools/kws/export.py build/kws"

# two halves, one per image: ci runs each directly in its own image (IN_DOCKER_DIRECT), and buf's has no python
proto-check: proto-buf-check proto-nanopb-check

# buf format cannot exclude a module dir: --path proto/cadence checks our protos only, so a new proto root of ours
# needs its own --path (the vendored proto/third_party stays as upstream wrote it)
proto-buf-check:
	$(BUF) lint
	$(BUF) format -d --exit-code --path proto/cadence
	$(BUF) generate
	git diff --exit-code -- pkg/gen
	@test -z "$$(git status --porcelain -- pkg/gen)" || { echo "pkg/gen has untracked changes"; exit 1; }

proto-nanopb-check: proto-nanopb
	git diff --exit-code -- media/gen
	@test -z "$$(git status --porcelain -- media/gen)" || { echo "media/gen has untracked changes"; exit 1; }

sqlc:
	deploy/dev/in-docker.sh $(SQLC_IMAGE) /workspace/sqlc generate -f server/sqlc.yaml

sqlc-check: sqlc
	git diff --exit-code -- server/internal/store/db
	@test -z "$$(git ls-files --others --exclude-standard -- server/internal/store/db)" || { echo "server/internal/store/db has untracked changes"; exit 1; }

# the admin api's OpenAPI document, generated from the registered operations without a database; openapi-check
# regenerates it beside the committed one and fails on any difference
openapi:
	go run ./server/cmd/cadence-server admin openapi -o server/openapi/openapi.json

openapi-check:
	@mkdir -p build
	go run ./server/cmd/cadence-server admin openapi -o build/openapi.json
	@cmp -s build/openapi.json server/openapi/openapi.json || { echo "server/openapi/openapi.json is stale: run make openapi"; exit 1; }

# the dashboard's caches: pnpm's store and metadata and the pnpm corepack fetched, in one volume at the node image's
# /var/tmp (mode 1777, which a fresh volume takes over, so the wrapper's --user can write it); pnpm's store is safe for
# concurrent use, so parallel worktrees share it
NODE_CACHE_FLAGS := -v cadence-pnpm-cache:/var/tmp -e PNPM_CONFIG_STORE_DIR=/var/tmp/pnpm-store \
	-e PNPM_CONFIG_CACHE_DIR=/var/tmp/pnpm-cache -e COREPACK_HOME=/var/tmp/corepack -e COREPACK_ENABLE_DOWNLOAD_PROMPT=0
IN_DOCKER_NODE := IN_DOCKER_FLAGS="$(NODE_CACHE_FLAGS) $${IN_DOCKER_FLAGS:-}" deploy/dev/in-docker.sh

# the dashboard's api client (dashboard/src/api/gen), generated from the committed OpenAPI document
dashboard-client:
	$(IN_DOCKER_NODE) $(NODE_IMAGE) sh deploy/dev/dashboard.sh client

# the dashboard's gate: frozen install, lint, typecheck, unit tests, build, and the client regenerated beside the
# committed one (deploy/dev/dashboard.sh)
dashboard-check:
	$(IN_DOCKER_NODE) $(NODE_IMAGE) sh deploy/dev/dashboard.sh check

# the go and golangci-lint caches of the go gates that run in docker (lint, test-linux), kept in one volume so a run
# downloads and builds only what changed: go's caches are safe for concurrent use, so parallel worktrees share it. it
# sits at the go images' /go (mode 1777, which a fresh volume takes over), so the wrapper's --user can write it
GO_CACHE_FLAGS := -v cadence-go-cache:/go -e GOCACHE=/go/cache/build -e GOLANGCI_LINT_CACHE=/go/cache/golangci-lint
IN_DOCKER_GO := IN_DOCKER_FLAGS="$(GO_CACHE_FLAGS) $${IN_DOCKER_FLAGS:-}" deploy/dev/in-docker.sh

# without cgo: the lint image has no libopus headers, so it checks the audio stub, not opus_cgo.go
lint:
	$(IN_DOCKER_GO) $(LINT_IMAGE) sh -c 'for m in pkg server device; do (cd "$$m" && CGO_ENABLED=0 golangci-lint run ./...) || exit 1; done'

# testcontainers reaches docker desktop on windows only through its pipe (it refuses the default as rootless docker)
ifeq ($(OS),Windows_NT)
TEST_DOCKER_HOST := DOCKER_HOST=$${DOCKER_HOST:-npipe:////./pipe/docker_engine}
endif

test: test-go test-386

# ryuk fails on docker desktop for windows; the tests clean up their own containers
test-go:
	$(TEST_DOCKER_HOST) TESTCONTAINERS_RYUK_DISABLED=$${TESTCONTAINERS_RYUK_DISABLED:-true} go test ./pkg/... ./server/... ./device/...

# the log pipeline's scrub window again with a 32-bit int (midi's armv7 has one), as 386 on the amd64 host. ci runs it
# apart from test-go: the elevated runner's kata kernel runs no 32-bit binary
test-386:
	CGO_ENABLED=0 GOARCH=386 go test ./device/internal/telemetry/logs/

# with the race detector: the device tests that need linux (unixpacket, audiod, fifos, colons in file names), which
# make test skips elsewhere, and the device packages that run goroutines of their own or are shared between them.
# -short skips the busybox tests: the image has no docker cli, and make test runs them
test-linux:
	$(IN_DOCKER_GO) $(GO_IMAGE) sh -c 'export GOFLAGS=-buildvcs=false CGO_ENABLED=1 && cd device && go test -race -short ./internal/alarms/... ./internal/audiod/... ./boards/sim/... ./internal/session/... ./internal/media/... ./internal/core/... ./internal/volume/... ./internal/soloist/... ./internal/airplay/... ./internal/bluetooth/... ./internal/leds/... ./internal/input/... ./internal/evdev/... ./boards/yandex-pickle/... ./boards/yandex-midi/... ./internal/telemetry/... ./internal/discovery/... ./internal/screen/... ./boards/sim-tv/... ./boards/rpi/... ./boards/android-tv/...'

# a build whose steps are the tests: buildkit runs a step again whenever anything it copies or installs changed, and
# otherwise takes its passed run from its cache; no image is written (that took test-cgo 16 s)
TEST_BUILD := docker build --progress=plain --output type=cacheonly

# the libopus encoder tests, in the server image's cgo build stage
test-cgo:
	$(TEST_BUILD) -f server/Dockerfile --target test-cgo .

tools-test:
	sh deploy/dev/in-docker.test.sh
	sh deploy/dev/jq-native.test.sh
	sh deploy/dev/ci.test.sh
	sh deploy/dev/dashboard-deploy.test.sh
	sh deploy/quickstart/setup.test.sh
	# the github mirror's sync lives on gitlab alone: the mirror has no deploy/github/
	if [ -f deploy/github/sync.test.sh ]; then sh deploy/github/sync.test.sh; fi
	bash deploy/builder/apply-patches.test.sh
	bash deploy/builder/dist.test.sh
	bash deploy/builder/prebuilt.test.sh

# the prebuilt cache's publish job (.gitlab-ci.yml): every artifact ensured, publishing each one it had to build
# (the job token authenticates). a manual run on main after the recipes change
prebuilt-publish:
	PREBUILT_PUBLISH=1 bash deploy/builder/prebuilt.sh ensure-all

# the home assistant component (M8-D10): ruff (lint and format check) and pytest, in the pinned python image with uv and
# the exact pins of integrations/home_assistant/uv.lock. uv's cache sits in the docker volume cadence-uv-cache at the
# image's /var/tmp (mode 1777, which a fresh volume takes over, so the wrapper's --user can write it), shared with other
# worktrees (uv locks it); the environment lives in the container, and nothing is written into the tree. e2e.sh reads
# HA_IMAGE from its line here for its home assistant client block
HA_IMAGE := ghcr.io/astral-sh/uv:0.13.7-python3.14-trixie-slim@sha256:e80c05a310400cfc90af00b5b7271ef9ad24816e3265e1162af8a678e1aad099
HA_FLAGS := -v cadence-uv-cache:/var/tmp -e UV_CACHE_DIR=/var/tmp/uv -e UV_PROJECT_ENVIRONMENT=/tmp/ha-venv -e UV_LINK_MODE=copy \
	-e UV_PYTHON_DOWNLOADS=never -e PYTHONDONTWRITEBYTECODE=1 -e RUFF_CACHE_DIR=/tmp/ruff-cache
HA_CHECKS := uv sync --locked --quiet && uv run --no-sync ruff check . && uv run --no-sync ruff format --check . && \
	uv run --no-sync pytest -q

ha-test:
	IN_DOCKER_FLAGS="$(HA_FLAGS) $${IN_DOCKER_FLAGS:-}" deploy/dev/in-docker.sh $(HA_IMAGE) sh -c 'cd integrations/home_assistant && $(HA_CHECKS)'

# the component as a zip for a manual install (M8-D11): build/ha/cadence-ha-<manifest version>.zip, which holds
# custom_components/cadence (no bytecode or hidden file) and unpacks in home assistant's config directory; the same
# files make the same bytes. the script is stdlib alone, run by the image's python
ha-package:
	deploy/dev/in-docker.sh $(HA_IMAGE) python3 -B integrations/home_assistant/scripts/package.py \
		integrations/home_assistant/custom_components/cadence build/ha

# an init-container image: busybox and the component at /cadence. native only; a push is always explicit
HA_COMPONENT_IMAGE ?= registry.example.com/cadence-home-assistant
HA_IMAGE_TAG ?= dev
HA_IMAGE_PUSH ?= 0
ha-image:
	docker build -f deploy/home_assistant/Dockerfile -t $(HA_COMPONENT_IMAGE):$(HA_IMAGE_TAG) .
	@if [ "$(HA_IMAGE_PUSH)" = 1 ]; then docker push $(HA_COMPONENT_IMAGE):$(HA_IMAGE_TAG); \
	elif [ "$(HA_IMAGE_PUSH)" != 0 ]; then echo "HA_IMAGE_PUSH must be 0 or 1" >&2; exit 2; fi

# no-cgo builds for the speaker cpus, and cadence-install for the owner's machine, output discarded
cross:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /dev/null ./device/cmd/cadenced
	GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -o /dev/null ./device/cmd/cadenced
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./device/cmd/cadence-install
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./device/cmd/cadence-install

FIXTURES_IMAGE := cadence-fixtures:dev

# the tools (espeak-ng, ffmpeg, libopus) are only used in the image; only the .opusframes and the .wav (audiod's
# file capture) are committed, both from one espeak run, the url tests' flac: 2 s at 44.1 kHz, 440 Hz left and
# 660 Hz right, the e2e's music: 8 s at 44.1 kHz, 1000 Hz left and 1500 Hz right at -12 dBFS (0.2511886), and the
# multiroom block's track: 120 s of the same tones as 48 kHz opus at 24 kbps cbr (its pause, resume and rejoin outlast
# the music's 8 s; opus keeps it small)
fixtures:
	docker build -f deploy/dev/fixtures.Dockerfile -t $(FIXTURES_IMAGE) deploy/dev
	deploy/dev/in-docker.sh $(FIXTURES_IMAGE) sh -c 'export GOCACHE=/tmp/gocache CGO_ENABLED=1 && espeak-ng -v en-us -s 160 -w /tmp/q22.wav "What time is it?" && ffmpeg -y -i /tmp/q22.wav -af aresample=resampler=soxr -ar 16000 -ac 1 -sample_fmt s16 /tmp/q.wav && go run ./server/tools/opusframes -in /tmp/q.wav -out device/boards/sim/fixtures/what-time.opusframes && cp /tmp/q.wav device/boards/sim/fixtures/what-time.wav'
	deploy/dev/in-docker.sh $(FIXTURES_IMAGE) sh -c 'mkdir -p media/tests/fixtures && ffmpeg -y -loglevel error -f lavfi -i "aevalsrc=0.5*sin(2*PI*440*t)|0.5*sin(2*PI*660*t):s=44100:d=2" -c:a flac -sample_fmt s16 -map_metadata -1 -fflags +bitexact -flags:a +bitexact media/tests/fixtures/tone-44k.flac'
	deploy/dev/in-docker.sh $(FIXTURES_IMAGE) sh -c 'mkdir -p deploy/dev/fixtures && ffmpeg -y -loglevel error -f lavfi -i "aevalsrc=0.2511886*sin(2*PI*1000*t)|0.2511886*sin(2*PI*1500*t):s=44100:d=8" -c:a flac -sample_fmt s16 -map_metadata -1 -fflags +bitexact -flags:a +bitexact deploy/dev/fixtures/music.flac'
	deploy/dev/in-docker.sh $(FIXTURES_IMAGE) sh -c 'mkdir -p deploy/dev/fixtures && ffmpeg -y -loglevel error -f lavfi -i "aevalsrc=0.2511886*sin(2*PI*1000*t)|0.2511886*sin(2*PI*1500*t):s=48000:d=120" -c:a libopus -b:a 24k -vbr off -map_metadata -1 -fflags +bitexact -flags:a +bitexact deploy/dev/fixtures/group.ogg'

# the media builder builds on the host's own cpu: its ffmpeg artifact is keyed by that debian architecture
HOST_DEB_ARCH := $(if $(filter arm64 aarch64,$(shell uname -m)),arm64,amd64)
FFMPEG_MEDIAHOST := --build-context ffmpeg-mediahost=build/prebuilt/ffmpeg-mediahost-$(HOST_DEB_ARCH)

# the host build and its tests, then soloist's runtime for arm64 and amd64 with its script's tests, then libairplay for
# the screens' three targets (linux amd64 and arm64, android armeabi-v7a) with its test run on this machine's cpu.
# the host build's ffmpeg is ensured from the prebuilt registry first (deploy/builder/prebuilt.sh), so a cache hit
# compiles no ffmpeg
# MEDIA_CTEST_ARGS selects the host and sim configurations' suites (ctest's -R or -E with a pattern, no spaces in it)
media-test:
	bash deploy/builder/prebuilt.sh ensure ffmpeg-mediahost-$(HOST_DEB_ARCH)
	$(TEST_BUILD) -f deploy/builder/media-host.Dockerfile --target test $(FFMPEG_MEDIAHOST) \
		--build-arg "MEDIA_CTEST_ARGS=$(MEDIA_CTEST_ARGS)" .
	$(TEST_BUILD) -f deploy/builder/media-host.Dockerfile --target soloist-runtime .
	$(TEST_BUILD) -f deploy/builder/libairplay.Dockerfile --target test .

# one tag per worktree, so parallel lanes never run each other's build
MEDIA_PRIVILEGED_IMAGE := cadence-media-privileged:$(notdir $(CURDIR))

# soloist's jail for real: its namespace and mounts need CAP_SYS_ADMIN, which no docker build step has
media-test-privileged:
	bash deploy/builder/prebuilt.sh ensure ffmpeg-mediahost-$(HOST_DEB_ARCH)
	docker build --progress=plain -f deploy/builder/media-host.Dockerfile --target privileged $(FFMPEG_MEDIAHOST) \
		-t $(MEDIA_PRIVILEGED_IMAGE) .
	docker run --rm --privileged $(MEDIA_PRIVILEGED_IMAGE) ctest -L privileged --no-tests=error --output-on-failure

# cadence-screen (tv wave 1, stream a): its offscreen test harness (screen/docker/test.Dockerfile: sdl's offscreen
# driver on mesa llvmpipe, libmpv with ao=null, so nothing sounds) on the host's own cpu. screen-cross builds it alone
# for both linux cpus, the other one under emulation: slow, so it is a gate of its own, run once on merged changes
.PHONY: screen-test screen-cross
screen-test:
	$(TEST_BUILD) -f screen/docker/test.Dockerfile --target test .

screen-cross:
	$(TEST_BUILD) --platform linux/amd64 -f screen/docker/test.Dockerfile --target build .
	$(TEST_BUILD) --platform linux/arm64 -f screen/docker/test.Dockerfile --target build .

# the screen simulator's native (macos/linux) build: homebrew's cmake, ninja, sdl3 and freetype, rmlui and stb from
# their pinned sources into build/screen-sim/deps, then the screen with CADENCE_SCREEN_SIM=ON. no docker, no
# cross-build, nothing emulated; the wrapper is build/screen-sim/sim (screen/README.md, the simulator). projectM
# (milkdrop) is built there too from media/third_party/projectm's pins for desktop gl 4.1 core (about a minute, again
# only when its pins, patch or candidates change), with the candidate presets as the build's own milkdrop_dir()
SCREEN_SIM_RMLUI_VERSION := 6.3
SCREEN_SIM_RMLUI_SHA256 := d977298bb6147610e5984d5db85ddf284020d655a8713913f6982074f1dbdede
SCREEN_SIM_STB_SHA256 := 46b95a22bc60ff48d07836660a298c1c169ce4db3b9dff10de0df16632641a46

screen-sim:
	@command -v cmake > /dev/null || { echo "screen-sim needs cmake (brew install cmake)"; exit 1; }
	@command -v ninja > /dev/null || { echo "screen-sim needs ninja (brew install ninja)"; exit 1; }
	@pkg-config --exists sdl3 || { echo "screen-sim needs sdl3 (brew install sdl3)"; exit 1; }
	@pkg-config --exists freetype2 || { echo "screen-sim needs freetype (brew install freetype)"; exit 1; }
	mkdir -p build/screen-sim/deps
	@if [ ! -f build/screen-sim/deps/rmlui.stamp ]; then \
	  rm -rf build/screen-sim/deps/rmlui.tar.gz build/screen-sim/deps/RmlUi-$(SCREEN_SIM_RMLUI_VERSION) \
	    build/screen-sim/deps/rmlui-build build/screen-sim/deps/install; \
	  curl --fail --silent --show-error --location --output build/screen-sim/deps/rmlui.tar.gz \
	    "https://github.com/mikke89/RmlUi/archive/refs/tags/$(SCREEN_SIM_RMLUI_VERSION).tar.gz" \
	  && echo "$(SCREEN_SIM_RMLUI_SHA256)  build/screen-sim/deps/rmlui.tar.gz" | shasum -a 256 --check --strict - \
	  && tar -xzf build/screen-sim/deps/rmlui.tar.gz -C build/screen-sim/deps \
	  && cmake -S build/screen-sim/deps/RmlUi-$(SCREEN_SIM_RMLUI_VERSION) -B build/screen-sim/deps/rmlui-build -G Ninja \
	    -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DCMAKE_POSITION_INDEPENDENT_CODE=ON -DRMLUI_SAMPLES=OFF \
	    -DRMLUI_FONT_ENGINE=freetype -DRMLUI_PRECOMPILED_HEADERS=OFF \
	    -DCMAKE_INSTALL_PREFIX=$(CURDIR)/build/screen-sim/deps/install \
	  && cmake --build build/screen-sim/deps/rmlui-build \
	  && cmake --install build/screen-sim/deps/rmlui-build \
	  && echo ok > build/screen-sim/deps/rmlui.stamp \
	  || { echo "rmlui build failed"; exit 1; }; \
	fi
	@if [ ! -f build/screen-sim/deps/stb.stamp ]; then \
	  rm -rf build/screen-sim/deps/stb.tar.gz build/screen-sim/deps/stb-master; \
	  curl --fail --silent --show-error --location --output build/screen-sim/deps/stb.tar.gz \
	    "https://github.com/nothings/stb/archive/refs/heads/master.tar.gz" \
	  && echo "$(SCREEN_SIM_STB_SHA256)  build/screen-sim/deps/stb.tar.gz" | shasum -a 256 --check --strict - \
	  && tar -xzf build/screen-sim/deps/stb.tar.gz -C build/screen-sim/deps \
	  && mkdir -p build/screen-sim/deps/include/stb \
	  && cp build/screen-sim/deps/stb-master/stb_image.h build/screen-sim/deps/stb-master/stb_image_write.h \
	    build/screen-sim/deps/include/stb/ \
	  && rm -rf build/screen-sim/deps/stb-master build/screen-sim/deps/stb.tar.gz \
	  && echo ok > build/screen-sim/deps/stb.stamp \
	  || { echo "stb fetch failed"; exit 1; }; \
	fi
	@pm=$$(cat media/third_party/projectm/pins.conf media/third_party/projectm/patches/*.patch \
	    media/third_party/projectm/presets.candidates | shasum -a 256 | cut -d' ' -f1); \
	if [ "$$(cat build/screen-sim/deps/projectm.stamp 2>/dev/null)" != "$$pm" ]; then \
	  rm -rf build/screen-sim/deps/projectm build/screen-sim/deps/projectm-install build/screen-sim/deps/milkdrop; \
	  sh media/third_party/projectm/fetch.sh build/screen-sim/deps/projectm >/dev/null \
	  && sh media/third_party/projectm/build.sh build/screen-sim/deps/projectm/src build/screen-sim/deps/projectm/build \
	    $(CURDIR)/build/screen-sim/deps/projectm-install $$([ "$$(uname)" = Darwin ] && echo -DENABLE_GLES=OFF) \
	    > build/screen-sim/deps/projectm.log 2>&1 \
	  && sh media/third_party/projectm/fetch-presets.sh build/screen-sim/deps/milkdrop \
	    media/third_party/projectm/presets.candidates build/screen-sim/deps/projectm/dl \
	  && cp media/third_party/projectm/presets.candidates build/screen-sim/deps/milkdrop/presets.txt \
	  && echo "$$pm" > build/screen-sim/deps/projectm.stamp \
	  || { tail -40 build/screen-sim/deps/projectm.log 2>/dev/null; echo "projectm build failed"; exit 1; }; \
	fi
	cmake -S screen -B build/screen-sim/build -G Ninja -DCMAKE_BUILD_TYPE=RelWithDebInfo \
	  -DCADENCE_SCREEN_SIM=ON -DCADENCE_SCREEN_TESTS=OFF \
	  "-DCMAKE_PREFIX_PATH=$(CURDIR)/build/screen-sim/deps/install;$(CURDIR)/build/screen-sim/deps/projectm-install" \
	  -DCADENCE_SCREEN_PROJECTM=ON -DCADENCE_VIZ_MILKDROP_DIR=$(CURDIR)/build/screen-sim/deps/milkdrop \
	  -DCMAKE_CXX_FLAGS=-I$(CURDIR)/build/screen-sim/deps/include
	cmake --build build/screen-sim/build
	@printf '#!/bin/sh\nexec "$$(dirname "$$0")/build/sim/cadence-screen-sim" "$$@"\n' > build/screen-sim/sim
	@chmod +x build/screen-sim/sim
	@echo "screen-sim: build/screen-sim/sim (try: make screen-sim-run SCRIPT=screen/sim/examples/walkthrough.txt)"

# the simulator's own tests: the parser's unit test and the headless walkthrough with its checked screenshots
screen-sim-test: screen-sim
	cmake --build build/screen-sim/build --target sim-parser-test sim-smoke-test sim-seek-test sim-prepare-test
	ctest --test-dir build/screen-sim/build -R '^sim-' --output-on-failure

# runs a script through the simulator headless; screenshots land where the script puts them (sim-out/ in the examples)
screen-sim-run: screen-sim
	build/screen-sim/sim run $(SCRIPT)

# this machine as a screen device of a real server: cadenced (sim-tv) and the real cadence-screen with mpv, native;
# its state in ~/.config/cadence-sim (CADENCE_SIM_HOME), never the repo. the first run pairs: make sim SERVER=https://...
sim: screen-sim
	@pkg-config --exists mpv || { echo "sim needs mpv (brew install mpv)"; exit 1; }
	@test -x build/screen-sim/build/cadence-screen || { echo "sim: cadence-screen was not built"; exit 1; }
	mkdir -p build/sim
	CGO_ENABLED=0 go build -o build/sim/cadenced ./device/cmd/cadenced
	SERVER=$(SERVER) SIZE=$(SIZE) sh deploy/dev/sim-device.sh

# the ui's faces (screen/ui/fonts, committed with their licences): instanced, tnum-baked and subset from the pinned
# inter, jetbrains mono and lucide icons sources (pins.conf); run after an icon is added to screen/ui/tokens/tokens.json
SCREEN_UI_FONTS_IMAGE := cadence-ui-fonts:$(notdir $(CURDIR))
.PHONY: screen-ui-fonts
screen-ui-fonts:
	docker build -q -f screen/ui/fonts/fonts.Dockerfile -t $(SCREEN_UI_FONTS_IMAGE) . >/dev/null
	id=$$(docker create $(SCREEN_UI_FONTS_IMAGE)) && docker cp $$id:/out/. screen/ui/fonts/ && docker rm $$id >/dev/null

# the airplay runtime (M4d-D1) for ARCH, a debian architecture: amd64, or arm64 (pickle) and armhf (midi), both built
# under emulation: shairport sync and nqptp from their pinned tarballs, avahi from debian's pinned source package, the
# rest from pinned trixie debs, gated by its script, the script's tests and a smoke run of the four programs in an empty
# image; fetched from the prebuilt registry when its recipe hash is there (deploy/builder/prebuilt.sh, which runs the
# same docker build on a miss); out: build/airplay/airplay-runtime-<arch>.tar
ARCH ?= amd64

# docker's platform for a debian architecture
platform = linux/$(if $(filter armhf,$(1)),arm/v7,$(1))

airplay-runtime:
	bash deploy/builder/prebuilt.sh ensure airplay-runtime-$(ARCH)

# the server image for ARCH (default amd64), tagged cadence-server:$(SERVER_VERSION) in the local docker: its
# ffmpeg (server/internal/ringsounds, alarm music) ensured from the prebuilt registry first (ffmpeg-server-<arch>,
# cross-built with zig, so no emulation either way) and given as the named context ffmpeg-server; a plain docker build
# of server/Dockerfile compiles the same stage itself. manual, not in make ci
SERVER_VERSION ?= $(shell git describe --always --dirty --abbrev=12 --exclude '*')

server-image:
	bash deploy/builder/prebuilt.sh ensure ffmpeg-server-$(ARCH)
	docker buildx build --progress=plain --platform linux/$(ARCH) -f server/Dockerfile \
		--build-context ffmpeg-server=build/prebuilt/ffmpeg-server-$(ARCH) --build-arg VERSION=$(SERVER_VERSION) \
		-t cadence-server:$(SERVER_VERSION) --load .

# the tests of the device build's limit check and packing, natively with small elf files for this machine's cpu
device-limits-test:
	$(TEST_BUILD) -f deploy/builder/device.Dockerfile --target limits-test .

# a distro's artifact as its manifest (deploy/distros/<DISTRO>.json) says, by its package format (deploy/builder/dist.sh):
# a speaker's tarball for the installer (M2-D10, M2m-D6), manual and not in make ci: yandex-pickle (arm64) or
# yandex-midi (armhf). audiod with its ffmpeg builds for the board's arch under emulation; airplay's runtime is arm64 for
# both, as soloist's is from the media builder: midi's 64-bit kernel runs them, and its 4.9 kernel lacks the time64 calls
# an armhf glibc tries on every clock read; cadenced is cross-compiled; out: build/device/cadence-<version>-<board>-<arch>.tar.
# rpi builds its screen and bluetooth deb natively for linux/arm64 in trixie (deploy/rpi/README.md). sim builds its
# image; a screen (rpi's deb, android-tv's apk) builds its libairplay and mpv, then its builder. nothing but an image is
# tagged, so parallel worktrees share only the build cache
DISTRO ?= yandex-pickle
BOARD ?= yandex-pickle
DEVICE_VERSION ?= $(shell git describe --always --dirty --abbrev=12 --exclude '*')

dist:
	DIST_VERSION=$(DEVICE_VERSION) bash deploy/builder/dist.sh $(DISTRO)

# the board's tarball, as before make dist: the same builds, read from the board's manifest
device-build:
	$(MAKE) dist DISTRO=$(BOARD)

# libairplay for every screen target, to build/libairplay/<target>/ (lib/libairplay.a, include/airplay/, bin/airplay-test)
libairplay:
	docker buildx build --progress=plain -f deploy/builder/libairplay.Dockerfile --target export \
		--output type=local,dest=build/libairplay .

# mpv with the ao_cadence patch (owner decision 5), to build/mpv/<target>[-<arch>]/: MPV_TARGET deb-0.40 (trixie's, the
# pi's default) or deb-0.41 (forky's) as debs for MPV_ARCH (arm64 or amd64), or android-armeabi-v7a (libmpv.so for the
# jdtech aar, checked against it). fetched from the prebuilt registry first (deploy/builder/prebuilt.sh); outputs are
# kept in the docker volume cadence-mpv-cache by the hash of their recipe; MPV_REBUILD=1 builds again and fails unless
# the result equals the kept one, file by file
MPV_TARGET ?= deb-0.40
MPV_ARCH ?= arm64

mpv:
	sh deploy/builder/mpv.sh $(MPV_TARGET) $(MPV_ARCH)

# tv stream P: i18n, ambient, visualizers
.PHONY: i18n screen-p-test screen-p-gl-test

# the code made from the catalogs in i18n/ (pkg/i18n/servertext, screen/i18n/gen); make test fails while it drifts
i18n:
	go run ./pkg/i18n/cmd/i18ngen

# stream P's screen libraries alone with their tests, in the media builder's base stage (cmake, g++, catch2), until
# screen/'s own project adds them (add_subdirectory) and make screen-test runs them; one tag per worktree
SCREEN_P_IMAGE := cadence-media-base:$(notdir $(CURDIR))
SCREEN_P_DIRS := screen/i18n screen/render/viz screen/render/kenburns
screen-p-test:
	bash deploy/builder/prebuilt.sh ensure ffmpeg-mediahost-$(HOST_DEB_ARCH)
	docker build -q --target base $(FFMPEG_MEDIAHOST) -f deploy/builder/media-host.Dockerfile -t $(SCREEN_P_IMAGE) . >/dev/null
	deploy/dev/in-docker.sh $(SCREEN_P_IMAGE) sh -c 'for d in $(SCREEN_P_DIRS); do b=/tmp/build/$$d; \
		cmake -S $$d -B $$b -G Ninja -DCMAKE_BUILD_TYPE=Release >/dev/null && cmake --build $$b && \
		ctest --test-dir $$b --output-on-failure || exit 1; done'

# the visualizer's gl host on mesa's llvmpipe (its golden images, the benchmark) with libprojectM-4 built by
# media/third_party/projectm's scripts at their pins; CADENCE_UPDATE_GOLDEN=1 rewrites the golden images
SCREEN_P_GL_IMAGE := cadence-viz-gl:$(notdir $(CURDIR))
screen-p-gl-test:
	docker build -q --target projectm -f screen/render/viz/tests/gl.Dockerfile -t $(SCREEN_P_GL_IMAGE) . >/dev/null
	IN_DOCKER_FLAGS="-e CADENCE_UPDATE_GOLDEN=$${CADENCE_UPDATE_GOLDEN:-0} $${IN_DOCKER_FLAGS:-}" \
		deploy/dev/in-docker.sh $(SCREEN_P_GL_IMAGE) sh -c 'b=/tmp/build/viz-gl; \
		cmake -S screen/render/viz -B $$b -G Ninja -DCMAKE_BUILD_TYPE=Release -DCADENCE_SCREEN_GL=ON \
		-DCADENCE_SCREEN_PROJECTM=ON >/dev/null && cmake --build $$b && ctest --test-dir $$b --output-on-failure'

dev-up:
	docker compose -f deploy/dev/compose.yml up -d --build

dev-down:
	docker compose -f deploy/dev/compose.yml down -v

e2e:
	sh deploy/dev/e2e.sh

# owner-gated: HOST is a deployed v2 server's host, CODE an enroll code for a sim device; @ keeps the code off the console
live-turn:
	@sh deploy/dev/live-turn.sh "$(HOST)" "$(CODE)"

# make ci: every gate, side by side where that is safe. proto-check and sqlc-check regenerate code in the tree, so the
# gates that compile it wait for them; tools-test, device-limits-test, airplay-runtime and ha-test read none and run
# beside them. e2e runs last and alone: its timed checks (a follow-up's window, the alarm's ramp in the mix) must not
# share the machine with the c++ and race builds. each gate's output is printed whole when it ends (meanwhile in
# build/ci/<gate>.log), then a `ci_gate <gate> <seconds>s ok|failed|skipped` line per gate in this order: a gate after
# a failed one is skipped, and ci fails naming every gate that is not ok
CI_GATES := proto-check sqlc-check openapi-check dashboard-check lint test test-linux test-cgo tools-test cross \
	device-limits-test media-test media-test-privileged airplay-runtime ha-test e2e
CI_GENERATORS := proto-check sqlc-check
CI_ASIDE := tools-test device-limits-test airplay-runtime ha-test
CI_LAST := e2e
CI_DIR = build/ci
# a gate's own make streams its output into build/ci/<gate>.log: from MAKEFLAGS it would take -Orecurse and hold it
# until the gate ends (the sub-make below still prints each gate whole). ci.test.sh points CI_GATE_MAKEFILE at its fakes
# on the command line; set here, it never comes from the environment
CI_GATE_MAKEFILE =
CI_GATE_CMD = $(MAKE) --no-print-directory --no-keep-going --output-sync=none $(CI_GATE_MAKEFILE)
ci_gate = $(addprefix ci-gate-,$(1))

# + passes the jobserver on to the gate's own make; --output-sync=recurse below still holds its output whole
.PHONY: $(call ci_gate,$(CI_GATES))
$(call ci_gate,$(CI_GATES)): ci-gate-%:
	+@sh deploy/dev/ci.sh gate $(CI_DIR) $* $(CI_GATE_CMD) $*
$(call ci_gate,$(filter-out $(CI_GENERATORS) $(CI_ASIDE) $(CI_LAST),$(CI_GATES))): $(call ci_gate,$(CI_GENERATORS))
$(call ci_gate,$(CI_LAST)): $(call ci_gate,$(filter-out $(CI_LAST),$(CI_GATES)))

ci:
	@rm -rf $(CI_DIR) && mkdir -p $(CI_DIR)
	@echo "ci: $(words $(CI_GATES)) gates, logs in $(CI_DIR)"
	@$(MAKE) --no-print-directory -k -j$(words $(CI_GATES)) --output-sync=recurse $(call ci_gate,$(CI_GATES)); \
	  rc=$$?; sh deploy/dev/ci.sh summary $(CI_DIR) $(CI_GATES) || rc=1; exit $$rc
