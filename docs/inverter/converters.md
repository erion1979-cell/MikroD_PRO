# Ethernet-to-RS485 converters — setup

Both converters must run as a **Modbus TCP ↔ RTU gateway**, so the platform speaks plain
Modbus TCP (no RTU-over-TCP needed). Serial side always matches the inverter:
**9600, 8N1**.

## E30-485 (Huayun / "E300", 1 × RS485)

Tested and working with the Top One Power unit.

| Page / section | Setting | Value |
|---|---|---|
| Port1 → UART | Baudrate / Databits / Stopbits / Parity | **9600** / 8 / 1 / NONE |
| Port1 → Advance | Frame Interval (ms) | **5** (10 if replies are cut) |
| | Frame Length | 1460 (default) |
| | **Modbus Gateway** | **ON** (the undocumented setting that enables Modbus TCP ↔ RTU) |
| | Data Cache | **OFF** (could return stale data) |
| | Poll | OFF (the platform polls) |
| | Sync Baudrate (RFC2217) | OFF |
| Port1 → SocketA | Enable / Mode / Port | ON / **TCP Server** / **502** |
| Port1 → SocketB | Enable | OFF |
| Port1 → Register and Heart | Sending method / Directions | OFF / OFF |
| System | DHCP | OFF, static IP from the site LAN |
| System | Web username/password | change from admin/admin |
| Edge collection | — | leave unused (vendor data push) |

Facts from the manual: power 5–36 V DC; default IP 192.168.10.8; web login admin/admin;
LAN search + config without password on **UDP 8168** (firewall it); TCP server accepts
only a few clients and drops the oldest; auto-restarts if no network data for 24 h
(irrelevant with polling); Reload button > 3 s = factory reset.

Errors seen during testing (about half of polls failing) disappeared after applying the
settings above and a reboot.

## ZLAN family (Shanghai Zhuolan): 5143, 5243A, 7104/7144, 7144N2

Not yet tested with the inverter. All ZLAN models share the same settings, configured
with the **ZLVircom** Windows tool (finds devices even on another subnet; more options
under "More advanced options") or the web page.

### Which models can do Modbus TCP <-> RTU

ZLAN rule: only models whose **third digit is 4** have the Modbus gateway.

| Model | Ports | Network | Modbus gateway | Notes |
|---|---|---|---|---|
| ZLAN5143 | 1 x RS232/485/422 | Ethernet | yes | DEF switch = start with defaults (IP 192.168.0.254) |
| ZLAN5243A | **2** x RS232/485/422 | 2 x Ethernet (built-in switch) | yes | each serial port configured separately; 10 TCP connections |
| ZLAN7104 | 1 x RS232/485/422 | Ethernet + Wi-Fi | **no** | transparent only: platform must send **RTU over TCP** |
| ZLAN7144 / 7144N2 | 1 x RS232/485/422 | Ethernet + Wi-Fi | yes | N2 adds vendor P2P/N2N cloud: keep off |

### Settings for the inverter

| Setting | Value |
|---|---|
| IP mode | Static, site LAN address |
| Work mode | TCP Server |
| Conversion protocol (转化协议) | **Modbus TCP<->RTU** (port changes to **502** automatically). On a 7104: NONE, and use RTU over TCP on port 4196 |
| Baud / data / parity / stop | **9600 / 8 / None / 1** |
| Flow control | None |
| Packet interval | default (a few ms) |
| **Storage Modbus gateway** | **Turn OFF** (see below): in More advanced options untick "RS485 multi-host support" and "RS485 bus conflict detection". Configuring via the web page gives non-storage by default |
| RS485 reply timeout (if multi-host left on) | packet interval x (8 + 77 + 5) + 100 ms, about 500 ms is safe |
| Keepalive / registration packet / P2P | off |
| Web password | set one (default empty or 123456 depending on firmware) |
| ZLAN5243A port 2 | same settings; if both ports share one IP, give port 2 its own TCP port (e.g. 503) |

**Storage vs non-storage.** When Modbus TCP<->RTU is selected in ZLVircom, ZLAN turns on
"storage" mode: the gateway remembers each query, **re-sends it on RS485 continuously**
(every ~20-50 ms) and answers the network from its cache. A query not repeated within
**5 s** is dropped. For us this is unwanted: it hammers the inverter non-stop, our 5 s poll
sits exactly on the expiry limit, and cached replies could hide an inverter that stopped
answering. Non-storage = one RS485 query per platform poll. Trade-off: non-storage also
disables multi-host, so only one client (the platform) should poll at a time.

**Test to do:** with the gateway set up, unplug the RS485 cable and check that polls
fail (Modbus exception or timeout) instead of returning the last values.

### Hardware

- Power **9-24 V DC**, barrel 5.5 mm centre-positive or terminal (5243A needs >= 500 mA).
- RS485 terminal: **485+ = A, 485- = B** (7104/7144: **T+ = A, T- = B**, R+/R- only for
  RS422). Twisted pair; 120 ohm termination only for runs over ~300 m.
- Default IP **192.168.1.200**, data port 4196.

### Security

- ZLVircom finds and reconfigures devices via **UDP 1092** (management port): firewall it.
- Wi-Fi models (7104/7144) **ship as an open hotspot "ZLAN" with no password**: set AES +
  strong password, or STA mode to the site Wi-Fi. Reset switch returns to open AP,
  IP 192.168.1.254.
- Log out of the web page after changes (the manual warns the session stays open).
