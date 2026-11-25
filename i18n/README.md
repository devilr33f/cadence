# Catalogs

One message catalog per area and language: `i18n/<area>/<lang>.json`, areas `screen` (the TV app) and `server` (card
text, step labels, toasts and notices, rendered in the target screen's language). `en` and `ru` are both required
for every key; `xx` (the pseudo-locale: accented, about 40 % longer) is made from `en` at load time and has no file.

Each file is one flat JSON object of dotted keys (`player.osd.voiceover`) to messages, sorted by key.

## Message format

An ICU MessageFormat subset, the same in Go (`pkg/i18n`) and C++ (`screen/i18n`):

| Form | Example |
| --- | --- |
| text | `Settings`; `''` is an apostrophe, `'{'` a literal brace |
| `{name}` | `Up next: {title}` (text) |
| `{name, number}` | `{size, number} GB` (grouped, the language's decimal sign) |
| `{name, plural, …}` | `{count, plural, one {# task} other {# tasks}}`; `#` is the number |
| `{name, select, …}` | `{kind, select, tv {TV} other {Speaker}}` |

`other` is required in every plural and select. A plural must list exactly its language's CLDR categories: `en`
`one other`, `ru` `one few many other`; explicit cases (`=0 {…}`) may be added. Every language of a key takes the
same arguments of the same types. Numbers, clocks, countdowns and durations that code formats go through the locale
helpers (`i18n.Count`, `Clock`, `Countdown`, `Elapsed`, `Duration`; `cadence::i18n::count`, …), not through messages.

## Adding or changing a key

1. Add it to `en.json` and `ru.json` of its area.
2. Run `make i18n`. It refuses an incomplete catalog and regenerates:
   - `pkg/i18n/servertext`: a typed constructor per server key (`servertext.StepWebSearch(query)`) and the embedded
     catalog;
   - `screen/i18n/gen/keys.h` and `catalog.cpp`: a type per screen key (`keys::TasksCount{3}`), so a key that does
     not exist, a missing argument or a wrong type fails the C++ build.
3. `make test` fails while the generated files drift from the catalogs, or a key is missing from a language.

In RML, text and attributes take `[[key]]` or `[[key|name=value|name=value]]`; the screen's translator rewrites them
(RmlUi's `SystemInterface::TranslateString`).
