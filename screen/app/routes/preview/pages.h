#pragma once

#include "app/routes/core/router.h"
#include "app/routes/preview/fake-catalog.h"
#include "ui/preview/gallery.h"

#include <functional>
#include <memory>
#include <vector>

// i1's gallery: each route on the fake catalog, as the device draws it, for the golden images, the budget report,
// the pseudo-locale and the d-pad walk (cadence-browse-preview, ctest browse-gallery)
namespace cadence::screen::routes::preview {

// the routes on one ui: the fake server, its painted artwork in an inline cache, the router (no root opened), and a
// ticker that runs the router on the ui's clock
struct Session {
  Session(ui::Ui& ui, fake::Script script);
  ~Session();
  Session(const Session&) = delete;
  Session& operator=(const Session&) = delete;

  ui::Ui& ui;
  std::shared_ptr<fake::FakeImages> fetcher;
  std::unique_ptr<ui::images::ImageCache> images;
  std::unique_ptr<fake::FakeCatalog> server;
  std::unique_ptr<Router> router;
  std::shared_ptr<bool> alive;
};

// a fresh session on ui (the previous one, on this ui or another, goes first)
Session& start(ui::Ui& ui, fake::Script script = {});

std::vector<ui::preview::Page> pages();

}  // namespace cadence::screen::routes::preview
