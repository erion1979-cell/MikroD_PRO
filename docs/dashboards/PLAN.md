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

1. **Named dashboards.** **Done.** The dashboards after the first are stored per user in a new
   `user_layouts` row, `dashboards` (migration 36), validated by the server; the first keeps its own
   row, so nothing changes for anyone until they add a second. The tab strip sits above the grid;
   switching applies that dashboard's layout, and the existing editor edits and saves the active one.
2. **Cards for any device.** **Done for the first six.** Every frame a router's session sends names
   that router (hub `Envelope.Router`); in the browser `socket.on` hears another router's frames
   never, and `socket.onRouter` hears them all (`web/src/socket.ts`). `dash:watch` carries the
   (router, card, interface) set the dashboard on screen shows; the server joins those rooms on each
   router under the checks selecting it would pass, replays the last readings tagged, and lets demand
   run the collectors (`internal/server/dashwatch.go`). The six cards were made into factories drawing
   into a scope (`web/src/pages/dashboard-card-scope.ts`), so a copy is the original card cloned and
   drawn by the same code (`dashboard-device-cards.ts`); the Dashboard's own copy is one of them. A
   Traffic copy's backlog comes on `dash:traffic-history`, which the original chart never adopts.
   The other card types follow in groups.
3. **Load.** **Done.** Each extra device on screen keeps its collectors running while the dashboard
   is open, so the tab strip shows how many devices the dashboard on screen reads (a blue pill,
   naming them on hover). A dashboard's cards may name at most 8 devices (`dashDevicesMax`, with
   `DASH_DEVICES_MAX` in the browser held to it by `web/test/dashboard-tabs.test.ts`); cards following
   the selection do not count, since that device is read anyway. The server refuses a ninth when
   saving, and a `dash:watch` set reads at most those 8 plus the selection, so no browser can run more
   routers' collectors than a saved dashboard could. In a card's device picker the devices past the
   limit are disabled.
4. **Sharing** (later): an administrator publishes a dashboard; each viewer still sees only the
   devices and pages their role allows.
