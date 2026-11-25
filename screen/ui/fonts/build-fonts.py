# builds the ui's faces from the pinned sources (fonts.Dockerfile): python3 -I build-fonts.py <src> <tokens.json> <out>
import json
import os
import sys

from fontTools import subset
from fontTools.pens.boundsPen import BoundsPen
from fontTools.ttLib import TTFont
from fontTools.ttLib.tables._g_l_y_f import GlyphCoordinates
from fontTools.varLib import instancer

src, tokens_path, out = sys.argv[1], sys.argv[2], sys.argv[3]
os.makedirs(out, exist_ok=True)

# latin, latin-1, latin extended a/b, ipa and latin extended additional (the pseudo-locale's accents), cyrillic,
# general punctuation, currency (rouble, euro), letterlike (numero), arrows, minus and multiplication, triangles
TEXT_RANGES = [
    (0x20, 0x7E), (0xA0, 0xFF), (0x100, 0x24F), (0x250, 0x2AF), (0x400, 0x45F), (0x490, 0x491), (0x1E00, 0x1EFF),
    (0x2000, 0x206F), (0x20AC, 0x20AC), (0x20BD, 0x20BD), (0x2116, 0x2116), (0x2190, 0x21FF), (0x2212, 0x2212),
    (0x2715, 0x2715), (0x25B2, 0x25BC),
]
UNICODES = [c for lo, hi in TEXT_RANGES for c in range(lo, hi + 1)]


def set_names(font, family, style):
    name = font["name"]
    for rec in list(name.names):
        if rec.nameID in (1, 2, 3, 4, 6, 16, 17, 21, 22, 25):
            name.removeNames(nameID=rec.nameID)
    full = f"{family} {style}"
    for nid, value in ((1, family), (2, style), (3, f"cadence;{full}"), (4, full),
                       (6, full.replace(" ", ""))):
        name.setName(value, nid, 3, 1, 0x409)


def cut(font, unicodes, path):
    opts = subset.Options()
    opts.layout_features = []
    opts.name_IDs = ["*"]
    opts.name_languages = ["*"]
    opts.notdef_outline = True
    opts.hinting = False
    opts.glyph_names = False
    opts.drop_tables += ["STAT", "DSIG", "GDEF", "GPOS", "GSUB", "meta"]
    s = subset.Subsetter(opts)
    s.populate(unicodes=unicodes)
    s.subset(font)
    font.save(path)


def freeze(font, feature):
    # single substitutions of the feature's lookups applied to the cmap: rmlui's freetype path draws the default
    # glyphs only, so the tabular figures become the defaults
    gsub = font["GSUB"].table
    lookups = set()
    for rec in gsub.FeatureList.FeatureRecord:
        if rec.FeatureTag == feature:
            lookups.update(rec.Feature.LookupListIndex)
    mapping = {}
    for i in sorted(lookups):
        lookup = gsub.LookupList.Lookup[i]
        for st in lookup.SubTable:
            if lookup.LookupType == 7:
                st = st.ExtSubTable
            if getattr(st, "mapping", None):
                mapping.update(st.mapping)
    if not mapping:
        sys.exit(f"no {feature} substitutions")
    for table in font["cmap"].tables:
        for code, glyph in list(table.cmap.items()):
            if glyph in mapping:
                table.cmap[code] = mapping[glyph]
    return len(mapping)


def glyph_bounds(font, name):
    pen = BoundsPen(font.getGlyphSet())
    font.getGlyphSet()[name].draw(pen)
    return pen.bounds


def centre_icons(font, count):
    # shifts every icon's outline so its ink is centred in the glyph's advance and in the line box the rcss uses
    # (`.ibtn .icon` centres by line-height: rmlui leads the baseline half above the font's ascent..descent box,
    # so the box's middle sits at (ascent+descent)/2 whatever the font size). source sets draw on a canvas that is
    # centred only on average, so each glyph moves by its own bbox; two names may map to one glyph, and a composite
    # moves by its components' offsets.
    # a renderer (freetype, and fontTools' glyph set) places an outline at its hmtx lsb, translating by lsb - xMin,
    # and lucide.ttf ships headers and lsbs that disagree; each glyph's lsb is set to its shifted xMin so the drawn
    # outline is the stored one
    glyf = font["glyf"]
    middle = (font["hhea"].ascent + font["hhea"].descent) / 2
    seen = set()
    for cp in range(0xE000, 0xE000 + count):
        name = font.getBestCmap()[cp]
        if name in seen:  # two names mapped to the same glyph: moved already
            continue
        seen.add(name)
        bounds = glyph_bounds(font, name)
        if bounds is None:
            sys.exit(f"icon at U+{cp:04X} has no outline")
        x0, y0, x1, y1 = bounds
        advance, lsb = font["hmtx"].metrics[name]
        x0, x1 = x0 - (lsb - glyf[name].xMin), x1 - (lsb - glyf[name].xMin)  # the stored outline
        dx = round((advance - (x1 - x0)) / 2 - x0)
        dy = round(middle - (y0 + y1) / 2)
        glyph = glyf[name]
        if glyph.isComposite():
            for component in glyph.components:
                if not component.flags & 0x0002:  # ARGS_ARE_XY_VALUES
                    sys.exit(f"icon at U+{cp:04X}: a component matches points, cannot shift")
                component.x, component.y = component.x + dx, component.y + dy
            coordinates, _, _ = glyph.getCoordinates(glyf)
        else:
            glyph.coordinates = GlyphCoordinates([(x + dx, y + dy) for x, y in glyph.coordinates])
            coordinates = glyph.coordinates
        # a scaled component gives fractional bounds: the tables want ints, and compile recalculates from the
        # coordinates anyway
        glyph.xMin, glyph.yMin, glyph.xMax, glyph.yMax = (round(v) for v in (x0 + dx, y0 + dy, x1 + dx, y1 + dy))
        font["hmtx"].metrics[name] = (advance, round(min(x for x, _ in coordinates)))


def check_icons(path, count):
    # the gate: reopen what was saved and fail unless every icon's ink is centred within 1% of the em, of its
    # advance and of the line box
    font = TTFont(path)
    tolerance = font["head"].unitsPerEm / 100
    middle = (font["hhea"].ascent + font["hhea"].descent) / 2
    worst = 0.0
    for cp in range(0xE000, 0xE000 + count):
        name = font.getBestCmap()[cp]
        x0, y0, x1, y1 = glyph_bounds(font, name)
        off_advance = abs((x0 + x1) / 2 - font["hmtx"].metrics[name][0] / 2)
        off_box = abs((y0 + y1) / 2 - middle)
        worst = max(worst, off_advance, off_box)
        if off_advance > tolerance or off_box > tolerance:
            sys.exit(f"icon at U+{cp:04X} not centred: {off_advance:.1f} off its advance, {off_box:.1f} off the line box")
    print(f"CadenceIcons.ttf: {count} icons centred within {worst:.2f} units (tolerance {tolerance:.0f})")


inter = os.path.join(src, "inter", "InterVariable.ttf")
faces = [
    ("Inter-Regular.ttf", "Inter", "Regular", {"wght": 400, "opsz": 14}, False),
    ("Inter-Medium.ttf", "Inter", "Medium", {"wght": 500, "opsz": 14}, False),
    ("Inter-SemiBold.ttf", "Inter", "SemiBold", {"wght": 600, "opsz": 14}, False),
    ("InterDisplay-Bold.ttf", "Inter Display", "Bold", {"wght": 700, "opsz": 32}, False),
    ("InterFigures-Medium.ttf", "Inter Figures", "Medium", {"wght": 500, "opsz": 14}, True),
]
for file, family, style, axes, tnum in faces:
    font = instancer.instantiateVariableFont(TTFont(inter), axes)
    if tnum:
        n = freeze(font, "tnum")
        print(f"{file}: tnum froze {n} glyphs")
    set_names(font, family, style)
    cut(font, UNICODES, os.path.join(out, file))

mono = TTFont(os.path.join(src, "jbmono", "fonts", "ttf", "JetBrainsMono-Regular.ttf"))
set_names(mono, "JetBrains Mono", "Regular")
cut(mono, UNICODES, os.path.join(out, "JetBrainsMono-Regular.ttf"))

# icons: tokens.json's names in order at U+E000 onwards, from lucide's css codepoints
tokens = json.load(open(tokens_path, encoding="utf-8"))
icons = [(k, v) for k, v in tokens["icons"].items() if not k.startswith("_")]
css = open(os.path.join(src, "package", "font", "lucide.css"), encoding="utf-8").read()
codes = {}
for block in css.split(".icon-")[1:]:
    head, _, rest = block.partition("::before")
    if "content:" in rest:
        value = rest.split('"')[1]
        codes[head] = int(value.strip("\\"), 16)
lucide = TTFont(os.path.join(src, "package", "font", "lucide.ttf"))
best = lucide.getBestCmap()
remap = {}
for i, (ours, theirs) in enumerate(icons):
    if theirs not in codes or codes[theirs] not in best:
        sys.exit(f"icon {ours}: lucide has no {theirs}")
    remap[0xE000 + i] = best[codes[theirs]]
for table in lucide["cmap"].tables:
    if table.isUnicode():
        table.cmap = dict(remap)
set_names(lucide, "Cadence Icons", "Regular")
icons_path = os.path.join(out, "CadenceIcons.ttf")
cut(lucide, sorted(remap), icons_path)

# the tabler cut's vertical metrics, kept so a line that mixes an icon with text does not move
font = TTFont(icons_path)
font["hhea"].ascent, font["hhea"].descent, font["hhea"].lineGap = 900, -100, 0
os2 = font["OS/2"]
os2.sTypoAscender, os2.sTypoDescender, os2.sTypoLineGap = 900, -100, 90
os2.usWinAscent, os2.usWinDescent = 990, 104
centre_icons(font, len(icons))
font.save(icons_path)
check_icons(icons_path, len(icons))

for f in sorted(os.listdir(out)):
    print(f, os.path.getsize(os.path.join(out, f)))
