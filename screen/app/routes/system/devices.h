#pragma once

#include "app/routes/system/device.h"
#include "app/routes/system/env.h"
#include "app/routes/system/nearby.h"
#include "app/routes/system/page-rpc.h"
#include "ui/cards/host.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// what the household's other devices play, on this screen: one devices.media subscription while the link is up
// (home's strip of its own room, the top bar's indicator of all rooms), and the quick panel, the right sheet of the
// server's cadence cards (devices.panel, subscribed while it is open: a card per device with its cover, transport,
// volume and «play here too»), opened from a strip item on that device's card, from the indicator, or by cadenced's
// Route DEVICES (its ref the device to focus). a new page updates in place without moving focus; BACK closes it.
// the cards' buttons and sliders act through card.action (rule 7: a volume moves only by such a press)
class Devices {
 public:
  Devices(Env env, PageRpc& rpc, const DeviceState& device);
  ~Devices();
  Devices(const Devices&) = delete;
  Devices& operator=(const Devices&) = delete;

  // link_status starts and stops the feed; route DEVICES opens the panel, back and other routes close it
  bool message(const cadence_screen_v1_Envelope& m);

  // the feed: every item (all rooms), and a number one higher on each change
  [[nodiscard]] const std::vector<nearby::Item>& items() const { return items_; }
  [[nodiscard]] std::uint64_t revision() const { return revision_; }
  // the top bar's indicator, empty while nothing plays elsewhere
  [[nodiscard]] std::string indicator() const;
  std::function<void()> onItemsChanged;

  // the panel, focused on a device's card ("" focuses the first)
  void open(const std::string& focusDeviceId = {});
  void close();
  [[nodiscard]] bool isOpen() const { return open_; }
  [[nodiscard]] const std::string& focusDevice() const { return focus_; }
  [[nodiscard]] cards::CardHost& cards() { return *cards_; }
  // before the panel takes focus (the other sheet closes first); and once it opened or closed
  std::function<void()> onOpening;
  std::function<void(bool open)> onOpenChanged;
  // the drawer over the panel: its duck stands in for the panel's scrim
  void setUnderDrawer(bool under);

  // an answer or event of the feed and a page of the panel, as the link would hand them (tests and the gallery)
  void showItems(std::string_view json);
  void showPage(const cadence_cards_v1_Page& page);

 private:
  std::string build();
  void loaded(Rml::ElementDocument& doc);
  bool key(const platform::KeyEvent& e);
  bool activate(const ui::Activation& a);
  void tick(std::int64_t nowMs);

  void subscribeFeed();
  void subscribePanel();
  void draw(const cadence_cards_v1_Page& page);
  // the same cards in the same order: each card's new revision in place, focus kept
  void update(const cadence_cards_v1_Page& page);
  void empty();
  void focusInitial();
  [[nodiscard]] Rml::Element* el(const char* id) const;

  Env env_;
  Alive alive_;
  PageRpc& rpc_;
  const DeviceState& device_;
  std::unique_ptr<cards::CardHost> cards_;
  std::vector<int> hooks_;
  Rml::ElementDocument* doc_ = nullptr;

  std::string feedId_;
  std::vector<nearby::Item> items_;
  std::string itemsSig_;
  std::uint64_t revision_ = 0;

  bool open_ = false;
  bool under_ = false;
  std::string focus_;
  std::string panelId_;
  cards::PagePtr page_;
  // the panel's first page places focus (on the focus device's card); later ones keep it
  bool placed_ = false;
};

// the devices part of the router's last attach (home reads its feed; null without the system layer)
Devices* devicesPart();

}  // namespace cadence::screen::routes
