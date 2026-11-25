# the ui's faces (design r2 section 4): rmlui's freetype path applies no opentype features, so static faces are
# instanced from inter's variable font, tnum is frozen into "inter figures", and every face is subset to latin,
# latin-1, the pseudo-locale's accents and cyrillic; the icons are lucide's font cut to tokens.json's set, every
# glyph centred in its advance and in the line box (build-fonts.py's centre_icons and check_icons).
# make screen-ui-fonts runs this and copies /out into screen/ui/fonts (committed with their licenses)
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS fonts
RUN grep -q 'snapshot.debian.org/archive/debian/20261104T212339Z' /etc/apt/sources.list.d/debian.sources \
  && printf '%s\n' 'Types: deb' 'URIs: http://snapshot.debian.org/archive/debian/20261104T212339Z' \
    'Suites: trixie trixie-updates' 'Components: main' 'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' \
    >/etc/apt/sources.list.d/debian.sources \
  && printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot \
  && apt-get update \
  && apt-get install --yes --no-install-recommends ca-certificates curl python3-fonttools unzip \
  && rm -rf /var/lib/apt/lists/*
COPY screen/ui/fonts/pins.conf /src/pins.conf
RUN . /src/pins.conf && mkdir -p /dl && cd /dl \
  && curl --fail --silent --show-error --location -o inter.zip "$INTER_URL" \
  && curl --fail --silent --show-error --location -o jbmono.zip "$JBMONO_URL" \
  && curl --fail --silent --show-error --location -o lucide.tgz "$LUCIDE_URL" \
  && printf '%s  %s\n' "$INTER_SHA256" inter.zip "$JBMONO_SHA256" jbmono.zip "$LUCIDE_SHA256" lucide.tgz \
    | sha256sum --check --strict - \
  && unzip -q -o inter.zip InterVariable.ttf LICENSE.txt -d inter \
  && unzip -q -o jbmono.zip 'fonts/ttf/JetBrainsMono-Regular.ttf' OFL.txt -d jbmono \
  && tar -xzf lucide.tgz package/LICENSE package/font/lucide.css package/font/lucide.ttf
COPY screen/ui/fonts/build-fonts.py /src/build-fonts.py
COPY screen/ui/tokens/tokens.json /src/tokens.json
RUN python3 -I /src/build-fonts.py /dl /src/tokens.json /out \
  && cp /dl/inter/LICENSE.txt /out/Inter-OFL.txt \
  && cp /dl/jbmono/OFL.txt /out/JetBrainsMono-OFL.txt \
  && cp /dl/package/LICENSE /out/LucideIcons-LICENSE.txt
