# Inverter / UPS monitoring module — handoff

Read this first. It summarizes the design work done before coding started: requirements,
decisions, what has been verified on real hardware, and what is still open.

Companion files in this folder:

| File | What it is |
|---|---|
| `powerguard-register-map.md` | Modbus register map for the first supported inverter, **verified on a real unit** |
| `converters.md` | How to configure the Ethernet-to-RS485 converters (E30 and ZLAN7144N2) |
| `prototype/inverter_monitor.py` | Working Python prototype (stdlib only): polls the inverter, decodes, detects events, serves a live page. Use it as the reference for decoding and event logic. |
| `mockups/*.png` | Agreed look of the per-inverter page in three states (on mains, on battery, overload fault) |

The owner is not a programmer. Explain choices in plain language, ask before big or
irreversible steps, and propose a plan before writing code.

---

## 1. Goal

Remotely monitor inverters and UPSs installed at customer sites. Each unit has an RS485
Modbus RTU port. At each site an **Ethernet-to-RS485 converter** in Modbus TCP gateway
mode exposes the unit as **Modbus TCP**. A central server polls every unit, stores
history, shows dashboards and sends alerts.

## 2. Decisions already made

1. **Build it inside this MikroDash fork** (repo `MikroD_PRO`), not as a separate app, to
   reuse MikroDash's login and roles, Sites, the Devices/fleet page, notifications
   (Telegram, email, ntfy, Pushbullet), the SQLite history database, Reports and the look
   and feel. A standalone panel ("MikroPanel") was dropped.
2. **Keep upstream merges painless.** MikroDash has no plugin system, so the inverter
   code is a new module: put it in its own packages and folders (for example
   `internal/inverter/...` and `web/src/inverter/...`) and touch existing files only
   where it must hook in (routes, sidebar/nav, settings, alert types, collector
   registry). Follow MikroDash's existing patterns (collectors, sessions, alert rules,
   store, WebSocket protocol) rather than inventing new ones.
3. **Must keep running as a container on small MikroTik routers**, like MikroDash
   (amd64, arm64, armv7; static Go binary). No heavy dependencies.
4. **Several producers, not just one.** Scope covers inverters and UPSs from multiple
   brands (PowerGuard first; SVC and others later). Organize as
   **producer → model**, each model with its own register map, scaling, status-bit and
   event-code definitions. Adding a model should mean adding a definition, not new
   polling code.
5. **Read-only.** Use only Modbus function 04 (and 03 if a model needs it). Never send
   write function codes (05, 06, 0F, 10): the inverter accepts them and they can change
   real settings.
6. **Minimal Modbus client.** MikroDash keeps very few dependencies, each with a reason.
   A Modbus TCP read client is about 100 lines (see the prototype's `ModbusTCP` class);
   writing it in-house is preferred over adding a library, unless there is a good reason.

## 3. Polling and storage rules

| Item | Value | Why |
|---|---|---|
| Background poll | **every 5 s** | Mains loss is only visible live (see §5), so short outages need a short interval. Traffic is tiny (~0.3 s per poll, ~1.5 MB/day/unit). |
| Fastest allowed | not under 1–2 s | No benefit; inverter may answer "busy" (exception 06) |
| Request | function 04, address 0, 36 registers, one request per poll | |
| Reply timeout | 1–2 s | |
| Offline | after **3 consecutive failed polls** | Avoids false alarms from short VPN glitches; occasional single failures happen during mains/battery switch-over |
| Connection | keep **one persistent TCP connection** per converter; reconnect on error | Converters accept few clients and may drop the oldest |
| History | store **one point per minute** (average + min + max) | Small database, graphs look the same, dips stay visible |
| Several units on one converter | poll sequentially | Only if slave IDs differ (see open questions) |
| Intervals | configurable in Settings, like MikroDash's other poll intervals | |

## 4. Features wanted (first version)

- **Inverter list / fleet view**, grouped by MikroDash Site, with status colours
  (on mains / on battery / fault / not responding).
- **Per-inverter page** as in `mockups/`: status banner with the status bits, power-flow
  view, input/output/load/battery/temperature cards, 24 h / 7 d / 30 d history charts,
  events list, connection info (converter IP, slave ID, reply time, success rate).
- **Dashboard card** for inverter status of the current site.
- **Alerts through MikroDash's notification channels**: mains lost / restored, event
  code raised / cleared, output switched off, battery low, inverter not responding /
  back. With cooldowns like MikroDash's existing alerts.
- **Reports** tab for inverter history with CSV export.
- **Settings** section to add units: name, site, producer, model, converter IP, port,
  slave ID.
- Show **apparent power = output V × output A** marked as "calculated" (not a register).
- Battery estimated runtime needs battery Ah, which the inverter does not report: leave
  as a field the user can fill in later, or omit.

## 5. Behaviour learned from the real PowerGuard unit

(Details in `powerguard-register-map.md`.)

- It is a **line-interactive (offline) inverter**: on mains, "inverter running" is OFF
  and mains passes through; on battery, the inverter switches on. Status register 032
  reads **259 on mains** and **260 on battery**.
- **Mains loss is NOT an event code.** Event codes (register 035) are protection trips
  only. An outage is visible only as status bit 0 going low while it lasts; the inverter
  keeps no record. The platform must detect and timestamp it itself.
- **Battery % is voltage-based** and jumps to 100 % as soon as the charger runs (47 % →
  100 % in minutes). Show battery voltage next to %, and trust % only on battery.
- **DC bus current** stayed 0.0 A while charging; probably discharge current only.
- **Ambient temperature equals internal** in every reading so far; maybe no separate sensor.

## 6. Network and security

Modbus has **no authentication and no encryption**, and the converters can be found and
reconfigured by anyone on the same LAN. Security is done at the network level:

- Converters on their own VLAN, or at least firewalled so only the MikroDash server can
  reach TCP 502 (and block UDP 8168 on the E30, the search/config port).
- Sites reach the server over a VPN (WireGuard on the site MikroTik). Never port-forward
  Modbus to the internet.
- Change converter web passwords; disable converter cloud features (E30 "Edge
  collection", ZLAN P2P/N2N) and secure the ZLAN's open Wi-Fi hotspot.
- MikroDash's own roles and Sites control who sees which inverters.

## 7. Open questions (asked or to ask the manufacturer)

1. Bit definitions of registers **033 (warning)** and **034 (error)**.
2. **Holding register map (000–199)**: settings; also whether one holds the slave ID.
3. **How to change the Modbus slave ID** (panel, software or register). If fixed at 01,
   each inverter needs its own converter.
4. Meaning of registers **028 = 105** and **029 = 122** (constant so far).
5. Functions **14H "history record"** and **2BH "software version"** are listed but not
   documented. If the inverter keeps its own event history, the platform could recover
   events that happened between polls.

Not yet tested on the real unit: on battery **with a load** (registers 004, 006, 012).

## 8. Suggested first steps for Claude Code

1. Read MikroDash's `CLAUDE.md`, `AI_CONTEXT.md` and the architecture section of
   `README.md`; study how a collector, its settings, alerts, history and a page are wired.
2. Propose a short plan: where the module plugs in, the producer/model definition format,
   data model and storage, which existing files change. Wait for approval.
3. Build in small steps, starting with the Modbus client + PowerGuard decoder with
   unit tests (use the verified readings in the register map as test vectors), then the
   collector, then the page, then alerts.
