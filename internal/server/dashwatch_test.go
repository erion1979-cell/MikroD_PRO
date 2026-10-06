package server

import (
	"fmt"
	"sort"
	"testing"
)

// watchViewer may read r1 and r2, with the Dashboard and the WAN page but not
// the Interfaces page.
func watchViewer() *Session {
	return &Session{AuthMode: "password", Readable: []string{"r1", "r2"},
		Pages: map[string]string{"dashboard": "read", "wan": "read"}}
}

func roomsOf(cn *conn) []string {
	out := cn.c.Rooms()
	sort.Strings(out)
	return out
}

// A WATCH JOINS THE ROOMS ITS CARD NEEDS ON THE WATCHED ROUTER, each card's
// own room as the Dashboard's copy of the card would join it, and nothing for
// a card that cannot follow a device.
func TestAWatchJoinsItsCardsRoomsOnThatRouter(t *testing.T) {
	cn, _, _ := peekConn(watchViewer())
	cn.setWatches([]dashWatch{
		{Router: "r2", Card: "card-system"},
		{Router: "r2", Card: "dc-card-ping"},
		{Router: "r2", Card: "dc-card-wanflow"},
		{Router: "r2", Card: "dc-card-logs"}, // not a card that follows a device
	})
	want := []string{"router-r2", "router-r2-dash-card-ping", "router-r2-dash-card-wan"}
	if got := roomsOf(cn); !equalStrings(got, want) {
		t.Errorf("rooms %v, want %v", got, want)
	}
	// Replacing the set leaves what is no longer wanted.
	cn.setWatches([]dashWatch{{Router: "r2", Card: "dc-card-ping"}})
	if got := roomsOf(cn); !equalStrings(got, []string{"router-r2-dash-card-ping"}) {
		t.Errorf("after narrowing the set: %v", got)
	}
	cn.dropWatches()
	if got := roomsOf(cn); len(got) != 0 {
		t.Errorf("after dropping: %v", got)
	}
}

// A WATCH PASSES WHAT SELECTING THE ROUTER WOULD, plus the card's own page:
// no router grant, no rooms; no Interfaces page, no Physical Ports card.
func TestAWatchIsGatedLikeASelection(t *testing.T) {
	cn, _, _ := peekConn(watchViewer())
	cn.setWatches([]dashWatch{
		{Router: "r3", Card: "card-system"},       // no grant on r3
		{Router: "r2", Card: "dc-card-physports"}, // no Interfaces page
		{Router: "r2", Card: "dc-card-wanflow"},   // the control
	})
	if got := roomsOf(cn); !equalStrings(got, []string{"router-r2-dash-card-wan"}) {
		t.Errorf("rooms %v, want only the WAN Flow card's on r2", got)
	}
	// A grant lost is a watch dropped the next time the set is applied, which
	// the revalidator does every minute.
	cn.sess = &Session{AuthMode: "password", Readable: []string{"r1"},
		Pages: map[string]string{"dashboard": "read", "wan": "read"}}
	cn.applyWatches()
	if got := roomsOf(cn); len(got) != 0 {
		t.Errorf("after losing r2: %v", got)
	}
}

// A WATCH NEVER TAKES THE CONNECTION OUT OF A ROOM ITS SELECTION NEEDS, and a
// router switch, which leaves every other room, keeps the watches'.
func TestAWatchAndTheSelectionShareRoomsSafely(t *testing.T) {
	cn, h, _ := peekConn(watchViewer())
	cn.routerID = "r1"
	h.Join(cn.c, "router-r1")
	cn.setWatches([]dashWatch{{Router: "r1", Card: "card-system"}, {Router: "r2", Card: "card-system"}})
	cn.setWatches(nil)
	if got := roomsOf(cn); !equalStrings(got, []string{"router-r1"}) {
		t.Errorf("dropping a watch on the selected router left its room: %v", got)
	}
	cn.setWatches([]dashWatch{{Router: "r2", Card: "dc-card-ping"}})
	cn.leaveRouterRooms()
	if got := roomsOf(cn); !equalStrings(got, []string{"router-r2-dash-card-ping"}) {
		t.Errorf("a router switch left the watch's room, or kept the selection's: %v", got)
	}
}

func TestAWatchSetIsBounded(t *testing.T) {
	cn, _, _ := peekConn(watchViewer())
	many := make([]dashWatch, 150)
	for i := range many {
		many[i] = dashWatch{Router: "r2", Card: "dc-card-ping"}
	}
	cn.setWatches(many)
	if len(cn.watch.list) != dashWatchMax {
		t.Errorf("kept %d watches, want at most %d", len(cn.watch.list), dashWatchMax)
	}
}

// A WATCH SET READS AT MOST THE DEVICES A DASHBOARD MAY NAME, plus the
// selection: a browser cannot run more routers' collectors than a saved
// dashboard could. Further cards on a device already counted are kept.
func TestAWatchSetReadsABoundedNumberOfDevices(t *testing.T) {
	cn, _, _ := peekConn(watchViewer())
	var in []dashWatch
	for i := 0; i < dashDevicesMax+3; i++ {
		in = append(in, dashWatch{Router: fmt.Sprintf("r%d", i), Card: "card-system"})
	}
	in = append(in, dashWatch{Router: "r0", Card: "dc-card-ping"})
	cn.setWatches(in)
	routers := map[string]bool{}
	for _, w := range cn.watch.list {
		routers[w.Router] = true
	}
	if len(routers) != dashDevicesMax+1 || routers[fmt.Sprintf("r%d", dashDevicesMax+1)] {
		t.Errorf("watching %d devices %v, want the first %d", len(routers), routers, dashDevicesMax+1)
	}
	if last := cn.watch.list[len(cn.watch.list)-1]; last.Router != "r0" || last.Card != "dc-card-ping" {
		t.Errorf("a second card on a counted device was dropped: %+v", last)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
