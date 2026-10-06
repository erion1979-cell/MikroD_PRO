# Multiple dashboards, cards from any device - agreed plan

Agreed 2026-10-06 with the owner.

## Decisions

| Question | Decision |
|---|---|
| Several dashboards | Yes: named dashboards as tabs on the Dashboard page, added, renamed, duplicated, reordered and deleted by the user. |
| Device per card | Each card either follows the selected device (today's behaviour) or is fixed to one device. |
| Repeat cards | A card type may appear several times on one dashboard, each instance for its own device. |
| Ownership | Personal first. Publishing a dashboard to other users comes later. |
| First cards for any device | Traffic, WAN Flow, Bandwidth, System, Ping, Physical Ports. The rest follow in groups. |

## Where things stand today

- A browser connection follows ONE router (`cn.routerID`), and every card's room is named after it
  (`router-<id>-dash-card-<key>`).
- The WebSocket envelope is `{event, data}`: a payload does not say which router it came from, so
  one connection cannot tell two routers' `traffic:update` apart.
- Each card's markup exists once in `web/src/ui/page-dashboard.html`, with fixed element ids, so a
  card type cannot appear twice.
- The layout is one list of every card type with a `visible` flag, saved per user in the
  `user_layouts` row `dashboard` (a storage key, never renamed) and cached in `localStorage`.

## The end state

A dashboard is `{id, name, cards: [{uid, type, router, x, y, w, h}]}`. `router` empty means "the
selected device". Every card is an instance; the old singleton cards are the instances of types not
yet converted, which can appear once per dashboard and follow the selected device.

## Phases

1. **Named dashboards.** **Done.** The dashboards are stored per user in a new `user_layouts` row,
   `dashboards`, validated by the server. The first is built from the existing layout, so nothing
   changes for anyone until they add a second. The tab strip sits beside the title; switching
   applies that dashboard's layout to the grid, and the existing editor edits the active one.
2. **Cards for any device.** A per-connection set of watched `(router, card type)` pairs, served the
   way the device modal is (`internal/server/peek.go`): joining a demand room on each watched router
   keeps the collectors those cards need running, and one declared event carries each router's
   readings with its router id. Card types are converted to render into instances created from a
   template, starting with the six above. Every watch is checked against the viewer's grant on
   that router and the card's page, as a card room is today.
3. **Load.** Each extra device on screen keeps its collectors running while the dashboard is open.
   A dashboard shows how many devices it reads, and the number of fixed devices is capped.
4. **Sharing** (later): an administrator publishes a dashboard; each viewer still sees only the
   devices and pages their role allows.
