# screen/ui

Stream B's UI library for cadence-screen: RmlUi 6.3 drawn through stream A's `RenderBackend`, the Afterglow design
system (design r2 §1–10), Cadence Cards on the device (§17), and the preview gallery. The dashboard is not a design
reference.

| Dir | What |
|---|---|
| `rml/` | RmlUi's interfaces: system (the UI's clock, `TranslateString` through `i18n::Translator`, a counted log that writes nothing), files (asset roots only), the renderer and the sprites the device draws itself |
| `rml/third_party/` | RmlUi's GL3 renderer (MIT), vendored from the 6.3 tarball and patched (see the commit after the vendoring one): `RMLUI_GLES3`, no MSAA layer, `SetExternalTarget` |
| `tokens/` | `tokens.json` and `tokens-gen` (a build tool): templates' `$group.name` → values, utility classes, `ui/tokens.h` |
| `assets/rcss/` | the RCSS templates (base, components, cards); a literal colour fails the build |
| `fonts/` | the baked faces, committed with their licences (OFL-1.1, Lucide ISC); `make screen-ui-fonts` rebuilds them |
| `core/` | `Ui` (context, documents, keys, overlays, motion), `Focus`, the component builders (`rml.h`), `ShellLayer` (the app's `UiLayer`), the budget analysis |
| `cards/` | decoding and the device's limits, the renderer for all 27 primitives, `CardHost` (actions, revisions, refusals), `timeline.h` (a slider's playback position, run from its anchor) |
| `preview/` | the gallery (`cadence-ui-preview`), its harness and the golden images |
| `tests/` | Catch2: the cards model, the timeline slider's clock, and the focus, long-press, dialog, slider and language behaviour |

## Hooks for the layers over the page

Wave 2's layers and routes (`screen/app/routes`) sit on `Ui` without a router of its own here: key filters (newest
first, before the page), tickers, activation filters, focus listeners, and documents of their own between the page and
the overlay (ordered by z, built again with the language; a focus scope on one survives a page change). A toast with
a button takes focus only when ↓ finds nothing below (`enterToast`); BACK or ↑ gives it back.

## Rendering

The UI has its own render target (RGBA8 + stencil from `RenderBackend::createRenderTarget`). `Ui::render` redraws it
only when something changed (a key, a message, a timer, a running transition or animation), then composites it
premultiplied over the frame every time; the overscan setting composites it into the safe area (one transform, no
relayout). Nothing is shown until the first page has laid out (`ready()`).

## Design §25 workarounds, as built

| Need | Here |
|---|---|
| tokens without `var()` | `tokens-gen` expands templates at build time |
| animated glow | a static, pre-blurred nine-patch (`gen:glow`) whose opacity transitions. RmlUi's `box-shadow` drew nothing in this setup (GL3 renderer, own or external base layer, llvmpipe), and the nine-patch never renders a blur layer, which is cheaper on the Mali anyway |
| tabular figures | «Inter Figures», `tnum` frozen into the cmap |
| icons | Lucide's font cut to `tokens.json`'s set (100 names, at U+E000), each glyph centred in its advance and the line box, tinted by `color` — a glyph font instead of a PNG atlas |
| spinner | one generated arc the `Ui` turns (`transform: rotate`) a step a frame while it is visible, so the loop wakes for it alone |
| hold arc | 8 sprite frames, generated on the device and swapped by class |
| shared skeleton pulse | one clock in `Ui` sets every `.skel`'s opacity, 12 steps a period |
| column memory, anchored scroll, nudge | `Focus` moves focus itself (`.f { nav: none }`) |
| dashed empty-state outline | 1 dp solid `line` at 50 % |
| QR | Nayuki's qrcodegen (MIT), ink on paper with its quiet zone |

## Gallery

`cadence-ui-preview --check` (ctest `ui-gallery`, in `make screen-test`) renders every page at 960 × 540 in `ru`,
compares it with `preview/golden/<page>.png` (tolerance 24 per channel, 0.2 % of pixels), renders it again for a
stable frame, checks every focusable node is reachable by the D-pad, loads every page in the pseudo-locale `xx`
for text that wraps a single-line slot, and writes `report.txt` (frame time, full-screen translucent layers, the
animated area at rest and while focus moves). `--update-golden` rewrites the golden images; `--out DIR` keeps the
frames (`xx-*.png` too).

A page fails over 2 translucent layers or over 30 % animated at rest. The focus move's area is reported, not
failed: a poster row's anchored scroll moves the whole row (~37 % of the screen) and the page's scroll to keep focus
low moves everything, both by design (§7), so they stand as the design's risks for the stick.
