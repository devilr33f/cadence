#include "app/routes/preview/fake-devices.h"

#include "app/routes/preview/fake-catalog.h"

#include <cstring>

namespace cadence::screen::routes::fake {

namespace {

// a protobuf message by hand (the cards' wire format: varints, length-delimited fields, fixed64 doubles), so a
// made-up card reads as the server's own bytes do
class Pb {
 public:
  Pb& u(int field, std::uint64_t v) {
    key(field, 0);
    varint(v);
    return *this;
  }
  Pb& s(int field, const std::string& v) {
    key(field, 2);
    varint(v.size());
    b_ += v;
    return *this;
  }
  Pb& m(int field, const Pb& v) { return s(field, v.b_); }
  Pb& d(int field, double v) {
    key(field, 1);
    std::uint64_t bits = 0;
    std::memcpy(&bits, &v, sizeof bits);
    for (int i = 0; i < 8; ++i) b_ += static_cast<char>((bits >> (8 * i)) & 0xff);
    return *this;
  }
  [[nodiscard]] const std::string& bytes() const { return b_; }

 private:
  void key(int field, int wire) { varint(static_cast<std::uint64_t>(field) << 3 | static_cast<std::uint64_t>(wire)); }
  void varint(std::uint64_t v) {
    while (v >= 0x80) {
      b_ += static_cast<char>((v & 0x7f) | 0x80);
      v >>= 7;
    }
    b_ += static_cast<char>(v);
  }
  std::string b_;
};

// cards.proto's numbers: Node's oneof, the enums
constexpr int kNodeCard = 1, kNodeRow = 2, kNodeColumn = 3, kNodeText = 5, kNodeImage = 8, kNodeSlider = 25;
constexpr int kToneNeutral = 1;
constexpr int kStyleTitle = 1, kStyleBody = 2, kStyleMeta = 3;
constexpr int kAspectSquare = 1;
constexpr int kGapS = 1, kGapM = 2;

Pb node(int kind, const Pb& body) { return Pb().m(kind, body); }

Pb text(const std::string& t, int style) { return node(kNodeText, Pb().s(1, t).u(2, static_cast<std::uint64_t>(style))); }

Pb button(const std::string& label, const std::string& icon, const std::string& action, bool primary = false) {
  Pb b;
  if (!label.empty()) b.s(1, label);
  if (!icon.empty()) b.s(2, icon);
  b.m(3, Pb().s(1, action));
  if (primary) b.u(4, 1);
  return b;
}

// a volume slider as the server sends one: 0-100 by 5, its label the member's name
Pb volume(const std::string& label, int level, const std::string& action) {
  Pb s = Pb().d(1, level).d(2, 0).d(3, 100).d(4, 5).s(5, "%").s(6, label).s(7, std::to_string(level) + " %");
  s.m(8, Pb().s(1, action));
  return node(kNodeSlider, s);
}

struct Member {
  std::string name;
  int level;
};

struct Device {
  std::string id;
  std::string name;
  std::string room;
  std::string title;
  // the rest of the title the panel shows (a long one wraps to two lines there); the feed keeps it short, as the
  // strip's goldens were made with it
  std::string titleRest;
  std::string artist;
  std::string album;
  // the source line: the service, or how it reached the speaker
  std::string source;
  int art;
  bool paused;
  bool here;
  // a stream with no length: «В эфире» in place of the timeline
  bool live;
  // the timeline takes the remote (a seek), or is shown alone
  bool seek;
  double positionS;
  double durationS;
  std::vector<Member> members;
};

const std::vector<Device>& devices() {
  static const std::vector<Device> d = {
      {"kitchen-speaker", "Кухня", "Гостиная", "Северное сияние", "", "Полярные огни", "Зимние песни", "YouTube Music", 42, false,
       false, false, true, 83, 225, {{"Кухня", 35}}},
      {"living-pickle", "Колонка в гостиной", "Гостиная", "Тёплый ветер", " над спящим городом и рекой (живая версия)",
       "Лесное эхо", "Дорога домой", "AirPlay", 57, true, true, false, false, 61, 215,
       {{"Колонка в гостиной", 20}, {"Телевизор", 30}}},
      {"bedroom-midi", "Спальня", "Спальня", "Колыбельная для города", "", "Ночные огни", "", "Радио", 63, false, false, true,
       false, 0, 0, {{"Спальня", 15}}},
  };
  return d;
}

std::string cover(int seed) { return "img:" + imageId(seed); }

// a device's card as the server draws it (server/internal/remote): the device in the header with its room and
// state, the cover beside the track's lines, the position, the transport, a volume slider per member, and «play
// here too» or «stop here»
Pb deviceCard(const Device& d, std::int64_t anchorMs) {
  const std::string id = "devices:" + d.id;
  const std::string state = d.paused ? "пауза" : "играет";
  Pb col;
  col.u(2, kGapS);
  col.m(1, text(d.title + d.titleRest, kStyleTitle));
  col.m(1, text(d.artist, kStyleBody));
  if (!d.album.empty()) col.m(1, text(d.album, kStyleMeta));
  col.m(1, text(d.source, kStyleMeta));
  if (d.live) col.m(1, text("В эфире", kStyleMeta));
  Pb row;
  row.u(2, kGapM);
  row.m(1, node(kNodeImage, Pb().s(1, cover(d.art)).s(2, "Обложка").u(3, kAspectSquare)));
  row.m(1, node(kNodeColumn, col));

  Pb block;
  block.s(1, d.name).s(2, d.here ? std::string("Играет и здесь") : d.room + " · " + state).s(3, "music").u(4, kToneNeutral);
  block.m(5, node(kNodeRow, row));
  if (!d.live) {
    // the position runs on the screen from its anchor: a playing session's card needs no revision a second
    Pb timeline;
    if (anchorMs > 0) timeline.u(1, static_cast<std::uint64_t>(anchorMs));
    timeline.d(2, d.paused ? 0.0 : 1.0);
    Pb pos = Pb().d(1, d.positionS).d(2, 0).d(3, d.durationS).d(4, 10).s(6, "Позиция");
    if (d.seek) pos.m(8, Pb().s(1, id + ":seek"));
    pos.m(9, timeline);
    block.m(5, node(kNodeSlider, pos));
  }
  Pb transport;
  transport.u(2, kGapS);
  if (!d.live) transport.m(1, node(16, button("Назад", "previous", id + ":previous")));
  transport.m(1, node(16, d.paused ? button("Продолжить", "play", id + ":resume", true)
                                   : button("Пауза", "pause", id + ":pause", true)));
  if (!d.live) transport.m(1, node(16, button("Дальше", "next", id + ":next")));
  block.m(5, node(kNodeRow, transport));
  for (std::size_t i = 0; i < d.members.size(); ++i) {
    block.m(5, volume(d.members[i].name, d.members[i].level, id + ":volume:" + std::to_string(i)));
  }
  block.m(6, d.here ? button("Отключить здесь", "close", id + ":leave") : button("Играть и здесь", "share_to_speaker", id + ":join"));
  Pb card;
  card.u(1, 1).s(2, id).u(3, 1).s(6, d.name + ": " + state).m(7, node(kNodeCard, block));
  for (const char* f : {"button", "column", "image", "row", "slider", "text"}) card.s(8, f);
  if (!d.live) card.s(8, "slider.timeline");
  return card;
}

}  // namespace

Json devicesMedia() {
  Json items = Json::array();
  for (std::size_t i = 0; i < devices().size(); ++i) {
    const auto& d = devices()[i];
    items.push_back({{"id", "session-" + d.id},
                     {"deviceId", d.id},
                     {"name", d.name},
                     {"room", d.room},
                     {"sameRoom", i < 2},
                     {"source", "url"},
                     {"state", d.paused ? "paused" : "playing"},
                     {"title", d.title},
                     {"artist", d.artist},
                     {"art", cover(d.art)},
                     {"positionMs", 61000},
                     {"durationMs", 215000},
                     {"here", d.here}});
  }
  return Json{{"items", items}};
}

std::vector<std::uint8_t> devicesPage(std::int64_t anchorMs) {
  std::vector<std::string> cards;
  for (const auto& d : devices()) cards.push_back(deviceCard(d, anchorMs).bytes());
  return pageOf(cards);
}

std::vector<std::uint8_t> pageOf(const std::vector<std::string>& cardBytes, const std::string& id) {
  Pb page;
  page.u(1, 1);
  for (const auto& c : cardBytes) page.s(2, c);
  page.s(4, id);
  const auto& b = page.bytes();
  return {b.begin(), b.end()};
}

}  // namespace cadence::screen::routes::fake
