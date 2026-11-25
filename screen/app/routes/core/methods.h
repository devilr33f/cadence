#pragma once

// pkg/rpc's method names and status tokens, as the screen calls them (server/internal/catalog/catalogscreen serves
// the catalog's)
namespace cadence::screen::routes::rpc {

inline constexpr const char* kCatalogSearch = "catalog.search";
inline constexpr const char* kCatalogBrowse = "catalog.browse";
inline constexpr const char* kCatalogItem = "catalog.item";
inline constexpr const char* kCatalogOptions = "catalog.options";
inline constexpr const char* kCatalogAlternatives = "catalog.alternatives";
inline constexpr const char* kCatalogResolve = "catalog.resolve";
inline constexpr const char* kCatalogAct = "catalog.act";
inline constexpr const char* kAssistantInvoke = "assistant.invoke";
inline constexpr const char* kMediaPlay = "media.play";
inline constexpr const char* kMediaControl = "media.control";
inline constexpr const char* kMediaQueue = "media.queue";
inline constexpr const char* kMediaItemAction = "media.item_action";
inline constexpr const char* kSettingsSet = "settings.set";

inline constexpr const char* kStatusOK = "ok";
inline constexpr const char* kStatusInvalid = "invalid";
inline constexpr const char* kStatusUnknownMethod = "unknown_method";
inline constexpr const char* kStatusNotFound = "not_found";
inline constexpr const char* kStatusRefused = "refused";
inline constexpr const char* kStatusExpired = "expired";
inline constexpr const char* kStatusUnavailable = "unavailable";

}  // namespace cadence::screen::routes::rpc
