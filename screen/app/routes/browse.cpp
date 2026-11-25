// i1's routes in the registry: each opens from cadenced's Route message or from another route
#include "app/routes/core/router.h"
#include "app/routes/episodes/episodes.h"
#include "app/routes/home/home.h"
#include "app/routes/picker/picker.h"
#include "app/routes/resolving/resolving.h"
#include "app/routes/search/search.h"
#include "app/routes/title/title.h"

namespace cadence::screen::routes {

namespace {

template <class R>
Router::Factory factory() {
  return [](Services& s, const RouteArgs& a) -> std::unique_ptr<Route> { return std::make_unique<R>(s, a); };
}

}  // namespace

void registerBrowseRoutes(Router& router) {
  router.registerRoute(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME, factory<HomeRoute>());
  router.registerRoute(cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH, factory<SearchRoute>());
  router.registerRoute(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, factory<TitleRoute>());
  router.registerRoute(cadence_v2_ScreenRoute_SCREEN_ROUTE_EPISODES, factory<EpisodesRoute>());
  router.registerRoute(cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER, factory<PickerRoute>());
  router.registerRoute(cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING, factory<ResolvingRoute>());
}

}  // namespace cadence::screen::routes
