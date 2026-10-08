package power

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/power/megatec"
	"mikrodash/internal/power/modbus"
	"mikrodash/internal/power/model"
)

// fakeNet stands in for every converter: registers per address and slave id,
// and a failure switch per address.
type fakeNet struct {
	mu    sync.Mutex
	regs  map[string]map[byte][]uint16
	down  map[string]bool
	dials map[string]int
	fns   map[modbus.Function]int
	reads map[string]int // per "addr/slave"
}

func newFakeNet() *fakeNet {
	return &fakeNet{regs: map[string]map[byte][]uint16{}, down: map[string]bool{},
		dials: map[string]int{}, fns: map[modbus.Function]int{}, reads: map[string]int{}}
}

func (n *fakeNet) set(addr string, slave byte, edits map[int]uint16) {
	n.mu.Lock()
	defer n.mu.Unlock()
	regs := append([]uint16(nil), onMains...)
	for k, v := range edits {
		regs[k] = v
	}
	if n.regs[addr] == nil {
		n.regs[addr] = map[byte][]uint16{}
	}
	n.regs[addr][slave] = regs
}

func (n *fakeNet) setDown(addr string, down bool) {
	n.mu.Lock()
	n.down[addr] = down
	n.mu.Unlock()
}

func (n *fakeNet) readsOf(addr string, slave byte) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.reads[addr+"/"+string(rune('0'+slave))]
}

type fakeClient struct {
	n    *fakeNet
	addr string
}

func (c *fakeClient) Read(slave byte, fn modbus.Function, start, count uint16) ([]uint16, error) {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	c.n.fns[fn]++
	c.n.reads[c.addr+"/"+string(rune('0'+slave))]++
	if c.n.down[c.addr] {
		return nil, &modbus.ConnectError{Addr: c.addr, Err: errors.New("connection refused")}
	}
	regs, ok := c.n.regs[c.addr][slave]
	if !ok {
		return nil, &modbus.ExceptionError{Function: fn, Code: 0x0B}
	}
	return append([]uint16(nil), regs[start:start+count]...), nil
}

func (c *fakeClient) Close() error { return nil }

// recorder collects what the hooks receive.
type recorder struct {
	mu       sync.Mutex
	changes  map[string][]Change
	minutes  map[string][]Minute
	states   map[string]State
	restores map[string]int
	restore  map[string]Restored
	kept     map[string][]int64
}

func newRecorder() *recorder {
	return &recorder{changes: map[string][]Change{}, minutes: map[string][]Minute{},
		states: map[string]State{}, restores: map[string]int{}, restore: map[string]Restored{},
		kept: map[string][]int64{}}
}

func (r *recorder) hooks() Hooks {
	return Hooks{
		State: func(s State) { r.mu.Lock(); r.states[s.UnitID] = s; r.mu.Unlock() },
		Changes: func(id string, cs []Change, _ *model.Reading) {
			r.mu.Lock()
			r.changes[id] = append(r.changes[id], cs...)
			r.mu.Unlock()
		},
		Minute: func(id string, m Minute) {
			r.mu.Lock()
			r.minutes[id] = append(r.minutes[id], m)
			r.mu.Unlock()
		},
		Restore: func(id string) Restored {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.restores[id]++
			return r.restore[id]
		},
		Keep: func(id string, _ model.Reading, at int64) {
			r.mu.Lock()
			r.kept[id] = append(r.kept[id], at)
			r.mu.Unlock()
		},
	}
}

func (r *recorder) changesOf(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return show(r.changes[id])
}

func (r *recorder) stateOf(id string) State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.states[id]
}

func pgModel(t *testing.T) *model.Model {
	t.Helper()
	m := model.ByID("powerguard/modbus-v1.1")
	if m == nil {
		t.Fatal("the PowerGuard definition did not load")
	}
	return m
}

// testManager has a fake network and a clock the test moves by hand.
func testManager(t *testing.T, net *fakeNet, rec *recorder) (*Manager, *int64) {
	t.Helper()
	m := NewManager(rec.hooks(), DefaultSettings())
	clock := int64(1_000_000)
	m.now = func() time.Time { return time.UnixMilli(clock) }
	m.dial = func(addr string, _ time.Duration) Reader {
		net.mu.Lock()
		net.dials[addr]++
		net.mu.Unlock()
		return &fakeClient{n: net, addr: addr}
	}
	return m, &clock
}

func TestAPollFeedsStateChangesAndHistory(t *testing.T) {
	net, rec := newFakeNet(), newRecorder()
	m, clock := testManager(t, net, rec)
	u := Unit{ID: "u1", Model: pgModel(t), Addr: "198.51.100.10:502", Slave: 1}
	c := &gateway{client: &fakeClient{n: net, addr: u.Addr}}

	net.set(u.Addr, 1, nil)
	m.pollUnit(c, u)
	s := rec.stateOf("u1")
	if !s.Online || s.Reading == nil || s.Reading.Mode != model.ModeMains || s.Polls != 1 || s.Answered != 1 {
		t.Fatalf("after one good poll: %+v", s)
	}
	if rec.restores["u1"] != 1 {
		t.Errorf("restore asked %d times before the first poll, want 1", rec.restores["u1"])
	}

	*clock += 5000
	net.set(u.Addr, 1, battery(80))
	m.pollUnit(c, u)
	if got := rec.changesOf("u1"); got != "+mains_lost@1005000" {
		t.Errorf("changes %q", got)
	}

	net.setDown(u.Addr, true)
	for i := 0; i < 3; i++ {
		*clock += 5000
		m.pollUnit(c, u)
	}
	s = rec.stateOf("u1")
	if s.Online || s.LastError == "" || s.Cause != CauseConverter || s.Polls != 5 || s.Answered != 2 {
		t.Errorf("after three failures: %+v", s)
	}
	if got := rec.changesOf("u1"); got != "+mains_lost@1005000 +not_responding@1010000" {
		t.Errorf("changes %q", got)
	}
	// The reading shown stays the last good one, not a blank.
	if s.Reading == nil || s.Reading.Mode != model.ModeBattery {
		t.Errorf("last reading lost: %+v", s.Reading)
	}

	// The polls so far fell either side of a minute boundary: 1,000,000 to
	// 1,015,000 ms are in the minute starting 960,000, and the third failure
	// at 1,020,000 opened the next. Moving on once more hands that one over.
	*clock = 1_080_000
	net.setDown(u.Addr, false)
	m.pollUnit(c, u)
	if s = rec.stateOf("u1"); s.Cause != CauseUnknown {
		t.Errorf("cause %q kept after a good poll", s.Cause)
	}
	rec.mu.Lock()
	mins := rec.minutes["u1"]
	rec.mu.Unlock()
	if len(mins) != 2 || mins[0].Start != 960_000 || mins[0].Polls != 4 || mins[0].OK != 2 ||
		len(mins[0].Stats) == 0 || mins[1].Start != 1_020_000 || mins[1].Polls != 1 || mins[1].OK != 0 {
		t.Errorf("minutes %+v", mins)
	}
	if rec.restores["u1"] != 1 {
		t.Errorf("restore asked again: %d", rec.restores["u1"])
	}
}

func TestRestoredConditionsAreNotOpenedAgain(t *testing.T) {
	net, rec := newFakeNet(), newRecorder()
	m, _ := testManager(t, net, rec)
	u := Unit{ID: "u1", Model: pgModel(t), Addr: "a:502", Slave: 1}
	rec.restore["u1"] = Restored{Open: []Change{{Cond: Cond{Kind: KindMainsLost, Text: "Mains lost"}, Began: true, At: 42}}}
	net.set(u.Addr, 1, battery(70))
	m.pollUnit(&gateway{client: &fakeClient{n: net, addr: u.Addr}}, u)
	if got := rec.changesOf("u1"); got != "" {
		t.Errorf("a restored outage began again: %q", got)
	}
	if open := rec.stateOf("u1").Open; len(open) != 1 || open[0].At != 42 {
		t.Errorf("open %+v, want the outage from 42", open)
	}
}

// waitFor polls cond for up to two seconds: the pollers run on their own
// goroutines in these tests.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestOneConnectionPerConverterAndOnlyReadsAreSent(t *testing.T) {
	net, rec := newFakeNet(), newRecorder()
	m, _ := testManager(t, net, rec)
	m.settings.Interval = 5 * time.Millisecond
	pg := pgModel(t)
	net.set("a:502", 1, nil)
	net.set("a:502", 2, battery(60))
	net.set("b:502", 1, nil)
	m.Sync([]Unit{
		{ID: "u1", Model: pg, Addr: "a:502", Slave: 1},
		{ID: "u2", Model: pg, Addr: "a:502", Slave: 2},
		{ID: "u3", Model: pg, Addr: "b:502", Slave: 1},
	})
	defer m.Stop()
	waitFor(t, "every unit polled twice", func() bool {
		return net.readsOf("a:502", 1) >= 2 && net.readsOf("a:502", 2) >= 2 && net.readsOf("b:502", 1) >= 2
	})
	net.mu.Lock()
	dials, fns := net.dials["a:502"], net.fns
	net.mu.Unlock()
	if dials != 1 {
		t.Errorf("converter a was dialled %d times for two units, want 1", dials)
	}
	for fn := range fns {
		if fn != modbus.ReadInput {
			t.Errorf("function %02x was sent", byte(fn))
		}
	}
	if got := rec.changesOf("u2"); got != "~mains_lost@1000000" {
		t.Errorf("u2 changes %q", got)
	}

	// Saving the same list again, or adding a unit on ANOTHER converter, must
	// not reconnect converter a: converters drop their oldest client, and a
	// needless reconnect is exactly that churn.
	net.set("c:502", 1, nil)
	m.Sync([]Unit{
		{ID: "u1", Model: pg, Addr: "a:502", Slave: 1},
		{ID: "u2", Model: pg, Addr: "a:502", Slave: 2},
		{ID: "u3", Model: pg, Addr: "b:502", Slave: 1},
		{ID: "u4", Model: pg, Addr: "c:502", Slave: 1},
	})
	waitFor(t, "u4 polled", func() bool { return net.readsOf("c:502", 1) > 0 })
	net.mu.Lock()
	dials = net.dials["a:502"]
	net.mu.Unlock()
	if dials != 1 {
		t.Errorf("converter a was dialled %d times after an unrelated change, want 1", dials)
	}
}

func TestReconfiguringOneUnitKeepsItsNeighboursState(t *testing.T) {
	net, rec := newFakeNet(), newRecorder()
	m, _ := testManager(t, net, rec)
	m.settings.Interval = 5 * time.Millisecond
	pg := pgModel(t)
	net.set("a:502", 1, battery(60))
	net.set("a:502", 2, nil)
	u1 := Unit{ID: "u1", Model: pg, Addr: "a:502", Slave: 1}
	m.Sync([]Unit{u1})
	defer m.Stop()
	waitFor(t, "u1 polled", func() bool { return rec.stateOf("u1").Polls > 0 })

	// A second unit on the same converter restarts its goroutine...
	m.Sync([]Unit{u1, {ID: "u2", Model: pg, Addr: "a:502", Slave: 2}})
	waitFor(t, "u2 polled", func() bool { return rec.stateOf("u2").Polls > 0 })
	// ...and u1 carries on: its outage did not begin a second time.
	waitFor(t, "u1 polled again", func() bool { return rec.stateOf("u1").Polls > 3 })
	if got := rec.changesOf("u1"); got != "~mains_lost@1000000" {
		t.Errorf("u1 changes %q, want its one outage", got)
	}

	// Changing u1's slave id is a different unit as far as the tracker knows.
	before := rec.stateOf("u1").Polls
	net.set("a:502", 3, battery(60))
	m.Sync([]Unit{{ID: "u1", Model: pg, Addr: "a:502", Slave: 3}, {ID: "u2", Model: pg, Addr: "a:502", Slave: 2}})
	waitFor(t, "u1 at its new slave id", func() bool { return net.readsOf("a:502", 3) > 0 })
	waitFor(t, "u1's fresh state", func() bool { s := rec.stateOf("u1"); return s.Polls > 0 && s.Polls < before })
	if rec.restores["u1"] != 2 {
		t.Errorf("restore asked %d times for u1, want 2 (once per configuration)", rec.restores["u1"])
	}
}

func TestSettingsChangeKeepsStateAndStopFlushes(t *testing.T) {
	net, rec := newFakeNet(), newRecorder()
	m, _ := testManager(t, net, rec)
	m.settings.Interval = 5 * time.Millisecond
	net.set("a:502", 1, battery(60))
	m.Sync([]Unit{{ID: "u1", Model: pgModel(t), Addr: "a:502", Slave: 1}})
	waitFor(t, "first poll", func() bool { return rec.stateOf("u1").Polls > 0 })

	s := DefaultSettings()
	s.Interval, s.BatteryLowPct = 5*time.Millisecond, 70
	m.SetSettings(s)
	// 60 % on battery is low now: the tracker took the new threshold, and the
	// outage that was already open did not begin again.
	waitFor(t, "battery low", func() bool { return rec.changesOf("u1") == "~mains_lost@1000000 +battery_low@1000000" })

	m.Stop()
	rec.mu.Lock()
	n := len(rec.minutes["u1"])
	rec.mu.Unlock()
	if n != 1 {
		t.Errorf("stop handed over %d minute(s), want the one in progress", n)
	}
	polls := net.readsOf("a:502", 1)
	time.Sleep(20 * time.Millisecond)
	if after := net.readsOf("a:502", 1); after != polls {
		t.Errorf("%d polls after Stop", after-polls)
	}
	m.Sync([]Unit{{ID: "u1", Model: pgModel(t), Addr: "a:502", Slave: 1}})
	time.Sleep(20 * time.Millisecond)
	if after := net.readsOf("a:502", 1); after != polls {
		t.Error("a stopped manager started polling again")
	}
}

// A UNIT SILENT AFTER A RESTART STILL SHOWS WHAT IT LAST SAID, and when: the
// reading kept before the restart comes back with its time, and silence does
// not replace it.
func TestTheLastReadingSurvivesARestart(t *testing.T) {
	net, rec := newFakeNet(), newRecorder()
	m, clock := testManager(t, net, rec)
	u := Unit{ID: "u1", Model: pgModel(t), Addr: "198.51.100.10:502", Slave: 1}
	c := &gateway{client: &fakeClient{n: net, addr: u.Addr}}

	net.set(u.Addr, 1, battery(55))
	before, err := u.Model.Decode([][]uint16{net.regs[u.Addr][1]})
	if err != nil {
		t.Fatal(err)
	}
	rec.restore["u1"] = Restored{Last: &before, LastOK: 777_000}
	net.setDown(u.Addr, true)
	for i := 0; i < 3; i++ {
		*clock += 5000
		m.pollUnit(c, u)
	}
	s := rec.stateOf("u1")
	if s.Online || s.Reading == nil || s.LastOK != 777_000 || s.Reading.Values["battery_pct"] != before.Values["battery_pct"] {
		t.Errorf("after the restart: online %v, last ok %d, reading %+v", s.Online, s.LastOK, s.Reading)
	}
	if len(rec.kept["u1"]) != 0 {
		t.Errorf("a silent unit kept a reading: %v", rec.kept["u1"])
	}
}

// A READING IS KEPT AT MOST ONCE A MINUTE while the unit answers, and the one
// since the last kept is handed over when the poller stops, so a restart loses
// none.
func TestTheLastReadingIsKeptOnceAMinuteAndOnStop(t *testing.T) {
	net, rec := newFakeNet(), newRecorder()
	m, clock := testManager(t, net, rec)
	u := Unit{ID: "u1", Model: pgModel(t), Addr: "198.51.100.10:502", Slave: 1}
	c := &gateway{client: &fakeClient{n: net, addr: u.Addr}}
	net.set(u.Addr, 1, nil)
	for i := 0; i < 14; i++ { // 0 to 65 s, every 5 s
		m.pollUnit(c, u)
		*clock += 5000
	}
	start := int64(1_000_000)
	if got := rec.kept["u1"]; len(got) != 2 || got[0] != start || got[1] != start+60_000 {
		t.Errorf("kept at %v, want %d and %d", got, start, start+60_000)
	}
	m.Stop()
	if got := rec.kept["u1"]; len(got) != 3 || got[2] != start+65_000 {
		t.Errorf("on stop, kept %v; want the reading at %d last", got, start+65_000)
	}
}

// fakeUPS answers Megatec commands; rating "" means it echoes F (unsupported).
type fakeUPS struct {
	mu     sync.Mutex
	status string
	rating string
	sent   []string
}

func (f *fakeUPS) Query(c megatec.Command) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c == megatec.QueryStatus {
		f.sent = append(f.sent, "Q1")
		return f.status, nil
	}
	f.sent = append(f.sent, "F")
	if f.rating == "" {
		return "", megatec.ErrUnsupported
	}
	return f.rating, nil
}

func (f *fakeUPS) Close() error { return nil }

// A MEGATEC UPS IS POLLED WITH Q1, AND ASKED ITS RATING ONCE: the battery is
// sized from it, and a UPS that does not know F is not asked again.
func TestAMegatecUPSIsPolledByStatusLine(t *testing.T) {
	mdl := model.ByID("powerguard/megatec")
	if mdl == nil {
		t.Fatal("the Megatec map did not load")
	}
	for _, rating := range []string{"#220.0 004 096.0 50.0", ""} {
		rec := newRecorder()
		m := NewManager(rec.hooks(), DefaultSettings())
		ups := &fakeUPS{status: "(230.0 230.0 230.0 012 50.0 2.27 25.0 00000000", rating: rating}
		u := Unit{ID: "ups", Model: mdl, Addr: "198.51.100.40:4001", Slave: 1}
		g := &gateway{megatec: ups, rating: map[string]*megatec.Rating{}, asked: map[string]int{}}
		for i := 0; i < 3; i++ {
			m.pollUnit(g, u)
		}
		if got := strings.Join(ups.sent, ","); got != "Q1,F,Q1,Q1" {
			t.Errorf("rating %q: sent %s", rating, got)
		}
		st := rec.stateOf("ups")
		if st.Reading == nil || st.Reading.Mode != model.ModeMains || st.Answered != 3 {
			t.Fatalf("rating %q: state %+v", rating, st)
		}
		if _, has := st.Reading.Values["battery_v"]; has != (rating != "") {
			t.Errorf("rating %q: battery voltage present %v", rating, has)
		}
	}
}
