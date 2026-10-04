// Package power is the Power/UPS module: what a unit is doing, what changed,
// and (in later files) the pollers that ask it.
//
// ── THIS FILE IS PURE ───────────────────────────────────────────────────────
//
// The caller supplies every reading, every failure and the time. Nothing here
// touches a socket, a clock or a database, which is what lets the rules be
// tested against sequences of readings rather than against a unit.
//
// ── ONE RULE FOR EVERY KIND OF EVENT ────────────────────────────────────────
//
// A reading yields the set of CONDITIONS that are true right now: mains lost,
// output off, an event code, battery low, warning or error bits set. A change is
// simply a condition that appears (it began) or disappears (it ended) between
// one reading and the next. That is the whole event model, and it is why an
// event code going straight from 03 to 06 needs no special case: 03 ends and 06
// begins.
//
// ── MAINS LOSS IS ONLY EVER SEEN LIVE ───────────────────────────────────────
//
// The unit keeps no record of an outage: it is a status bit that is low while it
// lasts. So this is the only place an outage is timestamped, to the poll that
// first saw it.
package power

import (
	"fmt"
	"sort"

	"mikrodash/internal/power/model"
)

// Kind is a kind of condition.
type Kind string

const (
	KindMainsLost     Kind = "mains_lost"
	KindOutputOff     Kind = "output_off"
	KindEvent         Kind = "event"
	KindBatteryLow    Kind = "battery_low"
	KindWarningBits   Kind = "warning_bits"
	KindErrorBits     Kind = "error_bits"
	KindNotResponding Kind = "not_responding"
)

// Defaults for the Tracker's settings.
const (
	DefaultOfflineAfter  = 3
	DefaultBatteryLowPct = 20
	// batteryLowClear is how far above the threshold the battery must climb
	// before "battery low" ends while still on battery. The percentage is
	// voltage-based and wobbles with load, so without it a battery sitting at
	// the threshold would begin and end the condition every few polls.
	batteryLowClear = 5
)

// Cond is one condition that is true.
type Cond struct {
	Kind Kind
	// Code is the event code, or the register value for warning/error bits.
	// Zero for the kinds that have no code.
	Code int
	// Text names the condition for a person.
	Text string
	// Fault marks an event code the model counts as a fault (not ECO).
	Fault bool
}

type condKey struct {
	kind Kind
	code int
}

func (c Cond) key() condKey { return condKey{c.Kind, c.Code} }

// Change is a condition beginning or ending.
type Change struct {
	Cond
	// Began is true when the condition started, false when it ended.
	Began bool
	// At is when it began or ended, in Unix milliseconds: the poll that saw it,
	// or for "not responding" the first poll that failed.
	At int64
	// Since is when an ending condition began, so its duration is At - Since.
	// Zero on a beginning.
	Since int64
	// Initial marks a condition already true at the first reading: it began
	// some time before monitoring could see it, and At is only when it was
	// first seen.
	Initial bool
}

// Tracker turns one unit's polls into changes.
type Tracker struct {
	// OfflineAfter is how many polls in a row must fail before the unit is
	// "not responding". One failure is ordinary during a mains/battery
	// switch-over, and a VPN glitch can cost two.
	OfflineAfter int
	// BatteryLowPct: on battery at or below this, the battery is low.
	BatteryLowPct float64

	seen      bool // a reading has been taken
	open      map[condKey]openCond
	fails     int
	firstFail int64
}

type openCond struct {
	cond  Cond
	since int64
}

// NewTracker returns a tracker with the default settings.
func NewTracker() *Tracker {
	return &Tracker{OfflineAfter: DefaultOfflineAfter, BatteryLowPct: DefaultBatteryLowPct}
}

// Online reports whether the unit is answering: false only once OfflineAfter
// polls in a row have failed.
func (t *Tracker) Online() bool {
	_, down := t.open[condKey{kind: KindNotResponding}]
	return !down
}

// Open lists the conditions true now, each with when it began.
func (t *Tracker) Open() []Change {
	out := make([]Change, 0, len(t.open))
	for _, o := range t.open {
		out = append(out, Change{Cond: o.cond, Began: true, At: o.since})
	}
	sortChanges(out)
	return out
}

// Success records a reading taken at now.
func (t *Tracker) Success(r model.Reading, now int64) []Change {
	if t.open == nil {
		t.open = map[condKey]openCond{}
	}
	initial := !t.seen
	t.seen = true
	t.fails = 0

	cur := t.conditions(r)
	// The unit answering again ends "not responding" like any other condition;
	// it is not in `cur` because a reading never produces it.
	var out []Change
	for k, o := range t.open {
		if _, still := cur[k]; still {
			continue
		}
		delete(t.open, k)
		out = append(out, Change{Cond: o.cond, At: now, Since: o.since})
	}
	for k, c := range cur {
		if _, already := t.open[k]; already {
			continue
		}
		t.open[k] = openCond{cond: c, since: now}
		out = append(out, Change{Cond: c, Began: true, At: now, Initial: initial})
	}
	sortChanges(out)
	return out
}

// Failure records a poll at now that got no usable reply.
//
// Conditions other than "not responding" are left as they were: nobody can see
// whether mains came back while the unit was unreachable, so the next reading
// decides, compared with the last one before the silence.
func (t *Tracker) Failure(now int64) []Change {
	if t.open == nil {
		t.open = map[condKey]openCond{}
	}
	t.fails++
	if t.fails == 1 {
		t.firstFail = now
	}
	k := condKey{kind: KindNotResponding}
	if t.fails < t.OfflineAfter {
		return nil
	}
	if _, already := t.open[k]; already {
		return nil
	}
	c := Cond{Kind: KindNotResponding, Text: "Not responding"}
	t.open[k] = openCond{cond: c, since: t.firstFail}
	return []Change{{Cond: c, Began: true, At: t.firstFail, Initial: !t.seen}}
}

// conditions is everything true in one reading.
func (t *Tracker) conditions(r model.Reading) map[condKey]Cond {
	out := map[condKey]Cond{}
	add := func(c Cond) { out[c.key()] = c }

	if ok, has := r.Flags["mains_ok"]; has && !ok {
		add(Cond{Kind: KindMainsLost, Text: "Mains lost"})
	}
	if on, has := r.Flags["output_on"]; has && !on {
		add(Cond{Kind: KindOutputOff, Text: "Output off"})
	}
	if r.EventCode != 0 {
		add(Cond{Kind: KindEvent, Code: r.EventCode, Text: r.EventText, Fault: r.Mode == model.ModeFault})
	}
	if v := r.Raw["warning_bits"]; v != 0 {
		add(Cond{Kind: KindWarningBits, Code: int(v), Text: fmt.Sprintf("Warning bits %d", v)})
	}
	if v := r.Raw["error_bits"]; v != 0 {
		add(Cond{Kind: KindErrorBits, Code: int(v), Text: fmt.Sprintf("Error bits %d", v)})
	}
	// Battery % is voltage-based and reads high whenever the charger runs, so
	// it is trusted only on battery.
	if pct, has := r.Values["battery_pct"]; has && r.Mode == model.ModeBattery {
		_, wasLow := t.open[condKey{kind: KindBatteryLow}]
		if pct <= t.BatteryLowPct || (wasLow && pct < t.BatteryLowPct+batteryLowClear) {
			add(Cond{Kind: KindBatteryLow, Text: "Battery low"})
		}
	}
	return out
}

// sortChanges puts endings before beginnings, then orders by kind and code,
// so one poll's changes always come out the same way.
func sortChanges(cs []Change) {
	sort.Slice(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Began != b.Began {
			return !a.Began
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Code < b.Code
	})
}
