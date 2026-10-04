package power

// The pollers: the only part of the Power/UPS module that talks to a network.
//
// ── ONE GOROUTINE PER CONVERTER, NOT PER UNIT ───────────────────────────────
//
// A converter accepts few TCP clients and drops the oldest when another
// connects, and the serial line behind it carries one request at a time. So
// every unit behind one address (host:port) is polled by ONE goroutine over ONE
// connection, one unit after another. Several units on one converter only work
// when their Modbus slave ids differ, which is the operator's to arrange.
//
// ── STATE OUTLIVES THE GOROUTINES ───────────────────────────────────────────
//
// Adding a second unit to a converter restarts that converter's goroutine, and
// that must not make the first unit forget it was on battery. Each unit's
// tracker and history bucket live in the Manager, keyed by unit id, and are
// reset only when that unit's own address, slave id or model changes.
//
// ── WHAT LEAVES THIS FILE ───────────────────────────────────────────────────
//
// Nothing here writes a database or a socket. Each poll hands its results to
// `Hooks`, which the server wires to storage, the page and notifications. The
// hooks are called outside the Manager's lock, so a slow database write cannot
// stall another converter's poll.

import (
	"sort"
	"sync"
	"time"

	"mikrodash/internal/power/modbus"
	"mikrodash/internal/power/model"
	"mikrodash/internal/safe"
)

// Reader is the one Modbus call a poll needs. *modbus.Client implements it.
type Reader interface {
	Read(unit byte, fn modbus.Function, start, count uint16) ([]uint16, error)
	Close() error
}

// Unit is one inverter or UPS to poll.
type Unit struct {
	ID    string
	Model *model.Model
	// Addr is the converter's host:port.
	Addr  string
	Slave byte
}

// Settings are the poll timing and the tracker thresholds.
type Settings struct {
	Interval      time.Duration
	Timeout       time.Duration
	OfflineAfter  int
	BatteryLowPct float64
}

// DefaultSettings is HANDOFF.md section 3: every 5 s, a 2 s reply timeout,
// offline after 3 failures; battery low at 20 % as agreed on 2026-10-04.
func DefaultSettings() Settings {
	return Settings{Interval: 5 * time.Second, Timeout: 2 * time.Second,
		OfflineAfter: DefaultOfflineAfter, BatteryLowPct: DefaultBatteryLowPct}
}

// State is one unit as the page shows it.
type State struct {
	UnitID string
	Online bool
	// Reading is the last good reading; nil until the unit first answers.
	Reading *model.Reading
	// LastOK is when that reading was taken, Unix ms; 0 if never.
	LastOK  int64
	ReplyMs float64
	// Polls and Answered count since this process started (or since the unit's
	// configuration last changed).
	Polls, Answered int64
	// LastError is the most recent failure, sanitised for a browser.
	LastError string
	Open      []Change
}

// Hooks receive what each poll produced. Any may be nil.
type Hooks struct {
	// State is called after every poll of a unit.
	State func(State)
	// Changes is called when conditions began or ended.
	Changes func(unitID string, cs []Change)
	// Minute is called with each finished minute of history.
	Minute func(unitID string, m Minute)
	// Restore supplies a unit's conditions left open by a previous run. Called
	// once, before the unit's first poll.
	Restore func(unitID string) []Change
}

// Manager runs the pollers.
type Manager struct {
	hooks Hooks
	dial  func(addr string, timeout time.Duration) Reader
	now   func() time.Time

	mu       sync.Mutex
	settings Settings
	gateways map[string]*gateway
	units    map[string]*unitState
	stopped  bool
}

type gateway struct {
	addr   string
	units  []Unit
	client Reader
	stop   chan struct{}
	done   chan struct{}
}

type unitState struct {
	cfg     Unit
	tracker *Tracker
	bucket  Bucket
	state   State
}

// NewManager returns a Manager polling nothing until Sync is called.
func NewManager(h Hooks, s Settings) *Manager {
	return &Manager{
		hooks: h,
		dial: func(addr string, timeout time.Duration) Reader {
			return modbus.New(addr, timeout)
		},
		now:      time.Now,
		settings: s,
		gateways: map[string]*gateway{},
		units:    map[string]*unitState{},
	}
}

// Sync makes the running pollers match units: converters no longer named stop,
// new ones start, and a converter whose set of units changed restarts.
func (m *Manager) Sync(units []Unit) {
	want := map[string][]Unit{}
	ids := map[string]Unit{}
	for _, u := range units {
		want[u.Addr] = append(want[u.Addr], u)
		ids[u.ID] = u
	}
	for _, us := range want {
		sort.Slice(us, func(i, j int) bool { return us[i].ID < us[j].ID })
	}

	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	var stop []*gateway
	var flushed []unitMinute
	for addr, g := range m.gateways {
		if !sameUnits(g.units, want[addr]) {
			stop = append(stop, g)
			delete(m.gateways, addr)
		}
	}
	for id, us := range m.units {
		if u, ok := ids[id]; !ok || u.Addr != us.cfg.Addr || u.Slave != us.cfg.Slave || u.Model != us.cfg.Model {
			if f := us.bucket.Flush(); f != nil {
				flushed = append(flushed, unitMinute{id, *f})
			}
			delete(m.units, id)
		}
	}
	var start []*gateway
	for addr, us := range want {
		if _, running := m.gateways[addr]; running {
			continue
		}
		g := &gateway{addr: addr, units: us, stop: make(chan struct{}), done: make(chan struct{})}
		m.gateways[addr] = g
		start = append(start, g)
	}
	m.mu.Unlock()

	for _, g := range stop {
		close(g.stop)
		<-g.done
	}
	m.emitMinutes(flushed)
	for _, g := range start {
		go m.run(g)
	}
}

func sameUnits(a, b []Unit) bool {
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

// SetSettings applies new timing and thresholds. Each converter's goroutine
// restarts so a new interval takes effect at once; the units' trackers and
// history are kept, so an ongoing outage carries on.
func (m *Manager) SetSettings(s Settings) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.settings = s
	for _, us := range m.units {
		us.tracker.OfflineAfter, us.tracker.BatteryLowPct = s.OfflineAfter, s.BatteryLowPct
	}
	old := m.gateways
	m.gateways = map[string]*gateway{}
	fresh := make([]*gateway, 0, len(old))
	for addr, g := range old {
		ng := &gateway{addr: addr, units: g.units, stop: make(chan struct{}), done: make(chan struct{})}
		m.gateways[addr] = ng
		fresh = append(fresh, ng)
	}
	m.mu.Unlock()

	for _, g := range old {
		close(g.stop)
		<-g.done
	}
	for _, g := range fresh {
		go m.run(g)
	}
}

// Snapshot is every unit's current state, by unit id.
func (m *Manager) Snapshot() []State {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]State, 0, len(m.units))
	for _, us := range m.units {
		out = append(out, us.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UnitID < out[j].UnitID })
	return out
}

// Stop ends every poller and hands over the minutes in progress. The Manager
// polls nothing afterwards.
func (m *Manager) Stop() {
	m.Sync(nil)
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
}

func (m *Manager) run(g *gateway) {
	defer close(g.done)
	m.mu.Lock()
	interval, timeout := m.settings.Interval, m.settings.Timeout
	m.mu.Unlock()
	g.client = m.dial(g.addr, timeout)
	defer g.client.Close()

	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		m.pollGateway(g)
		select {
		case <-g.stop:
			return
		case <-tick.C:
		}
	}
}

// pollGateway polls every unit behind one converter, one after another.
func (m *Manager) pollGateway(g *gateway) {
	for _, u := range g.units {
		select {
		case <-g.stop:
			return
		default:
		}
		m.pollUnit(g.client, u)
	}
}

type unitMinute struct {
	id string
	m  Minute
}

func (m *Manager) pollUnit(c Reader, u Unit) {
	m.mu.Lock()
	us := m.units[u.ID]
	m.mu.Unlock()
	// Restore runs outside the lock (it reads the database) and only once.
	if us == nil {
		var restored []Change
		if m.hooks.Restore != nil {
			restored = m.hooks.Restore(u.ID)
		}
		m.mu.Lock()
		us = m.adopt(u, restored)
		m.mu.Unlock()
	}

	began := m.now()
	regs := make([][]uint16, 0, len(u.Model.Reads))
	var err error
	for _, r := range u.Model.Reads {
		var got []uint16
		if got, err = c.Read(u.Slave, r.Function, r.Start, r.Count); err != nil {
			break
		}
		regs = append(regs, got)
	}
	var reading model.Reading
	if err == nil {
		reading, err = u.Model.Decode(regs)
	}
	at := m.now()
	ms := at.UnixMilli()

	m.mu.Lock()
	if m.units[u.ID] != us {
		// The unit was removed or reconfigured while this poll was in flight.
		m.mu.Unlock()
		return
	}
	var changes []Change
	var done *Minute
	us.state.Polls++
	if err != nil {
		us.state.LastError = safe.Message(err.Error())
		changes = us.tracker.Failure(ms)
		done = us.bucket.Fail(ms)
	} else {
		reply := float64(at.Sub(began).Microseconds()) / 1000
		r := reading
		us.state.Answered++
		us.state.Reading, us.state.LastOK, us.state.ReplyMs = &r, ms, reply
		changes = us.tracker.Success(reading, ms)
		done = us.bucket.Add(reading, reply, ms)
	}
	snap := us.snapshot()
	m.mu.Unlock()

	if len(changes) > 0 && m.hooks.Changes != nil {
		m.hooks.Changes(u.ID, changes)
	}
	if done != nil {
		m.emitMinutes([]unitMinute{{u.ID, *done}})
	}
	if m.hooks.State != nil {
		m.hooks.State(snap)
	}
}

// adopt creates a unit's state, unless another poll got there first. Caller
// holds m.mu.
func (m *Manager) adopt(u Unit, restored []Change) *unitState {
	if us := m.units[u.ID]; us != nil {
		return us
	}
	tr := &Tracker{OfflineAfter: m.settings.OfflineAfter, BatteryLowPct: m.settings.BatteryLowPct}
	tr.Restore(restored)
	us := &unitState{cfg: u, tracker: tr, state: State{UnitID: u.ID, Online: true}}
	m.units[u.ID] = us
	return us
}

func (us *unitState) snapshot() State {
	s := us.state
	s.Online = us.tracker.Online()
	s.Open = us.tracker.Open()
	return s
}

func (m *Manager) emitMinutes(ms []unitMinute) {
	if m.hooks.Minute == nil {
		return
	}
	for _, um := range ms {
		m.hooks.Minute(um.id, um.m)
	}
}
