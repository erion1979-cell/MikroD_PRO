package server

// Power/UPS notifications: each condition that begins or ends, sent through the
// same channels, templates, cooldowns and `-alert-dispatch` switch as a router
// alert.
//
// ── NOT AN ALERT ROW ────────────────────────────────────────────────────────
//
// Agreed for this module (docs/inverter/PLAN.md): its events are kept and shown
// on the Power/UPS page, not in the router alert bell, so nothing here writes
// `alert_events`. It builds an `alert.Fired` only to go through
// `alertdispatch.Build`, so a power cut reads exactly like any other alert.
//
// ── WHO HEARS ───────────────────────────────────────────────────────────────
//
// A channel subscribed to the event. A user's own channel only when they may
// read the unit's SITE, asked at send time like a router alert's permission. A
// channel narrowed to routers only when the unit is linked to one of them.
//
// ── WHAT THE UNIT READ, IN THE MESSAGE ──────────────────────────────────────
//
// A message carries the reading that showed the change: "Mains lost on INV-01
// (Site A): running on battery · input 0 V · output 229.6 V · load 41 % ·
// battery 85 % (12.6 V)". The line is appended to {{detail}}, so templates
// written for routers show it unchanged, and each value is also a variable of
// its own (powerVars) for a template that wants its own layout. A unit that
// has stopped answering has no reading, so its message has none.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/alertdispatch"
	"mikrodash/internal/notify"
	"mikrodash/internal/power"
	"mikrodash/internal/power/model"
)

// powerFired is what one change says: the catalogue's name for its direction,
// the condition as the subject, and a detail line. ok is false for a kind that
// is not sent (see power.NoticeKey).
func powerFired(c power.Change, r *model.Reading, estimated bool) (f alert.Fired, key string, ok bool) {
	key = power.NoticeKey(c.Kind)
	ty, known := alert.TypeByKey(key) // never known for "", a kind not sent
	if !known {
		return alert.Fired{}, "", false
	}
	f = alert.Fired{Up: !c.Began, AlertType: ty.Down, Subject: c.Text}
	if !c.Began {
		f.AlertType = ty.Up
	}
	lasted := humanDuration(time.Duration(c.At-c.Since) * time.Millisecond)
	switch {
	case c.Began && c.Kind == power.KindEvent:
		f.Detail = fmt.Sprintf("%s (code %02d)", c.Text, c.Code)
	case c.Began && c.Kind == power.KindMainsLost:
		f.Detail = "running on battery"
	case c.Began && c.Kind == power.KindNotResponding:
		f.Detail = "no reply from the unit"
	case c.Began:
		f.Detail = c.Text
	case c.Kind == power.KindEvent:
		f.Detail = fmt.Sprintf("%s (code %02d) cleared after %s", c.Text, c.Code, lasted)
	case c.Kind == power.KindMainsLost:
		f.Detail = "back on mains after " + lasted
	default:
		f.Detail = "after " + lasted
	}
	if c.Initial {
		f.Detail += " (already so when monitoring began)"
	}
	if r != nil {
		f.Vars = powerVars(*r, estimated)
		if line := f.Vars["readings"]; line != "" {
			f.Detail += " · " + line
		}
	}
	return f, key, true
}

// powerVars are a reading's values as template variables: numbers without
// their units, each absent when the unit does not report it, and `readings`,
// all of them on one line. `estimated` marks a battery % worked out by
// MikroDash rather than reported (a Megatec UPS).
func powerVars(r model.Reading, estimated bool) map[string]string {
	out := map[string]string{}
	var line []string
	num := func(key, v string, unit string, digits int, label string) {
		x, has := r.Values[v]
		if !has {
			return
		}
		s := strconv.FormatFloat(x, 'f', digits, 64)
		out[key] = s
		line = append(line, label+" "+s+" "+unit)
	}
	num("inputV", "input_v", "V", 1, "input")
	num("outputV", "output_v", "V", 1, "output")
	num("load", "load_pct", "%", 0, "load")
	if pct, has := r.Values["battery_pct"]; has {
		s := strconv.FormatFloat(pct, 'f', 0, 64)
		out["batteryPct"] = s
		if estimated {
			s = "~" + s
		}
		b := "battery " + s + " %"
		if v, has := r.Values["battery_v"]; has {
			b += " (" + strconv.FormatFloat(v, 'f', 1, 64) + " V)"
		}
		line = append(line, b)
	}
	if v, has := r.Values["battery_v"]; has {
		out["batteryV"] = strconv.FormatFloat(v, 'f', 1, 64)
		if _, pct := r.Values["battery_pct"]; !pct {
			line = append(line, "battery "+out["batteryV"]+" V")
		}
	}
	out["readings"] = strings.Join(line, " · ")
	return out
}

// humanDuration is "48 s", "37 min", "1 h 29 min".
func humanDuration(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		return fmt.Sprintf("%d min", s/60)
	default:
		return fmt.Sprintf("%d h %d min", s/3600, s/60%60)
	}
}

// dispatchPower sends one unit's changes. Non-blocking, as dispatchFired is:
// it runs on the unit's poll, and delivery talks to the network.
func (s *Server) dispatchPower(unitID string, cs []power.Change, r *model.Reading) {
	if s.dispatch == nil || !s.dispatch.Enabled() {
		return
	}
	s.power.mu.Lock()
	u, ok := s.power.units[unitID]
	s.power.mu.Unlock()
	if !ok {
		return
	}
	// The unit as a message names it: "INV-01 (Site A - Server room)".
	name, siteName := u.Name, ""
	if site := siteOf(u); site != "" {
		if sites, err := s.auditDB.ListSites(); err == nil {
			for _, st := range sites {
				if st.ID == site {
					siteName = st.Name
					name += " (" + st.Name + ")"
				}
			}
		}
	}
	mdl := model.ByID(u.Model)
	estimated := mdl != nil && mdl.Protocol == "megatec"
	routerID := ""
	if u.RouterID != nil {
		routerID = *u.RouterID
	}
	may := func(owner string) bool { return s.powerMayUser(owner, "read", siteOf(u)) }

	var settings notify.Settings
	if cfg, err := s.mergedSettings(); err == nil {
		settings = notify.Settings(cfg)
	}
	stamp := s.alertTimestamp()
	type delivery struct {
		key string
		msg alertdispatch.Message
		to  []alertdispatch.Recipient
	}
	var out []delivery
	for _, c := range cs {
		f, key, ok := powerFired(c, r, estimated)
		if !ok {
			continue
		}
		if f.Vars == nil {
			f.Vars = map[string]string{}
		}
		f.Vars["unitName"], f.Vars["site"] = u.Name, siteName
		var to []alertdispatch.Recipient
		// The install's flat transports only while no channels exist, the rule
		// dispatchFired follows; a unit has no router to ask per-user grants of.
		if !s.haveChannels() {
			to = s.dispatch.Recipients("", nil)
		}
		to = append(to, s.channelRecipientsFor(routerID, key, "", 0, f.Up, may)...)
		if len(to) == 0 {
			continue
		}
		dir := "down"
		if f.Up {
			dir = "up"
		}
		out = append(out, delivery{
			key: strings.Join([]string{"power", key, unitID, fmt.Sprint(c.Code), dir}, ":"),
			msg: alertdispatch.Build(settings, name, stamp, f),
			to:  to,
		})
	}
	if len(out) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, d := range out {
			for i := range d.to {
				s.dispatch.Deliver(ctx, &d.to[i], d.key, d.msg)
			}
		}
	}()
}
