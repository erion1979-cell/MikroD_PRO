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

## ZLAN7144N2 (Shanghai Zhuolan, RS232/485/422 + Ethernet + Wi-Fi)

Not yet tested with the inverter. From the manual:

| Setting (Chinese label) | Value |
|---|---|
| 工作模式 Work mode | TCP Server |
| 端口 Port | 502 (default 4196) |
| 波特率 / 数据位 / 校验位 / 停止位 | 9600 / 8 / None / 1 |
| 流控 Flow control | None |
| **转化协议 Conversion protocol** | **Modbus TCP<->RTU** (also enables RS485 multi-master: several clients may poll) |
| RS485 指令应答超时时间 Response timeout | 256 ms (raise to 500 if timeouts) |
| 启用 P2P 功能 Enable P2P | OFF (vendor cloud) |
| Web 登录密码 Web password | set one (default: empty) |

Hardware: power **9–24 V DC** (barrel 5.5 mm centre-positive or terminal); RS485 on the
terminal **T+ = A, T− = B** (R+/R− only for RS422); default IP 192.168.1.200; configured
via web page or the ZLVircom Windows tool (more options under 更多高级选项).
Security: **ships as an open Wi-Fi hotspot "ZLAN" with no password**: set AES + strong
password; ZLVircom can find and reconfigure it across subnets.
