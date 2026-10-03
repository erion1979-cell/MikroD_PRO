# PowerGuard inverter — Modbus register map (verified)

Source: the manufacturer's *Inverter Modbus Protocol V1.1 (2022-11)*, corrected and
confirmed against a real unit on 2026-10-02 through an E30 converter in Modbus Gateway
mode, read with QModMaster.

## Communication

| Item | Value |
|---|---|
| Physical | RS485 (also RS232 on the unit; not used) |
| Serial | 9600 baud, 8 data bits, no parity, 1 stop bit |
| Slave ID | 01 (default; how to change it is undocumented) |
| Function | **04 Read Input Registers**, start address **0**, quantity **36** |
| Addressing | Table number = protocol address (register 000 = address 0). In QModMaster with Base Addr 1 this is "Start Address 1". |
| Byte order | Standard Modbus big-endian 16-bit registers; all values unsigned |

## Input registers (function 04)

| Reg | Content | Raw → value | Notes |
|---|---|---|---|
| 000 | Input AC voltage | ÷10 → V | |
| 001 | Input frequency | ÷10 → Hz | |
| 002 | Output voltage | ÷10 → **V** | Manual says "0.1 A": typo, verified as volts (2205 = 220.5 V) |
| 003 | Output frequency | ÷10 → Hz | |
| 004 | Output current | ÷10 → A | |
| 005 | reserved | | |
| 006 | Output load rate | % (0–300) | Manual V1.1 changed unit from 0.1 % to 1 % |
| 007 | Battery voltage | ÷10 → V | 12 V system on the test unit |
| 008 | reserved | | |
| 009 | Battery capacity | % (0–100) | Voltage-based; reads high while charging |
| 010–011 | reserved | | |
| 012 | DC bus current | ÷10 → A | 0 while charging on mains; probably discharge only |
| 013 | Internal temperature | ÷10 → °C | |
| 014 | Ambient temperature | ÷10 → °C | Always equal to 013 so far |
| 015–027 | reserved | | |
| 028 | reserved | | Reads **105**, constant, meaning unknown |
| 029 | reserved | | Reads **122**, constant, meaning unknown |
| 030 | reserved | | |
| 031 | Set status bits | | all bits reserved |
| 032 | Running status bits | see below | |
| 033 | Warning status bits | | **bits not documented** |
| 034 | Error status bits | | **bits not documented** |
| 035 | Event code | see below | |

### Register 032 — running status bits

| Bit | Meaning |
|---|---|
| 0 | Mains power normal |
| 1 | Charger running |
| 2 | Inverter running |
| 8 | Output on |
| others | reserved |

Observed: **259** (0x0103) = mains + charger + output on → on mains (inverter off,
line-interactive pass-through). **260** (0x0104) = inverter + output on → on battery.

Suggested mode logic (as in the prototype): event ≠ 0 and ≠ 9 → fault; else bit 0 →
on mains; else bit 2 → on battery; else output off.

### Register 035 — event codes

| Code | Event |
|---|---|
| 00 | No event |
| 01 | Internal overcurrent protection |
| 02 | Output short circuit protection |
| 03 | Output overload protection |
| 04 | System over temperature protection |
| 05 | High battery voltage protection |
| 06 | Low battery voltage protection |
| 07 | Phase sequence error warning |
| 08 | Output voltage low protection |
| 09 | ECO starts |

**Mains loss is not an event code.** After unplugging and re-plugging mains, 035 stayed 0.

## Verified readings (use as test vectors)

Registers 000–035, decimal, read 2026-10-02.

**On battery, no mains, no load:**
```
0 0 2205 505 0 0 0 118 0 47  0 0 0 250 250 0 0 0 0 0  0 0 0 0 0 0 0 0 105 122  0 0 260 0 0 0
```
→ input 0.0 V / 0.0 Hz, output 220.5 V / 50.5 Hz, 0.0 A, load 0 %, battery 11.8 V 47 %,
temps 25.0 / 25.0 °C, status 260 (inverter running + output on).

**On mains, charging, no load:**
```
2205 500 2200 500 0 0 0 138 0 100  0 0 0 250 250 0 0 0 0 0  0 0 0 0 0 0 0 0 105 122  0 0 259 0 0 0
```
→ input 220.5 V / 50.0 Hz, output 220.0 V / 50.0 Hz, battery 13.8 V 100 %, status 259.

**On mains after a short outage:**
```
2230 500 2200 500 0 0 0 138 0 100  0 0 0 250 250 0 0 0 0 0  0 0 0 0 0 0 0 0 105 122  0 0 259 0 0 0
```
→ event code 0: the outage left no trace.

## Other functions listed in the manual (undocumented)

01/05 control state bits, 06 control instruction, 02 status bits, 03/06 user settings
(holding registers 000–199, no map), 03/10 system date/time, self-test cycle, number of
historical records, **14H history record**, **2BH software version**. Do not write
anything without the manufacturer's map.
