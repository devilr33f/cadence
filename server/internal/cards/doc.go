// Package cards is Cadence Cards in production (W0-C items 10-12): the cards
// the server sends screens, the actions it issues for them, and the
// card.action and cards.watch calls that run those actions without the model
// and push live revisions. pkg/cards builds and checks the cards; this
// package renders them from their subjects (a Home Assistant entity, search
// results, a found page) and keeps what each device was sent.
//
// The wire, as a screen sees it:
//
//   - every card a device holds was issued to that device; its actions are
//     opaque random ids bound to the device, the card and its revision, with
//     the card's ttl (DefaultTTL when it sets none), and single use unless
//     idempotent (a slider, a link). A restart forgets them all.
//   - card.action {cardId, revision, actionId, value?}: ok, refused (a policy
//     said no), expired (an older revision, or past its ttl), not_found (an
//     action this device was never issued) or invalid (a value the primitive
//     does not take). The answer is typed, RpcResponse.card_action (a
//     cards.v1 CardActionResult): the card's next revision, the page an action
//     opened (a link, a post, a task), or a refusal with its reason, the toast
//     in the screen's language and the card as the server holds it, so the
//     screen's optimistic change rolls back. A panel's opener is idempotent and
//     its card is not drawn again. The json, ActionAnswer {code, toast,
//     revision}, stays for one release.
//   - a page's next action (Page.next): card.action {pageId, actionId}, the
//     page's id as Page.id carries it; one with neither id (a screen before
//     page ids) is taken for one release. The answer is the next page.
//   - subscribed, card.action and cards.watch {cardIds} push each live card's
//     next revision (Home Assistant's state changed elsewhere) as a Cards
//     envelope with the call's id, until RpcCancel, the link's end or the
//     card's ttl; then the call's last RpcEvent. A drawer's cards arrive in
//     AssistantTurn; the screen watches them with cards.watch.
//   - cards.lease {leaseId, cardIds} and cards.release {leaseId} or {all}: after a turn's Home Assistant write, a
//     screen whose lease holds the entity's card gets its next states as Cards frames (context "live"), at most 2 a
//     second, for a few minutes past the write (lease.go).
//
// A press on the remote is policy kind screen (ScreenTrigger): a pressed
// session for HASensitivePolicy, with a confirm on every sensitive control;
// no card ever controls a Cadence speaker's own entity, so no card action
// raises a device's volume (rule 7). Actions are recorded in the device's
// conversation history as tool calls of trigger screen (Recorder).
//
// Entity ids and names, search text and urls are never logged (rule 8):
// lines carry device ids, opaque card ids and codes.
package cards
