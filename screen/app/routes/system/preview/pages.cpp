#include "app/routes/system/preview/pages.h"

#include "app/routes/preview/fake-devices.h"
#include "app/routes/system/preview/fixture.h"
#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>

namespace cadence::screen::routes::syspreview {

namespace {

using ui::preview::Context;
using ui::preview::Page;

// a film's frame behind the ui (the video plane's stand-in: under 90 % of the screen, so it is no ui layer)
std::string film() {
  return "<div style=\"position: absolute; left: 0dp; top: 0dp; width: 1700dp; height: 1000dp; "
         "decorator: image(&quot;gen:art:film-7&quot; cover);\"/>"
         "<div class=\"subtitles\"><span>Мы не успеем до рассвета.</span></div>";
}

// home's stand-in: a row of films under the top bar the routes add
std::string homeBody(const std::string& topBar) {
  ui::rml::Row r;
  r.id = "films";
  r.title = "Фильмы и сериалы";
  r.kind = ui::rml::CardKind::Poster;
  const char* names[] = {"Тихая гавань", "Северный ветер", "Последний поезд", "Город огней", "Дом у озера", "Белые ночи"};
  for (int i = 0; i < 6; ++i) {
    ui::rml::Card c;
    c.kind = ui::rml::CardKind::Poster;
    c.id = "films-" + std::to_string(i);
    c.title = names[i];
    c.meta = "2024 · Драма";
    c.art = "gen:art:" + std::to_string(i + 11);
    c.action = "open";
    r.cards.push_back(c);
  }
  return "<div class=\"safe\"><div style=\"position: absolute; right: 0dp; top: 0dp;\">" + topBar +
         "</div><div class=\"vscroll\" style=\"position: absolute; left: 0dp; top: 440dp;\">" + ui::rml::row(r) + "</div></div>";
}

Fixture& fx(Context& c, std::function<std::string()> body) { return fixtureFor(c.ui, std::move(body)); }

// the drawer opened by a key over a film, the room's speaker answering
Fixture& keyed(Context& c, bool overFilm = true) {
  auto& f = fx(c, overFilm ? std::function<std::string()>(film) : [] { return homeBody({}); });
  f.online();
  f.system().drawer().invoke();
  f.answer("assistant.invoke", "ok", "{\"turnId\":\"turn-1\",\"micDeviceId\":\"speaker-1\"}");
  return f;
}

Turn listening() {
  Turn t;
  t.state = cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING;
  t.stable = "Включи свет в";
  t.unstable = "гостиной";
  return t;
}

Turn speaking() {
  Turn t;
  t.state = cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING;
  t.stable = "Включи свет в гостиной на семьдесят процентов";
  t.steps = {{"s1", "Свет в гостиной: яркость 70 %", cadence_v2_StepStatus_STEP_STATUS_DONE}};
  t.reply = "Включил свет в гостиной на 70 %. Скажите, если нужно ярче.";
  // the first four words sound in the first second, the rest later
  const std::uint32_t ends[] = {8, 14, 16, 25, 33, 41, 46, 53, 59};
  const std::uint32_t starts[] = {0, 9, 15, 17, 3000, 3400, 3800, 4200, 4600};
  std::uint32_t from = 0;
  const std::uint32_t at[] = {0, 300, 600, 900, 3000, 3400, 3800, 4200, 4600};
  for (std::size_t i = 0; i < 9; ++i) {
    cadence_v2_WordTiming w = cadence_v2_WordTiming_init_zero;
    w.text_start = i == 0 ? 0 : from;
    w.text_end = ends[i];
    w.start_ms = at[i];
    w.end_ms = at[i] + 250;
    from = ends[i] + 1;
    (void)starts;
    t.words.push_back(w);
  }
  t.cards = {"card_light_living.binpb"};
  return t;
}

Turn webAnswer(cadence_v2_AssistantState s) {
  Turn t;
  t.state = s;
  t.stable = "Когда выйдет третий сезон монолога фармацевта";
  t.steps = {{"s1", "Ищу в сети: монолог фармацевта", cadence_v2_StepStatus_STEP_STATUS_DONE},
             {"s2", "Читаю 2 источника", cadence_v2_StepStatus_STEP_STATUS_DONE}};
  t.reply = "Третий сезон выйдет в октябре 2026 года [1], и он уже в списке осенних премьер [2].";
  t.citations = {{1, "web.answer.w7"}, {2, "web.answer.w7"}};
  t.cards = {"card_web_answer.binpb"};
  t.followUpMs = 6000;
  return t;
}

// a conversation's first two rounds, both answered: the light, then the weather
void history(Fixture& f) {
  Turn a;
  a.state = cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP;
  a.stable = "Включи свет в гостиной";
  a.steps = {{"s1", "Свет в гостиной: яркость 70 %", cadence_v2_StepStatus_STEP_STATUS_DONE}};
  a.cards = {"card_light_living.binpb"};
  f.deliver(a.envelope());
  a.revision = 2;
  a.reply = "Включил свет в гостиной на 70 %.";
  a.followUpMs = 60000;
  f.deliver(a.envelope());
  Turn b;
  b.id = "turn-2";
  b.state = cadence_v2_AssistantState_ASSISTANT_STATE_THINKING;
  b.stable = "Какая погода завтра?";
  b.steps = {{"s1", "Погода: Москва", cadence_v2_StepStatus_STEP_STATUS_DONE}};
  b.cards = {"card_weather.binpb"};
  f.deliver(b.envelope());
  b.revision = 2;
  b.state = cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP;
  b.reply = "Завтра плюс четыре и облачно, к вечеру дождь.";
  b.followUpMs = 60000;
  f.deliver(b.envelope());
}

// the third round: listening, its partial transcript
Turn third(std::uint32_t revision, const char* stable, const char* unstable) {
  Turn t;
  t.id = "turn-3";
  t.revision = revision;
  t.state = cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING;
  t.stable = stable;
  t.unstable = unstable;
  return t;
}

void deliver(Fixture& f, Turn& t) { f.deliver(t.envelope()); }
void deliver(Fixture& f, Turn&& t) { f.deliver(t.envelope()); }

Page page(std::string name, std::function<std::string()> body, std::function<void(Context&)> setup, bool walk = true) {
  Page p;
  p.name = std::move(name);
  p.body = std::move(body);
  p.setup = std::move(setup);
  p.walk = walk;
  return p;
}

std::unique_ptr<PageOf> approvalPage() {
  std::vector<cards::CardPtr> c;
  c.push_back(loadCard("card_task_running.binpb"));
  c.push_back(loadCard("card_hermes_approval.binpb"));
  return std::make_unique<PageOf>(std::move(c));
}

void enrollClaim(Fixture& f) {
  f.deliver(linkStatus(cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED));
  f.deliver(claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_PENDING, "K7M4QX", f.epoch() + (9 * 60 + 41) * 1000 + 1500));
}

}  // namespace

std::vector<Page> pages() {
  std::vector<Page> out;
  const auto homeEmpty = [] { return homeBody({}); };

  // ---------- the drawer's seven states ----------
  out.push_back(page("drawer-invoked", film, [](Context& c) {
    auto& f = fx(c, film);
    f.online();
    // the press opened it: invoked, listening starts at once on the room's microphone
    f.system().drawer().invoke();
  }));
  out.push_back(page("drawer-listening", film, [](Context& c) {
    auto& f = keyed(c);
    deliver(f, listening());
    f.deliver(voiceLevel("turn-1", 200, 0));
  }));
  out.push_back(page("drawer-thinking", film, [](Context& c) {
    auto& f = keyed(c);
    Turn t = webAnswer(cadence_v2_AssistantState_ASSISTANT_STATE_THINKING);
    t.reply.clear();
    t.cards.clear();
    t.steps = {{"s1", "Ищу в сети: монолог фармацевта", cadence_v2_StepStatus_STEP_STATUS_DONE},
               {"s2", "Читаю 4 источника", cadence_v2_StepStatus_STEP_STATUS_FAILED},
               {"s3", "Ищу в X", cadence_v2_StepStatus_STEP_STATUS_RUNNING}};
    deliver(f, t);
  }));
  // no walk: focus stays on «Стоп» while the assistant speaks and never moves into a card (↑ ↓ are held); the
  // follow-up page walks the same card
  out.push_back(page("drawer-speaking", film, [](Context& c) {
    auto& f = keyed(c);
    deliver(f, speaking());
    f.deliver(voiceLevel("turn-1", 0, 160));
  }, false));
  out.push_back(page("drawer-follow-up", film, [](Context& c) {
    auto& f = keyed(c);
    Turn t = speaking();
    t.state = cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP;
    t.followUpMs = 6000;
    deliver(f, t);
  }));
  out.push_back(page("drawer-resting", film, [](Context& c) {
    auto& f = keyed(c);
    Turn t = speaking();
    t.state = cadence_v2_AssistantState_ASSISTANT_STATE_RESTING;
    deliver(f, t);
  }));
  out.push_back(page("drawer-citations", homeEmpty, [](Context& c) {
    auto& f = keyed(c, false);
    deliver(f, webAnswer(cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP));
    // the marker [2] focused lights its source
    c.ui.after(100, [&c] {
      if (auto* d = c.ui.documentNamed("drawer")) {
        Rml::ElementList marks;
        d->QuerySelectorAll(marks, ".cmark.f");
        if (marks.size() > 1) c.ui.focus().focus(marks[1]);
      }
    });
  }));
  // a wake word said to the room's speaker: no focus, the hint at the foot, the page's focus where it was
  out.push_back(page("drawer-wake", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    if (auto* e = c.ui.page()->GetElementById("films-1")) c.ui.focus().focus(e);
    Turn t = listening();
    t.trigger = cadence_v2_Trigger_TRIGGER_WAKE;
    t.stable = "Что идёт по";
    t.unstable = "телевизору";
    deliver(f, t);
  }, false));
  out.push_back(page("drawer-no-mic", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    f.system().device().mic = Mic::None;
    f.system().drawer().invoke();
  }));
  out.push_back(page("drawer-setup-qr", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    f.system().device().mic = Mic::None;
    f.system().drawer().invoke();
    c.ui.after(50, [&c] {
      // «Как подключить»
      platform::KeyEvent ok{cadence_screen_v1_Key_KEY_OK, cadence_screen_v1_KeyAction_KEY_ACTION_PRESS};
      c.ui.key(ok);
    });
  }));
  out.push_back(page("drawer-mic-offline", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online("Кухня");
    f.system().drawer().invoke();
    f.answer("assistant.invoke", "unavailable", "{\"code\":\"mic_offline\",\"toast\":\"Колонка не в сети\"}");
  }));
  out.push_back(page("drawer-offline", film, [](Context& c) {
    auto& f = fx(c, film);
    f.deliver(linkStatus(cadence_screen_v1_LinkState_LINK_STATE_OFFLINE, "unreachable"));
    f.system().drawer().invoke();
  }));
  out.push_back(page("drawer-not-heard", film, [](Context& c) {
    auto& f = keyed(c);
    Turn t = listening();
    t.stable.clear();
    t.unstable.clear();
    t.error = "not_heard";
    deliver(f, t);
  }));
  out.push_back(page("drawer-failed", film, [](Context& c) {
    auto& f = keyed(c);
    Turn t = webAnswer(cadence_v2_AssistantState_ASSISTANT_STATE_THINKING);
    t.reply.clear();
    t.cards.clear();
    t.steps = {{"s1", "Свет в гостиной", cadence_v2_StepStatus_STEP_STATUS_FAILED}};
    t.stable = "Включи свет в гостиной";
    t.error = "upstream";
    deliver(f, t);
  }));
  // ---------- the drawer as a chat: rounds stack up, older ones smaller and dimmer ----------
  out.push_back(page("drawer-chat-history", film, [](Context& c) {
    auto& f = keyed(c);
    history(f);
    deliver(f, third(1, "А что надеть, если", "пойду гулять"));
    f.deliver(voiceLevel("turn-3", 120, 0));
  }));
  // mid-animation: the third round's bubble 140 ms into its rise, the rounds above gliding up and shrinking
  out.push_back(page("drawer-chat-rising", film, [](Context& c) {
    auto& f = keyed(c);
    history(f);
    c.ui.after(1360, [&f] { deliver(f, third(1, "А что надеть, если", "пойду гулять")); });
  }, false));
  // a partial grows the bubble in place: the words it adds 100 ms into their fade
  out.push_back(page("drawer-chat-words", film, [](Context& c) {
    auto& f = keyed(c);
    history(f);
    deliver(f, third(1, "А что", "надеть"));
    c.ui.after(1400, [&f] { deliver(f, third(2, "А что надеть, если", "пойду гулять")); });
  }, false));
  // reduced motion: the same moment as drawer-chat-rising, already in place (opacity alone fades, over 120 ms)
  out.push_back(page("drawer-chat-reduced", film, [](Context& c) {
    c.ui.setReducedMotion(true);
    auto& f = keyed(c);
    history(f);
    c.ui.after(1360, [&f] { deliver(f, third(1, "А что надеть, если", "пойду гулять")); });
  }, false));
  out.push_back(page("drawer-back-hint", film, [](Context& c) {
    fx(c, film);
    // long BACK held past 250 ms: the mic glyph at bottom centre
    c.ui.key(platform::KeyEvent{cadence_screen_v1_Key_KEY_BACK, cadence_screen_v1_KeyAction_KEY_ACTION_DOWN});
  }, false));

  // ---------- tasks ----------
  out.push_back(page("tasks-home", homeEmpty, [](Context& c) {
    // home's stand-in with i3's part of the top bar
    auto& f = fx(c, [] {
      auto* l = systemLayer();
      return homeBody(l != nullptr ? l->topBar() : std::string());
    });
    f.online();
    f.system().device().mic = Mic::Speaker;
    static cards::PagePtr list = loadPage("page_tasks.binpb");
    f.answer("tasks.list", "ok", "{}", list.get());
    f.system().router().refresh();
    c.ui.after(50, [&c] {
      if (auto* e = c.ui.page()->GetElementById("tasks-ind")) c.ui.focus().focus(e);
    });
  }));
  // ---------- devices ----------
  // the quick panel opened on the living room's speaker (a strip item's OK): its card focused, the kitchen's above
  out.push_back(page("devices-panel", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    f.answer("devices.media", "ok", fake::devicesMedia().dump());
    // the positions anchored at the gallery's epoch: the kitchen's runs, the paused living room's stands
    static cards::PagePtr all = cards::decodePage(fake::devicesPage(kEpochMs));
    static cadence_cards_v1_Page two;
    two = *all;
    two.cards_count = 2;
    f.system().devices().open("living-pickle");
    f.answer("devices.panel", "ok", "{}", &two);
  }));
  // the panel scrolled to the bedroom's live radio: no position, «В эфире» among the track's lines
  out.push_back(page("devices-panel-live", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    f.answer("devices.media", "ok", fake::devicesMedia().dump());
    static cards::PagePtr all = cards::decodePage(fake::devicesPage(kEpochMs));
    f.system().devices().open("bedroom-midi");
    f.answer("devices.panel", "ok", "{}", all.get());
  }));
  out.push_back(page("tasks-list", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    static cards::PagePtr list = loadPage("page_tasks.binpb");
    f.answer("tasks.list", "ok", "{}", list.get());
    f.system().tasks().openList();
  }));
  out.push_back(page("task-approval", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    static auto p = approvalPage();
    f.system().tasks().openList();
    f.system().tasks().showTask(p->page, false);
  }));
  out.push_back(page("task-clarify", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    static std::unique_ptr<PageOf> p;
    std::vector<cards::CardPtr> v;
    v.push_back(loadCard("card_hermes_clarify.binpb"));
    p = std::make_unique<PageOf>(std::move(v));
    f.system().tasks().showTask(p->page, false);
  }));
  out.push_back(page("task-tail", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    static std::unique_ptr<BuiltCard> r1, r2;
    static std::unique_ptr<PageOf> p1, p2;
    std::string lines;
    for (int i = 1; i <= 14; ++i) lines += "Шаг " + std::to_string(i) + ": сравниваю модели по задержке и цене\n";
    r1 = tailCard("task-ab12-now", 1, lines);
    r2 = tailCard("task-ab12-now", 2, lines + "Шаг 15: пишу сводку\nШаг 16: проверяю цифры\n");
    p1 = std::make_unique<PageOf>(std::vector<cards::CardPtr>{});
    p1->cards.push_back(r1->card);
    p1->page.cards = p1->cards.data();
    p1->page.cards_count = 1;
    p2 = std::make_unique<PageOf>(std::vector<cards::CardPtr>{});
    p2->cards.push_back(r2->card);
    p2->page.cards = p2->cards.data();
    p2->page.cards_count = 1;
    auto* tasks = &f.system().tasks();
    tasks->showTask(p1->page, false);
    // the reader scrolled up into the tail; new lines arrive below: the pill, focus and scroll where they were
    c.ui.after(100, [&c, tasks] {
      if (auto* d = c.ui.documentNamed("tasks")) {
        if (auto* t = d->QuerySelector(".clong.tail")) t->SetScrollTop(0);
      }
      tasks->showTask(p2->page, false);
    });
  }));
  out.push_back(page("toast-over-film", film, [](Context& c) {
    fx(c, film);
    c.ui.toast(ui::ToastSpec{"t-1", ui::ToastTone::Warning, "Hermes ждёт ответа: какую модель сравнивать?", "task",
                             "task:ab12", "[[common.open]]", 60000});
  }, false));
  out.push_back(page("toast-focused", film, [](Context& c) {
    fx(c, film);
    c.ui.toast(ui::ToastSpec{"t-1", ui::ToastTone::Warning, "Hermes ждёт ответа: какую модель сравнивать?", "task",
                             "task:ab12", "[[common.open]]", 60000});
    // ↓ into it: nothing on the film takes the key
    c.ui.after(50, [&c] { c.ui.key(platform::KeyEvent{cadence_screen_v1_Key_KEY_DOWN, cadence_screen_v1_KeyAction_KEY_ACTION_DOWN}); });
  }, false));
  out.push_back(page("drawer-over-panel", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    static cards::PagePtr list = loadPage("page_tasks.binpb");
    f.answer("tasks.list", "ok", "{}", list.get());
    f.system().tasks().openList();
    f.system().drawer().invoke();
    f.answer("assistant.invoke", "ok", "{\"turnId\":\"turn-1\",\"micDeviceId\":\"speaker-1\"}");
    deliver(f, listening());
  }, false));

  // ---------- enrollment ----------
  out.push_back(page("enroll-connecting", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.deliver(linkStatus(cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED));
  }, false));
  out.push_back(page("enroll-unreachable", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.deliver(linkStatus(cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED));
    f.deliver(claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_FAILED, "", 0, "unreachable"));
  }));
  out.push_back(page("enroll-no-network", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.deliver(linkStatus(cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED));
    f.deliver(claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_FAILED, "", 0, "no_network"));
  }));
  out.push_back(page("enroll-claim", homeEmpty, [](Context& c) { enrollClaim(fx(c, [] { return homeBody({}); })); }));
  out.push_back(page("enroll-declined", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    enrollClaim(f);
    f.deliver(claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_DECLINED, "K7M4QX", 0));
  }));
  out.push_back(page("enroll-connected", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    enrollClaim(f);
    f.system().device().mic = Mic::Speaker;
    f.deliver(claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_APPROVED, "", 0));
    f.online();
  }, false));
  out.push_back(page("enroll-removed", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.deliver(linkStatus(cadence_screen_v1_LinkState_LINK_STATE_REMOVED));
  }, false));
  out.push_back(page("enroll-code", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    enrollClaim(f);
    c.ui.after(50, [&c] {
      for (auto k : {cadence_screen_v1_Key_KEY_OK, cadence_screen_v1_Key_KEY_OK}) c.ui.key(platform::KeyEvent{k, cadence_screen_v1_KeyAction_KEY_ACTION_PRESS});
    });
  }));
  out.push_back(page("enroll-address", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.deliver(linkStatus(cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED));
    f.deliver(claim(cadence_screen_v1_ClaimPhase_CLAIM_PHASE_FAILED, "", 0, "unreachable"));
    f.system().device().serverUrl = "https://cadence.home.arpa";
    c.ui.after(50, [&c] {
      // «Изменить адрес»
      if (auto* p = c.ui.page()) {
        if (auto* e = p->GetElementById("e-address")) c.ui.focus().focus(e);
      }
      c.ui.key(platform::KeyEvent{cadence_screen_v1_Key_KEY_OK, cadence_screen_v1_KeyAction_KEY_ACTION_PRESS});
    });
  }));

  // ---------- airplay ----------
  out.push_back(page("airplay-pin", film, [](Context& c) {
    auto& f = fx(c, film);
    f.online();
    cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
    e.which_body = cadence_screen_v1_Envelope_airplay_pin_tag;
    e.body.airplay_pin.active = true;
    copyTo(e.body.airplay_pin.pin, "4821");
    copyTo(e.body.airplay_pin.client_name, "iPhone Маши");
    e.body.airplay_pin.expires_at_ms = f.epoch() + 60000;
    f.deliver(e);
  }, false));
  out.push_back(page("airplay-mirroring", film, [](Context& c) {
    auto& f = fx(c, film);
    f.online();
    cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
    e.which_body = cadence_screen_v1_Envelope_mirror_start_tag;
    copyTo(e.body.mirror_start.session_id, "m-1");
    copyTo(e.body.mirror_start.client_name, "iPhone Маши");
    f.deliver(e);
  }, false));

  // ---------- settings, room and microphone ----------
  out.push_back(page("settings", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    f.system().device().mic = Mic::Speaker;
    f.deliver(route(cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS));
  }));
  out.push_back(page("room", homeEmpty, [](Context& c) {
    auto& f = fx(c, [] { return homeBody({}); });
    f.online();
    f.system().device().mic = Mic::Speaker;
    Settings::openRoom(f.system().router());
  }));
  return out;
}

}  // namespace cadence::screen::routes::syspreview
