# Power/UPS module — agreed plan

Agreed 2026-10-03. Read [HANDOFF.md](HANDOFF.md) first for the requirements; this file records
how they are being built and the decisions taken.

## Decisions

| Question | Decision |
|---|---|
| Language | Go for the server, TypeScript for the page, like the rest of MikroDash. One static binary; no Python or Node at run time. The Python prototype stays here as a decoding reference only. |
| Name in the app | **Power/UPS** (page key `power-ups`, URL `/power-ups`). A separate nav entry, not part of Devices. |
| First producer | **PowerGuard**. No other brand name for these units appears anywhere in the repository. |
| Model definitions | JSON files embedded in the binary, one per model. Adding a model is adding a file. |
| Alerts | Shown on the Power/UPS page, not in the router alert bell or the Alerts report. Notifications still go out through the existing notification channels. |
| Turning polling off | `-no-pool` (the existing switch for a second MikroDash watching the same fleet) also stops Power/UPS polling. `-history` gates recording and `-alert-dispatch` gates sending, as for routers. |
| Modbus | In-house read-only client: function 04, and 03 if a model needs it. It cannot build a write request. No new dependency. |

## Where it lives

```
internal/power/modbus   Modbus TCP read client
internal/power/model    model definitions (defs/<producer>/<model>.json) and the generic decoder
internal/power          pure logic (mode, events, offline after N failures, minute buckets)
                        and the pollers: one per converter address, one persistent connection,
                        its units polled one after another
internal/server/power*.go   REST and WebSocket wiring
web/src/pages, web/src/ui  the page's script and markup, named after its key
```

Existing files are touched only to hook in: the page list, a database migration, retention, the
alert catalogue (for channel toggles) and its ledger, a site-scoped permission check in
`internal/rbac`, server start-up and routes, settings defaults, one dashboard card, the Settings
section, a Reports tab, and the docs whose numbers the tests check.

## Model definition format

Each model maps its registers onto fixed names (`input_v`, `output_v`, `battery_pct`, `mains_ok`,
`output_on`, …). The page, alerts and history read only those names, so they work for every model.
The mode rule is written once: a fault event code → fault; else mains OK → on mains; else inverter
running → on battery; else output off.

## Storage (SQLite)

| Table | Holds |
|---|---|
| `power_units` | name, site, producer, model, converter IP, port, slave ID, optional linked router, optional battery Ah, enabled |
| `power_samples` | one row per unit, per minute, per measurement: average, minimum, maximum; plus poll success and reply time |
| `power_events` | kind, code, text, start, end (so durations can be shown) |

## Build order

1. Modbus client, tested against a fake converter.
2. Definition format, loader and the PowerGuard definition, tested against the verified readings.
3. Pure logic: mode, events, offline rule, minute buckets.
4. Storage, pollers, server wiring, REST for adding units.
5. The Power/UPS page and live updates.
6. Notifications.
7. Dashboard card, Reports tab, CSV export.

Live testing against a real unit happens on the owner's network, with the read-only
`cmd/powerprobe`. It uses the same Modbus client, model definition and event tracker as the
server, so what it prints is what the page will show:

```bash
docker run --rm --network host -v "$PWD":/src -w /src golang:1.27-alpine \
  go run ./cmd/powerprobe -host 192.168.20.83 -count 0
```

`-port`, `-slave` and `-model` default to 502, 1 and `powerguard/modbus-v1.1`; `-raw` also prints
every register; `-models` lists the definitions.
