// i3's routes and layer in the registry: enrollment and settings open from cadenced's Route message (and the layer:
// enrollment while there is no token, settings from the top bar); the drawer, tasks and airplay are a layer
#include "app/routes/core/router.h"
#include "app/routes/system/alarms-page.h"
#include "app/routes/system/enroll.h"
#include "app/routes/system/settings.h"
#include "app/routes/system/system.h"

namespace cadence::screen::routes {

void registerSystemRoutes(Router& router) {
  auto state = std::make_shared<SystemState>();
  state->appVersion = CADENCE_SCREEN_ROUTES_VERSION;
  router.registerRoute(cadence_v2_ScreenRoute_SCREEN_ROUTE_ENROLL,
                       [state](Services& s, const RouteArgs&) -> std::unique_ptr<Route> { return std::make_unique<Enroll>(s, state); });
  router.registerRoute(cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS, [state](Services& s, const RouteArgs& a) -> std::unique_ptr<Route> {
    return std::make_unique<Settings>(s, state, a);
  });
  router.registerRoute(cadence_v2_ScreenRoute_SCREEN_ROUTE_ALARMS, [](Services& s, const RouteArgs& a) -> std::unique_ptr<Route> {
    return std::make_unique<AlarmsPage>(s, a);
  });
  router.addLayer([state](Services& s, app::Outbox& out) -> std::unique_ptr<Layer> {
    return std::make_unique<SystemLayer>(s, out, state);
  });
}

}  // namespace cadence::screen::routes
