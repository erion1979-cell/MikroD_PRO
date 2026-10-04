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

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/alertdispatch"
	"mikrodash/internal/notify"
	"mikrodash/internal/power"
)

// powerFired is what one change says: the catalogue's name for its direction,
// the condition as the subject, and a detail line. ok is false for a kind that
// is not sent (see power.NoticeKey).
func powerFired(c power.Change) (f alert.Fired, key string, ok bool) {
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
		f.Detail = "no Modbus reply from the unit"
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
	return f, key, true
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
func (s *Server) dispatchPower(unitID string, cs []power.Change) {
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
	name := u.Name
	if site := siteOf(u); site != "" {
		if sites, err := s.auditDB.ListSites(); err == nil {
			for _, st := range sites {
				if st.ID == site {
					name += " (" + st.Name + ")"
				}
			}
		}
	}
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
		f, key, ok := powerFired(c)
		if !ok {
			continue
		}
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
