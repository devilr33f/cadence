// the gallery's command line, shared by every gallery binary (stream b's components, the screens' pages)
#include "ui/preview/run.h"

#include "ui/preview/harness.h"

#include <cstdio>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <string>
#include <vector>

namespace cadence::screen::ui::preview {

namespace {

struct Args {
  std::string out = "ui-preview";
  std::string page;
  std::string lang = "ru";
  int width = 960;
  int height = 540;
  bool check = false;
  bool update = false;
};

bool parse(int argc, char** argv, Args& a) {
  for (int i = 1; i < argc; ++i) {
    const std::string k = argv[i];
    const auto next = [&]() -> std::string { return i + 1 < argc ? argv[++i] : ""; };
    if (k == "--out") a.out = next();
    else if (k == "--page") a.page = next();
    else if (k == "--lang") a.lang = next();
    else if (k == "--size") {
      const auto s = next();
      if (std::sscanf(s.c_str(), "%dx%d", &a.width, &a.height) != 2) return false;
    } else if (k == "--check") a.check = true;
    else if (k == "--update-golden") a.update = true;
    else return false;
  }
  return true;
}

}  // namespace

int runGallery(int argc, char** argv, const std::vector<Page>& all, const std::string& golden, const char* name) {
  Args args;
  if (!parse(argc, argv, args)) {
    std::fprintf(stderr, "usage: %s [--out DIR] [--page NAME] [--lang ru|en|xx] [--size WxH] [--check] "
                         "[--update-golden]\n", name);
    return 2;
  }
  std::filesystem::create_directories(args.out);
  int failures = 0;
  const auto fail = [&](const std::string& what) {
    std::printf("FAIL %s\n", what.c_str());
    ++failures;
  };

  std::vector<Page> selected;
  for (const auto& p : all) {
    if (args.page.empty() || p.name == args.page) selected.push_back(p);
  }
  if (selected.empty()) {
    std::fprintf(stderr, "no page %s\n", args.page.c_str());
    return 2;
  }

  std::vector<PageReport> reports;
  {
    Harness h({args.width, args.height, cadence::i18n::parse_lang(args.lang)});
    if (!h.ok()) {
      std::fprintf(stderr, "harness: %s\n", h.error().c_str());
      return 1;
    }
    for (const auto& page : selected) {
      h.load(page);
      auto image = h.capture();
      writePng(args.out + "/" + page.name + ".png", image);
      if (args.update) writePng(golden + "/" + page.name + ".png", image);
      if (args.check) {
        // the same page loaded again draws the same frame
        h.load(page);
        auto again = h.capture();
        const auto unstable = diffFraction(image, again, 0);
        if (unstable > 0) fail(page.name + ": unstable, " + std::to_string(unstable * 100) + " % of pixels differ");
        cadence::screen::render::Image want;
        if (!readPng(golden + "/" + page.name + ".png", want)) {
          fail(page.name + ": no golden image");
        } else {
          // llvmpipe's rasterisation can differ by a unit between cpus: a small tolerance, a small share
          const auto d = diffFraction(image, want, 24);
          if (d > 0.002) fail(page.name + ": " + std::to_string(d * 100) + " % of pixels differ from the golden image");
        }
        if (page.walk) {
          const auto w = h.walk();
          std::printf("walk %-28s %zu/%zu focusable nodes\n", page.name.c_str(), w.visited, w.focusable);
          if (!w.ok()) {
            std::string missed;
            for (const auto& m : w.missed) missed += " " + m;
            fail(page.name + ": the walk missed" + missed);
          }
        }
        if (h.ui().system().counts().errors > 0) fail(page.name + ": rmlui error: " + h.ui().system().lastError());
      }
      reports.push_back(h.measure(page));
    }
  }

  if (args.check) {
    // the pseudo-locale: accented and 40 % longer; no single-line slot may wrap
    Harness x({args.width, args.height, cadence::i18n::Lang::Pseudo});
    for (const auto& page : selected) {
      x.load(page);
      writePng(args.out + "/xx-" + page.name + ".png", x.capture());
      const auto l = cadence::screen::ui::analyseLayers(*x.ui().context());
      if (l.overflows > 0) {
        std::string what;
        for (const auto& o : l.overflowing) what += " " + o;
        fail(page.name + " (xx): overflowing" + what);
      }
    }
  }

  std::ofstream report(args.out + "/report.txt");
  char line[512];
  std::snprintf(line, sizeof line, "%-28s %9s %7s %12s %13s %s\n", "page", "frame ms", "layers", "animated %",
                "focus move %", "flags");
  report << line;
  std::printf("%s", line);
  for (const auto& r : reports) {
    std::string flags;
    if (r.layersFlagged()) flags += " layers>" + std::to_string(r.maxLayers);
    if (r.animatedFlagged()) flags += " animated>" + std::to_string(static_cast<int>(r.maxAnimatedPct)) + "%";
    std::snprintf(line, sizeof line, "%-28s %9.2f %7d %12.1f %13.1f%s\n", r.page.c_str(), r.frameMs, r.layers,
                  r.animatedRestPct, r.animatedFocusPct, flags.c_str());
    report << line;
    std::printf("%s", line);
    // a page over its layer budget, or animating more than its share at rest, fails; the focus move's area is
    // reported (a row's anchored scroll and the page's scroll to keep focus low move whole rows by design)
    if (args.check && (r.layersFlagged() || r.animatedRestPct > r.maxAnimatedPct)) fail(r.page + ": over budget" + flags);
  }
  std::printf("%d failure(s)\n", failures);
  return failures == 0 ? 0 : 1;
}

}  // namespace cadence::screen::ui::preview
