#include "app/routes/preview/pages.h"

#include "app/routes/core/view.h"
#include "app/routes/system/env.h"
#include "ui/core/focus.h"

#include <RmlUi/Core/ElementDocument.h>

namespace cadence::screen::routes::preview {

using ui::preview::Context;
using ui::preview::Page;

namespace {

std::unique_ptr<Session>& current() {
  static std::unique_ptr<Session> s;
  return s;
}

void tick(Session* s, std::shared_ptr<bool> alive) {
  s->ui.after(16, [s, alive] {
    if (!*alive) return;
    s->router->update(s->ui.nowMs());
    tick(s, alive);
  });
}

void focusId(Session& s, const std::string& id) {
  if (auto* e = view::byId(s.ui.page(), id)) s.ui.focus().focus(e);
}

std::string blank() { return "<div/>"; }

Page page(const std::string& name, fake::Script script, std::function<void(Session&)> drive, bool walk = true, int maxLayers = 2) {
  Page p;
  p.name = "browse-" + name;
  p.body = blank;
  p.setup = [script, drive](Context& c) {
    auto& s = start(c.ui, script);
    drive(s);
  };
  p.walk = walk;
  p.maxLayers = maxLayers;
  return p;
}

// a chain of routes as the viewer reached them, each after the one below has loaded
void openTitle(Session& s, int afterMs, std::function<void()> then) {
  s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  s.ui.after(afterMs, [&s, then] {
    RouteArgs a;
    a.ref = "ref-tg-1";
    s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, a);
    if (then) s.ui.after(200, then);
  });
}

void typeQuery(Session& s, const std::string& q) {
  RouteArgs a;
  a.query = q;
  s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH, a);
}

}  // namespace

Session::Session(ui::Ui& u, fake::Script script) : ui(u), alive(std::make_shared<bool>(true)) {
  fetcher = std::make_shared<fake::FakeImages>();
  ui::images::CacheOptions o;
  o.inlineDecode = true;
  images = std::make_unique<ui::images::ImageCache>(fetcher, o);
  server = std::make_unique<fake::FakeCatalog>(u, std::move(script));
  Router::Options ro;
  ro.images = images.get();
  ro.openRoot = false;
  router = std::make_unique<Router>(ro);
  registerBrowseRoutes(*router);
  const bool nearby = server->script.nearby;
  if (nearby) registerSystemRoutes(*router);
  server->setTarget(router.get());
  router->attach(u, *server);
  if (nearby) {
    // the link is up: the system layer subscribes devices.media, which the fake answers with its devices
    cadence_screen_v1_Envelope l = cadence_screen_v1_Envelope_init_zero;
    l.which_body = cadence_screen_v1_Envelope_link_status_tag;
    l.body.link_status.state = cadence_screen_v1_LinkState_LINK_STATE_ONLINE;
    router->message(l);
  }
  ui.onActivate([this](const ui::Activation& a) { router->activate(a); });
  ui.onBack([this] { return router->back(); });
  ui.onPageLoaded([this](Rml::ElementDocument& doc) { router->loaded(doc); });
  tick(this, alive);
}

Session::~Session() {
  *alive = false;
  // a ui that is gone took its image binder with it: nothing of it may be touched
  if (!images->bound()) {
    // the system layer's parts (home's strip page) touch nothing of it either
    uiAbandoned() = true;
    router->abandon();
    router.reset();
    uiAbandoned() = false;
  }
  router.reset();
  server.reset();
  images.reset();
}

Session& start(ui::Ui& ui, fake::Script script) {
  current().reset();
  current() = std::make_unique<Session>(ui, std::move(script));
  return *current();
}

std::vector<Page> pages() {
  std::vector<Page> out;
  using R = fake::Script;

  // home with the hero expanded, focus on «Продолжить»; a provider down as its strip, one not configured left out
  out.push_back(page("home", R{}, [](Session& s) { s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME); }));

  // focus in the anime row: the hero collapsed into a preview of the focused title, rows above faded
  out.push_back(page("home-rows", R{}, [](Session& s) {
    s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
    s.ui.after(300, [&s] {
      // torrplayer's anime row, its longest (each card's shape is its item's now, so the row is found by its
      // provider; its continue row is the short one)
      auto* tp = view::byId(s.ui.page(), "prov-torrplayer");
      Rml::ElementList rows;
      if (tp != nullptr) tp->QuerySelectorAll(rows, ".row");
      Rml::ElementList best;
      for (auto* r : rows) {
        if (r->GetId().find("hrow") != 0) continue;
        Rml::ElementList cards;
        r->QuerySelectorAll(cards, ".card.has-ref");
        if (cards.size() > best.size()) best = cards;
      }
      if (best.size() > 2) s.ui.focus().focus(best[2]);
    });
  }));

  // what plays nearby: this room's two devices on a strip under the hero (one also playing here), the top bar's
  // indicator counting all three, focus on the second item
  R nearby;
  nearby.nearby = true;
  out.push_back(page("home-nearby", nearby, [](Session& s) {
    s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
    s.ui.after(300, [&s] { focusId(s, "nearby-1"); });
  }));

  // every provider still loading: headers and skeletons at once, the shared pulse
  R loading;
  loading.hang = true;
  out.push_back(page("home-loading", loading, [](Session& s) { s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME); }));

  // search while sources still answer: the keyboard beside the strip's chips, rows filling
  R typing;
  typing.searchHangs = true;
  out.push_back(page("search-typing", typing, [](Session& s) { typeQuery(s, "лун"); }, false));

  // every source answered: focus in the results, the keyboard collapsed to the query bar, one summary line, the
  // filter note by its row, a merged title group («на 5 источниках»), a row nobody can play
  out.push_back(page("search-results", R{}, [](Session& s) {
    typeQuery(s, "лун");
    s.ui.after(1200, [&s] {
      if (auto* row = view::byId(s.ui.page(), "srow-anime")) {
        if (auto* first = row->QuerySelector(".card.f")) s.ui.focus().focus(first);
      }
    });
  }, false));

  // nothing found, with the latin-search chip for a cyrillic query
  R empty;
  empty.searchEmpty = true;
  out.push_back(page("search-empty", empty, [](Session& s) { typeQuery(s, "ъъъ"); }, false));

  // the title page, focus on «Продолжить: серия 13»
  out.push_back(page("title", R{}, [](Session& s) { openTitle(s, 300, nullptr); }));

  // the source switcher over the title page
  out.push_back(page("title-switcher", R{}, [](Session& s) {
    openTitle(s, 300, [&s] {
      focusId(s, "t-source");
      if (auto* e = view::byId(s.ui.page(), "t-source")) s.router->activate(ui::Activation{e, "switcher", false, {}, {}});
    });
  }, true, 2));

  // episodes: numbered tiles, watched, the recap 5.5, an ova, a new one, range chips over 50
  R many;
  many.episodes = 120;
  out.push_back(page("episodes", many, [](Session& s) {
    openTitle(s, 300, [&s] {
      RouteArgs a;
      a.ref = "ref-v-aniliberty";
      a.episode = "13";
      a.data = Json{{"title", "Лунный аптекарь"}, {"provider", "torrplayer"}, {"kind", "title_group"}};
      s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_EPISODES, a);
    });
  }));

  // the voiceover and quality picker: translators with their tags, quality chips, a voiceover without the episode,
  // torrents with seeders, the hidden-by-filters row; the remembered choice marked
  R remembered;
  remembered.remembered = true;
  out.push_back(page("picker", remembered, [](Session& s) {
    openTitle(s, 300, [&s] {
      RouteArgs a;
      a.ref = "ref-v-aniliberty";
      a.episode = "13";
      a.data = Json{{"title", "Лунный аптекарь"}, {"art", fake::imageId(100)}, {"provider", "torrplayer"}, {"kind", "title_group"}};
      s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER, a);
    });
  }));

  // resolving a torrent: the steps ticking, the indeterminate line
  R hang;
  hang.resolve = "hang";
  out.push_back(page("resolving", hang, [](Session& s) {
    openTitle(s, 300, [&s] {
      RouteArgs a;
      a.ref = "ref-v-aniliberty";
      a.episode = "13";
      a.data = Json{{"title", "Лунный аптекарь"}, {"art", fake::imageId(100)}, {"provider", "torrplayer"}, {"kind", "title_group"},
                    {"choices", {{"torrent", "tor-1"}}}, {"label", "1–12 · 1080p"}};
      s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING, a);
    });
  }, false));

  // resolving failed: no seeders, «Другая озвучка или источник» focused
  R noSeeds;
  noSeeds.resolve = "no_seeders";
  out.push_back(page("resolving-failed", noSeeds, [](Session& s) {
    s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
    RouteArgs a;
    a.ref = "ref-v-aniliberty";
    a.episode = "13";
    a.data = Json{{"title", "Лунный аптекарь"}, {"art", fake::imageId(100)}, {"provider", "torrplayer"}, {"kind", "title_group"},
                  {"choices", {{"torrent", "tor-2"}}}, {"label", "1–24 · 1080p"}};
    s.router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING, a);
  }));

  return out;
}

}  // namespace cadence::screen::routes::preview
