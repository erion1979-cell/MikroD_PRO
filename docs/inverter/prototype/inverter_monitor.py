#!/usr/bin/env python3
"""
Inverter Monitor - PowerGuard inverter over Modbus TCP (E30 / ZLAN gateway)

Reads input registers 0-35 (function 04) from the inverter through an
Ethernet-to-RS485 converter in Modbus Gateway mode, and shows them on a
live web page.

Usage (Windows):  double-click start_monitor.bat
       or:        python inverter_monitor.py --host 192.168.4.1 --port 502

The browser opens automatically (http://localhost:8765 by default).

No extra packages needed: only the Python standard library.
"""

import argparse
import json
import os
import re
import socket
import struct
import threading
import time
import webbrowser
from collections import deque
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SETTINGS_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "inverter_settings.json")
DEFAULTS = {"host": "192.168.4.1", "port": 502, "unit": 1, "interval": 5.0}


def load_settings():
    try:
        with open(SETTINGS_FILE, encoding="utf-8") as f:
            saved = json.load(f)
        return {k: saved[k] for k in DEFAULTS if k in saved}
    except (OSError, ValueError):
        return {}


def save_settings(cfg):
    try:
        with open(SETTINGS_FILE, "w", encoding="utf-8") as f:
            json.dump({k: cfg[k] for k in DEFAULTS}, f, indent=2)
    except OSError as e:
        print("  Could not save settings: %s" % e)


def validate_settings(d):
    host = str(d.get("host", "")).strip()
    if not host or len(host) > 64 or not re.fullmatch(r"[A-Za-z0-9.\-]+", host):
        raise ValueError("Enter a valid IP address, e.g. 192.168.20.83")
    port, unit, interval = int(d.get("port")), int(d.get("unit")), float(d.get("interval"))
    if not 1 <= port <= 65535:
        raise ValueError("Port must be 1-65535")
    if not 1 <= unit <= 247:
        raise ValueError("Slave ID must be 1-247")
    if not 1 <= interval <= 3600:
        raise ValueError("Interval must be 1-3600 seconds")
    return {"host": host, "port": port, "unit": unit, "interval": interval}


EVENT_CODES = {
    0: "No event",
    1: "Internal overcurrent protection",
    2: "Output short circuit protection",
    3: "Output overload protection",
    4: "System over temperature protection",
    5: "High battery voltage protection",
    6: "Low battery voltage protection",
    7: "Phase sequence error warning",
    8: "Output voltage low protection",
    9: "ECO starts",
}

STATUS_BITS = {0: "Mains normal", 1: "Charger running", 2: "Inverter running", 8: "Output on"}


class ModbusError(Exception):
    pass


class ModbusTCP:
    """Minimal Modbus TCP client: function 04 (read input registers) only. Read-only by design."""

    def __init__(self, host, port, unit, timeout=2.0):
        self.host, self.port, self.unit, self.timeout = host, port, unit, timeout
        self.sock = None
        self.tid = 0

    def close(self):
        if self.sock:
            try:
                self.sock.close()
            except OSError:
                pass
        self.sock = None

    def _connect(self):
        self.close()
        self.sock = socket.create_connection((self.host, self.port), timeout=self.timeout)
        self.sock.settimeout(self.timeout)

    def _recv_exact(self, n):
        buf = b""
        while len(buf) < n:
            chunk = self.sock.recv(n - len(buf))
            if not chunk:
                raise ModbusError("connection closed by converter")
            buf += chunk
        return buf

    def read_input_registers(self, address, count):
        if self.sock is None:
            self._connect()
        self.tid = (self.tid + 1) & 0xFFFF
        pdu = struct.pack(">BHH", 0x04, address, count)
        mbap = struct.pack(">HHHB", self.tid, 0, len(pdu) + 1, self.unit)
        try:
            self.sock.sendall(mbap + pdu)
            while True:
                head = self._recv_exact(7)
                tid, proto, length, unit = struct.unpack(">HHHB", head)
                body = self._recv_exact(length - 1)
                if tid == self.tid:
                    break  # ignore stale replies from earlier timed-out requests
        except (OSError, ModbusError):
            self.close()
            raise
        fc = body[0]
        if fc == 0x84:
            raise ModbusError("inverter replied with Modbus exception code %d" % body[1])
        if fc != 0x04:
            raise ModbusError("unexpected function code %d" % fc)
        nbytes = body[1]
        if nbytes != count * 2 or len(body) < 2 + nbytes:
            raise ModbusError("wrong reply length")
        return list(struct.unpack(">%dH" % count, body[2:2 + nbytes]))


def decode(r):
    status = r[32]
    event = r[35]
    bits = {name: bool(status >> b & 1) for b, name in STATUS_BITS.items()}
    if event not in (0, 9):
        mode = "fault"
    elif bits["Mains normal"]:
        mode = "mains"
    elif bits["Inverter running"]:
        mode = "battery"
    else:
        mode = "off"
    out_v, out_a = r[2] / 10, r[4] / 10
    return {
        "input_v": r[0] / 10,
        "input_hz": r[1] / 10,
        "output_v": out_v,
        "output_hz": r[3] / 10,
        "output_a": out_a,
        "apparent_va": round(out_v * out_a),
        "load_pct": r[6],
        "battery_v": r[7] / 10,
        "battery_pct": r[9],
        "dc_bus_a": r[12] / 10,
        "temp_internal": r[13] / 10,
        "temp_ambient": r[14] / 10,
        "status_raw": status,
        "warning_raw": r[33],
        "error_raw": r[34],
        "bits": bits,
        "event_code": event,
        "event_text": EVENT_CODES.get(event, "Unknown event %d" % event),
        "mode": mode,
    }


class Monitor:
    def __init__(self, args):
        self.args = args
        self.lock = threading.Lock()
        self.gen = 0
        self.wake = threading.Event()
        self.client = ModbusTCP(args.host, args.port, args.unit, args.timeout)
        self.reset_state()

    def reset_state(self):
        self.latest = None          # decoded values
        self.raw = None             # 36 raw registers
        self.last_ok = None
        self.online = False
        self.fail_streak = 0
        self.polls = 0
        self.errors = 0
        self.last_error = ""
        self.history = deque(maxlen=int(3 * 3600 / max(self.args.interval, 1)))  # ~3 hours
        self.events = deque(maxlen=200)
        self.prev = None

    def apply_settings(self, cfg):
        with self.lock:
            self.client.close()
            for k, v in cfg.items():
                setattr(self.args, k, v)
            self.client = ModbusTCP(cfg["host"], cfg["port"], cfg["unit"], self.args.timeout)
            self.gen += 1
            self.reset_state()
            self.log("info", "Settings changed - now reading %s:%d, slave %d, every %gs"
                     % (cfg["host"], cfg["port"], cfg["unit"], cfg["interval"]))
        save_settings(cfg)
        print("  New settings: %s:%d slave %d every %gs" % (cfg["host"], cfg["port"], cfg["unit"], cfg["interval"]))
        self.wake.set()

    def log(self, kind, text):
        self.events.appendleft({"t": datetime.now().strftime("%Y-%m-%d %H:%M:%S"), "kind": kind, "text": text})

    def compare(self, d):
        p = self.prev
        if p is None:
            self.log("info", "Monitoring started - mode: " + d["mode"])
            return
        if p["bits"]["Mains normal"] and not d["bits"]["Mains normal"]:
            self.log("warn", "Mains lost - running on battery (%.1f V, %d %%)" % (d["battery_v"], d["battery_pct"]))
        if not p["bits"]["Mains normal"] and d["bits"]["Mains normal"]:
            self.log("ok", "Mains restored (%.1f V)" % d["input_v"])
        if p["event_code"] != d["event_code"]:
            if d["event_code"] == 0:
                self.log("ok", "Event cleared: " + p["event_text"])
            else:
                self.log("bad", "Event %02d: %s" % (d["event_code"], d["event_text"]))
        if p["bits"]["Output on"] and not d["bits"]["Output on"]:
            self.log("bad", "Output switched OFF")
        if not p["bits"]["Output on"] and d["bits"]["Output on"]:
            self.log("ok", "Output switched ON")
        if p["warning_raw"] != d["warning_raw"]:
            self.log("warn", "Warning bits changed: %d -> %d" % (p["warning_raw"], d["warning_raw"]))
        if p["error_raw"] != d["error_raw"]:
            self.log("bad", "Error bits changed: %d -> %d" % (p["error_raw"], d["error_raw"]))

    def poll_once(self):
        with self.lock:
            client, gen = self.client, self.gen
            self.polls += 1
        try:
            regs = client.read_input_registers(0, 36)
        except Exception as e:  # noqa: BLE001
            with self.lock:
                if gen != self.gen:
                    return
                self.errors += 1
                self.fail_streak += 1
                self.last_error = str(e) or e.__class__.__name__
                if self.online and self.fail_streak >= 3:
                    self.online = False
                    self.log("bad", "Inverter not responding (%s)" % self.last_error)
            return
        d = decode(regs)
        with self.lock:
            if gen != self.gen:
                return
            if not self.online and self.prev is not None:
                self.log("ok", "Inverter responding again")
            self.online = True
            self.fail_streak = 0
            self.raw = regs
            self.compare(d)
            self.prev = d
            self.latest = d
            self.last_ok = time.time()
            self.history.append({
                "t": int(time.time()),
                "in_v": d["input_v"], "out_v": d["output_v"],
                "bat_v": d["battery_v"], "bat_pct": d["battery_pct"],
                "load": d["load_pct"], "out_a": d["output_a"],
            })

    def run(self):
        while True:
            start = time.time()
            self.poll_once()
            self.wake.wait(max(0.2, self.args.interval - (time.time() - start)))
            self.wake.clear()

    def snapshot(self):
        with self.lock:
            return {
                "host": self.args.host, "port": self.args.port, "unit": self.args.unit,
                "interval": self.args.interval,
                "online": self.online,
                "age": None if self.last_ok is None else round(time.time() - self.last_ok, 1),
                "polls": self.polls, "errors": self.errors, "last_error": self.last_error,
                "data": self.latest, "raw": self.raw,
                "history": list(self.history), "events": list(self.events),
            }


PAGE = r"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Inverter Monitor</title>
<style>
:root{--bg:#0f1317;--card:#161b21;--line:#242c35;--text:#e6eaee;--muted:#93a0ad;--dim:#6f7c89;
--teal:#5fd4ca;--blue:#7aa2f7;--green:#56d37a;--amber:#e0a93b;--red:#ff6b61;--grey:#5b6672}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:14px/1.4 "Segoe UI",system-ui,sans-serif}
.mono{font-family:Consolas,"Cascadia Mono",ui-monospace,monospace}
main{max-width:1280px;margin:0 auto;padding:20px 16px 40px;display:flex;flex-direction:column;gap:16px}
header{display:flex;justify-content:space-between;align-items:flex-end;gap:16px;flex-wrap:wrap}
h1{margin:0;font-size:24px;font-weight:600}
h2{margin:0;font-size:15px;font-weight:600}
.sub{color:var(--muted);font-size:13px}
.pill{display:inline-flex;align-items:center;gap:7px;padding:4px 11px;border-radius:999px;font-size:13px;font-weight:500}
.dot{width:8px;height:8px;border-radius:50%;display:inline-block}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:16px 18px;display:flex;flex-direction:column;gap:12px;min-width:0}
.row{display:flex;justify-content:space-between;align-items:baseline;gap:8px}
.reg{font-size:11px;color:var(--dim)}
.banner{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:16px 20px;border-radius:12px;border:1px solid;flex-wrap:wrap}
.banner .t{font-size:19px;font-weight:600}
.chips{display:flex;gap:8px;flex-wrap:wrap}
.chip{display:inline-flex;align-items:center;gap:7px;padding:6px 12px;border-radius:8px;background:#151a20;border:1px solid #2a333d;font-size:13px}
.grid{display:grid;gap:16px}
.g4{grid-template-columns:repeat(4,minmax(0,1fr))}
.g3{grid-template-columns:2fr 1fr}
.big{font-size:32px;font-weight:600}
.unit{font-size:15px;color:var(--muted)}
.bar{height:6px;border-radius:3px;background:var(--line);overflow:hidden}
.bar>div{height:100%;transition:width .4s}
.kv{display:flex;justify-content:space-between;font-size:13px;color:var(--muted)}
.kv b{color:var(--text);font-weight:500}
svg{display:block;width:100%}
.legend{display:flex;gap:16px;font-size:12px;color:#b7c0ca}
.legend i{display:inline-block;width:14px;height:3px;margin-right:6px;vertical-align:middle}
.ev{display:flex;gap:10px;padding:8px 0;border-top:1px solid #222a32}
.ev .dot{margin-top:6px;flex-shrink:0}
.ev small{color:var(--muted)}
.events{max-height:430px;overflow:auto}
table{border-collapse:collapse;width:100%;font-size:12px}
td{border:1px solid var(--line);padding:4px 6px;text-align:center}
td.h{color:var(--dim)}
footer{display:flex;gap:24px;flex-wrap:wrap;font-size:13px;color:var(--muted);padding:12px 18px;border:1px solid #222a32;border-radius:12px;background:#131820}
footer b{color:var(--text);font-weight:500}
.note{font-size:12px;color:var(--dim)}
.fl{display:flex;flex-direction:column;gap:4px;font-size:12px;color:var(--muted)}
input{background:#10151a;border:1px solid #2c3640;border-radius:8px;color:var(--text);font:inherit;font-size:14px;padding:0 10px;min-height:40px}
button{min-height:40px;padding:0 16px;border-radius:8px;border:1px solid #2f5a57;background:#173a37;color:var(--teal);font:inherit;font-weight:600;cursor:pointer}
@media (max-width:900px){.g4{grid-template-columns:repeat(2,minmax(0,1fr))}.g3{grid-template-columns:1fr}}
@media (max-width:520px){.g4{grid-template-columns:1fr}}
</style>
</head>
<body>
<main>
<header>
  <div>
    <div class="sub" id="where">Inverter</div>
    <div style="display:flex;align-items:center;gap:12px;margin-top:4px">
      <h1>Inverter Monitor</h1>
      <span class="pill" id="online"><span class="dot"></span><span>Connecting...</span></span>
    </div>
  </div>
  <div class="sub">Last reading <b class="mono" id="age">-</b> &nbsp;·&nbsp; polling every <b class="mono" id="interval">-</b></div>
</header>

<details class="card" id="settings" style="gap:0">
  <summary style="cursor:pointer;font-weight:600">Connection settings <span class="note" id="cur"></span></summary>
  <form id="sform" style="display:flex;gap:12px;flex-wrap:wrap;align-items:flex-end;margin-top:12px">
    <label class="fl">Converter IP<input id="s_host" required maxlength="64"></label>
    <label class="fl">Port<input id="s_port" type="number" min="1" max="65535" required style="width:90px"></label>
    <label class="fl">Slave ID<input id="s_unit" type="number" min="1" max="247" required style="width:80px"></label>
    <label class="fl">Poll every (s)<input id="s_int" type="number" min="1" max="3600" step="1" required style="width:90px"></label>
    <button type="submit">Save &amp; connect</button>
    <span id="smsg" class="note"></span>
  </form>
</details>

<section class="banner" id="banner">
  <div><div class="t" id="btitle">Waiting for first reading...</div><div class="sub" id="bsub"></div></div>
  <div class="chips" id="chips"></div>
</section>

<div class="grid g4">
  <section class="card"><div class="row"><h2>Input (mains)</h2><span class="reg mono">reg 000-001</span></div>
    <div><span class="big mono" id="in_v">-</span> <span class="unit">V</span></div>
    <div class="bar"><div id="in_bar"></div></div>
    <div class="kv"><span>Frequency <b class="mono" id="in_hz">-</b></span><span>State <b id="in_state">-</b></span></div></section>
  <section class="card"><div class="row"><h2>Output</h2><span class="reg mono">reg 002-004</span></div>
    <div><span class="big mono" id="out_v">-</span> <span class="unit">V</span></div>
    <div class="bar"><div id="out_bar"></div></div>
    <div class="kv"><span>Frequency <b class="mono" id="out_hz">-</b></span><span>Current <b class="mono" id="out_a">-</b></span></div></section>
  <section class="card"><div class="row"><h2>Load</h2><span class="reg mono">reg 006</span></div>
    <div><span class="big mono" id="load">-</span> <span class="unit">% of rated</span></div>
    <div class="bar"><div id="load_bar"></div></div>
    <div class="kv"><span>Apparent <b class="mono" id="va">-</b></span><span>Event <b class="mono" id="evc">-</b></span></div></section>
  <section class="card"><div class="row"><h2>Battery</h2><span class="reg mono">reg 007, 009, 012</span></div>
    <div><span class="big mono" id="bat_pct">-</span> <span class="unit">%</span> &nbsp;<span class="mono" id="bat_v" style="font-size:18px">-</span></div>
    <div class="bar"><div id="bat_bar"></div></div>
    <div class="kv"><span>DC bus current <b class="mono" id="dc">-</b></span><span>Temp <b class="mono" id="temp">-</b></span></div>
    <div class="note" id="bat_note"></div></section>
</div>

<div class="grid g3">
  <section class="card">
    <div class="row"><h2>History (since this page's program started)</h2><span class="note" id="hspan"></span></div>
    <div class="legend"><span><i style="background:var(--teal)"></i>Input V</span><span><i style="background:var(--blue)"></i>Output V</span></div>
    <svg id="chartV" viewBox="0 0 800 150" preserveAspectRatio="none" height="150"></svg>
    <div class="legend"><span><i style="background:var(--green)"></i>Battery %</span><span><i style="background:var(--amber)"></i>Load %</span></div>
    <svg id="chartP" viewBox="0 0 800 150" preserveAspectRatio="none" height="150"></svg>
  </section>
  <section class="card">
    <div class="row"><h2>Events</h2><span class="reg mono">reg 032-035</span></div>
    <div class="events" id="events"><div class="note">No events yet.</div></div>
  </section>
</div>

<section class="card">
  <div class="row"><h2>Raw registers (input registers 000-035)</h2><span class="note">for checking values against the Modbus manual</span></div>
  <div style="overflow:auto"><table id="raw"></table></div>
</section>

<footer>
  <span>Converter <b class="mono" id="f_host">-</b></span>
  <span>Slave ID <b class="mono" id="f_unit">-</b></span>
  <span>Polls <b class="mono" id="f_polls">-</b></span>
  <span>Errors <b class="mono" id="f_err">-</b></span>
  <span>Success <b class="mono" id="f_ok">-</b></span>
  <span id="f_last" style="color:var(--amber)"></span>
</footer>
</main>

<script>
const C = {teal:'#5fd4ca',blue:'#7aa2f7',green:'#56d37a',amber:'#e0a93b',red:'#ff6b61',grey:'#5b6672',text:'#e6eaee',muted:'#93a0ad'};
const $ = id => document.getElementById(id);
const f1 = v => (v==null?'-':Number(v).toFixed(1));

function setBar(id, pct, color){ const el=$(id); el.style.width=Math.max(0,Math.min(100,pct))+'%'; el.style.background=color; }

function chart(svgId, series, maxY, unit){
  const svg=$(svgId), W=800, H=150, L=34, T=8, B=130;
  let s='';
  [0,0.5,1].forEach(f=>{const y=B-(B-T)*f; s+=`<line x1="${L}" y1="${y}" x2="${W}" y2="${y}" stroke="#232b34"/>`+
    `<text x="${L-4}" y="${y+4}" text-anchor="end" fill="#6f7c89" font-size="10" font-family="Consolas,monospace">${Math.round(maxY*f)}${f===0?unit:''}</text>`;});
  series.forEach(se=>{
    const pts=se.data; if(pts.length<2) return;
    const t0=pts[0].t, t1=pts[pts.length-1].t||t0+1, span=Math.max(1,t1-t0);
    const p=pts.map(d=>{const x=L+(W-L)*(d.t-t0)/span; const v=Math.max(0,Math.min(maxY,d.v)); return x.toFixed(1)+','+(B-(B-T)*v/maxY).toFixed(1);}).join(' ');
    s+=`<polyline points="${p}" fill="none" stroke="${se.color}" stroke-width="2" vector-effect="non-scaling-stroke" stroke-linejoin="round"/>`;
  });
  svg.innerHTML=s;
}

function render(j){
  $('where').textContent = `PowerGuard inverter · ${j.host}:${j.port}`;
  fillForm(j);
  $('interval').textContent = j.interval+' s';
  $('age').textContent = j.age==null ? 'never' : j.age+' s ago';
  const on=$('online');
  on.style.background = j.online?'#15301f':'#3a1a1c'; on.style.color = j.online?C.green:C.red;
  on.querySelector('.dot').style.background = j.online?C.green:C.red;
  on.lastElementChild.textContent = j.online?'Online':(j.polls?'Not responding':'Connecting...');

  $('f_host').textContent=j.host+':'+j.port; $('f_unit').textContent=j.unit;
  $('f_polls').textContent=j.polls; $('f_err').textContent=j.errors;
  $('f_ok').textContent = j.polls? (100*(j.polls-j.errors)/j.polls).toFixed(1)+'%':'-';
  $('f_last').textContent = (!j.online && j.last_error) ? 'Last error: '+j.last_error : '';

  const d=j.data;
  if(!d){ $('btitle').textContent=j.polls?'No reply from '+j.host+':'+j.port:'Connecting to '+j.host+'...'; $('btitle').style.color=C.muted; $('bsub').textContent=j.last_error?('Last error: '+j.last_error):''; $('banner').style.background='#161b21'; $('banner').style.borderColor='#242c35'; $('chips').innerHTML=''; }
  if(d){
    const ban=$('banner'); let title, sub, col, bg, br;
    if(!j.online){title='Not responding — showing last reading'; sub='Check power, cable and network to the converter'; col=C.red; bg='#2c1618'; br='#6b2a2c';}
    else if(d.mode==='fault'){title=`Event ${String(d.event_code).padStart(2,'0')} — ${d.event_text}`; sub=`Load ${d.load_pct} % · output ${f1(d.output_v)} V`; col=C.red; bg='#2c1618'; br='#6b2a2c';}
    else if(d.mode==='battery'){title='On battery — mains lost'; sub=`Battery ${f1(d.battery_v)} V · ${d.battery_pct} % · load ${d.load_pct} %`; col=C.amber; bg='#2a2213'; br='#5a4418';}
    else if(d.mode==='mains'){title='Normal — running on mains'; sub=d.event_code===9?'ECO mode active':'No active events'; col=C.green; bg='#132419'; br='#24502f';}
    else {title='Output off'; sub='Inverter is not supplying the output'; col=C.grey; bg='#1a1f25'; br='#2c3640';}
    $('btitle').textContent=title; $('btitle').style.color=col; $('bsub').textContent=sub; ban.style.background=bg; ban.style.borderColor=br;
    $('chips').innerHTML = Object.entries(d.bits).map(([k,v])=>{
      const c = v?C.green:(k==='Mains normal'?C.amber:C.grey);
      return `<span class="chip" style="color:${v?C.text:C.muted}"><span class="dot" style="background:${c}"></span>${k}</span>`;}).join('');

    $('in_v').textContent=f1(d.input_v); $('in_hz').textContent=f1(d.input_hz)+' Hz';
    $('in_state').textContent=d.bits['Mains normal']?'OK':'lost'; setBar('in_bar', d.input_v/2.5, d.bits['Mains normal']?C.teal:C.amber);
    $('out_v').textContent=f1(d.output_v); $('out_hz').textContent=f1(d.output_hz)+' Hz'; $('out_a').textContent=f1(d.output_a)+' A';
    setBar('out_bar', d.output_v/2.5, C.teal);
    $('load').textContent=d.load_pct; $('va').textContent=d.apparent_va+' VA'; $('evc').textContent=String(d.event_code).padStart(2,'0');
    setBar('load_bar', d.load_pct, d.load_pct>100?C.red:d.load_pct>80?C.amber:C.teal);
    $('bat_pct').textContent=d.battery_pct; $('bat_v').textContent=f1(d.battery_v)+' V';
    setBar('bat_bar', d.battery_pct, d.battery_pct<30?C.red:d.battery_pct<60?C.amber:C.green);
    $('dc').textContent=f1(d.dc_bus_a)+' A'; $('temp').textContent=f1(d.temp_internal)+' °C';
    $('bat_note').textContent = d.bits['Charger running'] ? 'Charging: battery % reads high while the charger is on; the voltage is more reliable.' : '';
  }

  const h=j.history;
  if(h.length){
    chart('chartV',[{color:C.teal,data:h.map(x=>({t:x.t,v:x.in_v}))},{color:C.blue,data:h.map(x=>({t:x.t,v:x.out_v}))}],250,' V');
    chart('chartP',[{color:C.green,data:h.map(x=>({t:x.t,v:x.bat_pct}))},{color:C.amber,data:h.map(x=>({t:x.t,v:x.load}))}],100,' %');
    const mins=Math.round((h[h.length-1].t-h[0].t)/60); $('hspan').textContent = `last ${mins} min · ${h.length} readings`;
  }

  const kc={ok:C.green,warn:C.amber,bad:C.red,info:C.teal};
  if(!j.events.length) $('events').innerHTML='<div class="note">No events yet.</div>';
  else $('events').innerHTML=j.events.map(e=>`<div class="ev"><span class="dot" style="background:${kc[e.kind]}"></span><div><div>${e.text}</div><small class="mono">${e.t}</small></div></div>`).join('');

  if(j.raw){
    let s='<tr>'+Array.from({length:10},(_,i)=>`<td class="h">+${i}</td>`).join('')+'</tr>';
    for(let r=0;r<4;r++){ s+='<tr>'; for(let c=0;c<10;c++){const i=r*10+c; s+= i<36?`<td class="mono" title="register ${String(i).padStart(3,'0')}"><span style="color:#6f7c89">${String(i).padStart(3,'0')}</span><br>${j.raw[i]}</td>`:'<td></td>';} s+='</tr>'; }
    $('raw').innerHTML=s;
  }
}

let formLoaded=false;
function fillForm(j){
  $('cur').textContent=` — ${j.host}:${j.port} · slave ${j.unit} · every ${j.interval}s`;
  if(formLoaded) return;
  $('s_host').value=j.host; $('s_port').value=j.port; $('s_unit').value=j.unit; $('s_int').value=j.interval; formLoaded=true;
}
$('sform').addEventListener('submit', async ev=>{
  ev.preventDefault(); $('smsg').textContent='Saving...'; $('smsg').style.color='';
  const body={host:$('s_host').value.trim(),port:+$('s_port').value,unit:+$('s_unit').value,interval:+$('s_int').value};
  try{
    const r=await fetch('/api/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    const j=await r.json();
    if(j.ok){ $('smsg').textContent='Saved. Connecting to '+body.host+'...'; $('smsg').style.color=C.green; tick(); }
    else { $('smsg').textContent=j.error; $('smsg').style.color=C.red; }
  }catch(e){ $('smsg').textContent='Program not running'; $('smsg').style.color=C.red; }
});

async function tick(){
  try{ const r=await fetch('/api/data',{cache:'no-store'}); render(await r.json()); }
  catch(e){ $('online').lastElementChild.textContent='Program not running'; $('online').style.color=C.red; }
}
tick(); setInterval(tick, 2000);
</script>
</body>
</html>
"""


def make_handler(monitor):
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path.startswith("/api/data"):
                body = json.dumps(monitor.snapshot()).encode()
                ctype = "application/json"
            elif self.path in ("/", "/index.html"):
                body = PAGE.encode()
                ctype = "text/html; charset=utf-8"
            else:
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header("Content-Type", ctype)
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self):
            if not self.path.startswith("/api/settings"):
                self.send_error(404)
                return
            if not self.headers.get("Content-Type", "").startswith("application/json"):
                self.send_error(415)
                return
            try:
                n = min(int(self.headers.get("Content-Length", 0)), 4096)
                cfg = validate_settings(json.loads(self.rfile.read(n) or b"{}"))
                monitor.apply_settings(cfg)
                body, code = {"ok": True}, 200
            except (ValueError, TypeError) as e:
                body, code = {"ok": False, "error": str(e)}, 400
            data = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, *a):
            pass

    return Handler


def main():
    ap = argparse.ArgumentParser(description="PowerGuard inverter monitor (Modbus TCP, read-only)")
    ap.add_argument("--host", help="converter IP (default: last saved, else 192.168.4.1)")
    ap.add_argument("--port", type=int, help="converter Modbus TCP port (default 502)")
    ap.add_argument("--unit", type=int, help="inverter slave ID (default 1)")
    ap.add_argument("--interval", type=float, help="seconds between polls (default 5)")
    ap.add_argument("--timeout", type=float, default=2, help="reply timeout in seconds (default 2)")
    ap.add_argument("--web", type=int, default=8765, help="web page port (default 8765; the next free one is used if taken)")
    ap.add_argument("--no-browser", action="store_true", help="do not open the browser automatically")
    ap.add_argument("--lan", action="store_true", help="allow other computers on the network to open the page")
    args = ap.parse_args()
    cfg = dict(DEFAULTS)
    cfg.update(load_settings())
    for k in DEFAULTS:
        if getattr(args, k) is not None:
            cfg[k] = getattr(args, k)
    try:
        cfg = validate_settings(cfg)
    except (ValueError, TypeError) as e:
        print("Bad settings (%s), using defaults." % e)
        cfg = dict(DEFAULTS)
    for k, v in cfg.items():
        setattr(args, k, v)

    monitor = Monitor(args)
    threading.Thread(target=monitor.run, daemon=True).start()

    bind = "0.0.0.0" if args.lan else "127.0.0.1"
    server = None
    for web_port in range(args.web, args.web + 20):
        try:
            server = ThreadingHTTPServer((bind, web_port), make_handler(monitor))
            break
        except OSError:
            print("  Port %d is already used by another program, trying %d..." % (web_port, web_port + 1))
    if server is None:
        print("ERROR: no free web port found between %d and %d." % (args.web, args.web + 19))
        return 1
    url = "http://localhost:%d" % web_port
    print("Inverter Monitor")
    print("  Reading inverter at %s:%d (slave %d) every %gs" % (args.host, args.port, args.unit, args.interval))
    print("  Open this in your browser:  " + url)
    print("  Change the converter IP on the web page (Connection settings).")
    print("  Press Ctrl+C or close this window to stop.")
    if not args.no_browser:
        threading.Timer(1.0, webbrowser.open, args=(url,)).start()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    raise SystemExit(main())
