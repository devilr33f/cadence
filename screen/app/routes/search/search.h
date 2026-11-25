#pragma once

#include "app/routes/core/catalog.h"
#include "app/routes/core/failure.h"
#include "app/routes/core/router.h"

#include <cstdint>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// search (design r2 section 18.1): the on-screen keyboard on the left with the query, results on the right in rows
// by kind (anime and series, films, youtube, music: each keeps one aspect and fills on its own), a status strip
// with a chip per provider and source while they answer. results stream in (catalog.search, subscribed): new ones
// append at a row's end, a title group another source also returns changes only its meta line, and once focus is
// in the results a row of a kind not yet shown joins below the others, so nothing under focus ever moves. focus
// moving right into the results collapses the keyboard to a query bar; ↑ onto that bar, or BACK, brings it back
class SearchRoute final : public Route {
 public:
  SearchRoute(Services& s, const RouteArgs& args);

  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH; }
  std::string body() override;
  void loaded(Rml::ElementDocument& doc, bool restoring) override;
  void start() override;
  bool activate(const ui::Activation& a) override;
  bool back() override;
  void update(std::int64_t nowMs) override;
  void context(ScreenContextData& c) const override;

  // the row a result goes to
  static std::string rowKindOf(const catalog::Item& item);
  // the query as typed so far
  [[nodiscard]] const std::string& query() const { return query_; }
  // tests: one search event, as the server streams them
  void event(const Json& ev);

 private:
  enum class SourceState { Pending, Answered, Failed };
  struct Source {
    std::string provider;
    std::string source;
    std::string name;
    SourceState state = SourceState::Pending;
    int count = 0;
    // how it failed, as the server said it (failure.h)
    failure::Call failure;
  };
  struct Row {
    std::string kind;
    std::vector<catalog::Item> items;
    std::vector<std::string> unenforced;
  };

  void type(const std::string& key);
  void search();
  void clearResults();
  Row& row(const std::string& kind);
  std::string rowRml(const Row& r) const;
  std::string cardRml(const catalog::Item& it) const;
  std::string stripRml() const;
  std::string resultsRml() const;
  std::string emptyRml() const;
  void placeRow(const Row& r);
  void addItem(const catalog::Item& it);
  void updateItem(const catalog::Item& it);
  void redrawStrip();
  void redrawEntry();
  void redrawKeyboard();
  void setCollapsed(bool on);
  const catalog::Item* itemOf(const std::string& ref) const;
  std::string sourceName(const catalog::Item& it) const;
  bool focusInResults() const;
  bool hasResults() const;

  std::string query_;
  ui::rml::KeyLayout layout_ = ui::rml::KeyLayout::Cyrillic;
  std::vector<Source> sources_;
  std::vector<Row> rows_;
  // the dom's order of rows (kinds), which never changes once a row is in
  std::vector<std::string> order_;
  bool searching_ = false;
  bool done_ = false;
  bool failed_ = false;
  // the whole search's failure as it came, and a retry under way (the failure stays, «Повторить» spinning)
  failure::Call failure_;
  bool retrying_ = false;
  std::int64_t searchedMs_ = 0;
  bool collapsed_ = false;
  std::int64_t dueMs_ = -1;
  int keyIndex_ = 0;
  std::string callId_;
};

}  // namespace cadence::screen::routes
