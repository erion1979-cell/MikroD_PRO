package server

// Dashboard cards that follow a device of their own (docs/dashboards/PLAN.md,
// phase 2): the server half of "watching" a router without selecting it.
//
// ── THE CLIENT SENDS THE WHOLE SET ──────────────────────────────────────────
//
// `dash:watch` carries every (router, card, interface) the dashboard on screen
// shows, and replaces the last set. So there is no join or leave to lose: the
// set is recomputed on every message, on every router switch and on the
// revalidator's tick, and the rooms follow it.
//
// ── A WATCH IS A SUBSET OF A SELECTION ──────────────────────────────────────
//
// Watching router R for a card joins the rooms selecting R would join for that
// card, under the checks selecting R would pass (router:read) plus the ones the
// card's room is gated on today (the Dashboard page and the card's own page, on
// R). So a dashboard can show nothing its user could not already see by picking
// R in the header. Frames reach the browser tagged with R (hub Envelope.Router),
// and only router-aware listeners take them (web/src/socket.ts).
//
// ── DEMAND, NOT A HOLD ──────────────────────────────────────────────────────
//
// As with the device modal (peek.go): a watched room is a reason on R's session
// that `applyDemand` already understands, so the collectors a card needs run
// while it is on screen and are handed to `suspendAfterGrace` when it goes. No
// session, connection or hold is added. A router with no live session (disabled,
// or not dialled) is skipped and picked up on the next recompute.
//
// ── NEVER OUT OF A ROOM THE SELECTION NEEDS ─────────────────────────────────
//
// A watch on the selected router shares rooms with the selection. Dropping that
// watch leaves only the rooms the selection does not hold, so removing a card
// cannot silence the rest of the page.

import (
	"sort"

	"mikrodash/internal/collect"
	"mikrodash/internal/dashcards"
	"mikrodash/internal/session"
)

// dashWatchMax bounds one message: 20 dashboards are allowed, but only the one
// on screen is watched, and 100 cards is the most it can hold.
const dashWatchMax = 100

// dashWatchCards are the card types that can follow a device of their own.
var dashWatchCards = map[string]bool{
	"card-system": true, "card-traffic": true, "dc-card-bw": true,
	"dc-card-ping": true, "dc-card-wanflow": true, "dc-card-physports": true,
}

// dashWatch is one card on screen that follows a device of its own.
type dashWatch struct {
	Router string `json:"router"`
	Card   string `json:"card"`
	Iface  string `json:"iface"`
}

// watchState is a connection's watches and what they hold. Read and written
// only on the connection's loop.
type watchState struct {
	list  []dashWatch
	rooms map[string]string // rooms joined for watches, to their router
	ifs   map[string]bool   // router\x00iface traffic streams held with Watch
	seen  map[string]bool   // router\x00card\x00iface already replayed
}

func watchKey(parts ...string) string {
	k := parts[0]
	for _, p := range parts[1:] {
		k += "\x00" + p
	}
	return k
}

// setWatches replaces the set and applies it.
func (cn *conn) setWatches(in []dashWatch) {
	if len(in) > dashWatchMax {
		in = in[:dashWatchMax]
	}
	// The devices a dashboard may name, plus the selection its other cards
	// follow: the watches past that many devices are dropped, so a browser
	// cannot run more routers' collectors than a saved dashboard could.
	out := make([]dashWatch, 0, len(in))
	routers := map[string]bool{}
	for _, w := range in {
		if w.Router == "" || !dashWatchCards[w.Card] {
			continue
		}
		if !routers[w.Router] && len(routers) == dashDevicesMax+1 {
			continue
		}
		routers[w.Router] = true
		out = append(out, w)
	}
	cn.watch.list = out
	cn.applyWatches()
}

// mayWatch is the check a watch passes, every time it is applied.
func (cn *conn) mayWatch(w dashWatch) bool {
	if cn.sess == nil || !cn.sess.CanReadRouter(w.Router) {
		return false
	}
	sc := connScope{sess: cn.sess, routerID: w.Router}
	if !cn.canPageIn(sc, "dashboard", "read") {
		return false
	}
	for _, c := range dashcards.All {
		if c.ID == w.Card && c.Page != "" {
			return cn.canPageIn(sc, c.Page, "read")
		}
	}
	return true
}

// applyWatches makes the rooms, streams and replays match the set.
func (cn *conn) applyWatches() {
	w := &cn.watch
	if w.rooms == nil {
		w.rooms, w.ifs, w.seen = map[string]string{}, map[string]bool{}, map[string]bool{}
	}
	rooms, ifs, seen := map[string]string{}, map[string]bool{}, map[string]bool{}
	type fresh struct {
		w  dashWatch
		rs *session.Session
	}
	var news []fresh
	touched := map[string]bool{}
	for k := range w.ifs {
		touched[k[:indexNul(k)]] = true
	}
	for _, id := range w.rooms {
		touched[id] = true
	}
	for _, x := range cn.watch.list {
		if !cn.mayWatch(x) {
			continue
		}
		// A router with no session yet still has its rooms joined, so its data
		// flows the moment it connects; only the replay, a traffic card's
		// interface and the demand re-ask need the session.
		rs := cn.srv.liveSession(x.Router)
		touched[x.Router] = true
		iface := x.Iface
		switch x.Card {
		case "card-system":
			rooms["router-"+x.Router] = x.Router
		case "card-traffic", "dc-card-bw":
			rooms["router-"+x.Router] = x.Router
			if rs == nil {
				continue
			}
			iface = watchIface(rs, iface, x.Card == "dc-card-bw")
			if iface == "" {
				continue
			}
			ifs[watchKey(x.Router, iface)] = true
			rooms[session.RoomFor(x.Router, collect.TrafficSub(iface))] = x.Router
		default:
			// The card's own room, as the Dashboard's copy of the card joins it.
			for _, c := range dashcards.All {
				if c.ID == x.Card && c.Room != "" {
					rooms[dashCardRoomFor(x.Router, c.Room)] = x.Router
				}
			}
		}
		k := watchKey(x.Router, x.Card, iface)
		// Replayed once its router has a session; until then it is not "seen",
		// so the first apply after the router connects replays it.
		if rs == nil {
			continue
		}
		seen[k] = true
		if !w.seen[k] {
			news = append(news, fresh{w: dashWatch{Router: x.Router, Card: x.Card, Iface: iface}, rs: rs})
		}
	}

	for room := range rooms {
		if _, had := w.rooms[room]; !had {
			cn.srv.hub.Join(cn.c, room)
		}
	}
	for room := range w.rooms {
		if _, still := rooms[room]; !still && !cn.heldBySelection(room) {
			cn.srv.hub.Leave(cn.c, room)
		}
	}
	// Streams: Watch for each new one (its backlog is replayed below), Unwatch
	// each one no card wants any more. The refcount is the collector's.
	backlog := map[string]collect.TrafficHistory{}
	for k := range ifs {
		if !w.ifs[k] {
			i := indexNul(k)
			if rs := cn.srv.liveSession(k[:i]); rs != nil {
				backlog[k] = rs.Traffic().Watch(k[i+1:])
			}
		}
	}
	for k := range w.ifs {
		if !ifs[k] {
			i := indexNul(k)
			if rs := cn.srv.liveSession(k[:i]); rs != nil {
				rs.Traffic().Unwatch(k[i+1:])
			}
		}
	}
	w.rooms, w.ifs, w.seen = rooms, ifs, seen

	for _, n := range news {
		cn.replayWatch(n.w, n.rs, backlog)
	}
	ids := make([]string, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if rs := cn.srv.liveSession(id); rs != nil {
			cn.srv.applyDemand(rs, id)
		}
	}
}

// dropWatches lets go of everything, for a closing connection.
func (cn *conn) dropWatches() {
	cn.watch.list = nil
	cn.applyWatches()
}

// heldBySelection is whether the selected router's own subscriptions need this
// room, so a watch going away must not leave it.
func (cn *conn) heldBySelection(room string) bool {
	if cn.routerID == "" {
		return false
	}
	if room == "router-"+cn.routerID {
		return true
	}
	if cn.trafficIf != "" && room == session.RoomFor(cn.routerID, collect.TrafficSub(cn.trafficIf)) {
		return true
	}
	cn.mu.Lock()
	defer cn.mu.Unlock()
	for key := range cn.cards {
		if room == cn.dashCardRoom(key) {
			return true
		}
	}
	return false
}

// watchIface is the interface a traffic copy streams: the one it names, if the
// router has it, else (for the Bandwidth card, or a copy that names none) the
// router's default.
func watchIface(rs *session.Session, name string, defaultOnly bool) string {
	tr := rs.Traffic()
	if defaultOnly || name == "" {
		return tr.DefaultIf()
	}
	if last := rs.IfStatus().Last(); last != nil {
		names := make([]string, 0, len(last.Interfaces))
		for _, i := range last.Interfaces {
			names = append(names, i.Name)
		}
		tr.SetAvailable(names)
	}
	if n, ok := tr.NormalizeIfName(name); ok {
		return n
	}
	return ""
}

// replayWatch sends a new watch the last readings its card draws from, tagged
// with the router, so it is not empty until the next tick.
func (cn *conn) replayWatch(w dashWatch, rs *session.Session, backlog map[string]collect.TrafficHistory) {
	h, c, r := cn.srv.hub, cn.c, w.Router
	switch w.Card {
	case "card-system":
		if last := rs.System().Last(); last != nil {
			collect.EvSystemUpdate.SendFrom(h, c, r, *last)
		}
	case "card-traffic", "dc-card-bw":
		if last := rs.IfStatus().Last(); last != nil {
			collect.EvIfstatusNames.SendFrom(h, c, r, *collect.NamesOf(last))
		}
		if hist, ok := backlog[watchKey(r, w.Iface)]; ok {
			EvDashTrafficHistory.SendFrom(h, c, r, hist)
		}
	case "dc-card-ping":
		if p := rs.Ping(); p != nil {
			if last := p.Last(); last != nil {
				collect.EvPingUpdate.SendFrom(h, c, r, *last)
			}
			if hist := p.History(); len(hist.History) > 0 {
				out := map[string]any{"target": hist.Target, "history": hist.History}
				if last := p.Last(); last != nil {
					out["minRtt"] = last.MinRTT
					out["maxRtt"] = last.MaxRTT
				}
				EvPingHistory.SendFrom(h, c, r, out)
			}
		}
	case "dc-card-wanflow":
		if last := rs.Wan().Last(); last != nil {
			collect.EvWanUpdate.SendFrom(h, c, r, *last)
		}
	case "dc-card-physports":
		if last := rs.IfStatus().Last(); last != nil {
			collect.EvIfstatusUpdate.SendFrom(h, c, r, *last)
		}
	}
}

func indexNul(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return i
		}
	}
	return len(s)
}
