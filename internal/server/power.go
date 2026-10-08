package server

// The Power/UPS module's place in the server: the pollers (internal/power),
// their storage, and (in later files) the page's routes.
//
// ── ON UNLESS -no-pool ──────────────────────────────────────────────────────
//
// The pollers run whether or not anyone has the page open, because a mains
// outage is only visible while it lasts: a poller started when somebody opens
// the page would miss every outage that happened before. `-no-pool` is the
// existing switch for a second MikroDash watching the same fleet, and it stops
// these too, because a converter drops its oldest client and two pollers would
// keep knocking each other off.
//
// ── HISTORY ONLY UNDER -history, EVENTS ALWAYS ──────────────────────────────
//
// Minute rows are history, written only under `-history` like the routers'
// traffic history and for its reason (two writers, two rows a minute). Events
// are written regardless, as router alerts are: they are what the page lists
// and what a restart restores the trackers from.

import (
	"encoding/json"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/power"
	"mikrodash/internal/power/model"
)

type powerState struct {
	mu      sync.Mutex
	manager *power.Manager
	// units is every stored unit by id, refreshed by powerSync: what a live
	// update is checked against before it is sent (power_live.go) and what a
	// notification names (power_notify.go).
	units map[string]db.PowerUnit
	// watchers is every socket wanting live updates, with why: the page, the
	// Dashboard card, or both (power_live.go).
	watchers map[*conn]map[string]bool
	// applied is the settings the pollers run with, so a save that changes
	// none of them restarts nothing.
	applied power.Settings
}

// powerStart builds and starts the pollers, unless -no-pool or there is no
// database to keep units in.
func (s *Server) powerStart(noPool, history bool) {
	if noPool || s.auditDB == nil {
		return
	}
	if _, err := model.All(); err != nil {
		// A broken definition fails the tests before a release, so this is a
		// build that should not exist; said, and the models that did load are
		// still served.
		log.Printf("[power] WARNING: a model definition did not load: %v", err)
	}
	adb := s.auditDB
	hooks := power.Hooks{
		State: s.powerPush,
		Restore: func(unitID string) power.Restored {
			var out power.Restored
			open, err := adb.PowerEvents(unitID, true, 100)
			if err != nil {
				log.Printf("[power] could not read open events for %s: %v", unitID, err)
			}
			for _, e := range open {
				out.Open = append(out.Open, power.Change{Cond: power.Cond{Kind: power.Kind(e.Kind),
					Code: e.Code, Text: e.Text, Fault: e.Fault}, Began: true, At: e.BeganAt, Initial: e.Initial})
			}
			// The last reading kept before the restart: a unit still silent
			// shows it as last known, with its age, not "no reading yet". One
			// this build cannot read is dropped, not fatal.
			at, raw, err := adb.PowerLast(unitID)
			if err != nil {
				log.Printf("[power] could not read the last reading of %s: %v", unitID, err)
			}
			var r model.Reading
			if at > 0 && json.Unmarshal(raw, &r) == nil {
				out.Last, out.LastOK = &r, at
			}
			return out
		},
		Keep: func(unitID string, r model.Reading, at int64) {
			raw, err := json.Marshal(r)
			if err == nil {
				err = adb.SavePowerLast(unitID, at, raw)
			}
			if err != nil {
				log.Printf("[power] could not keep the last reading of %s: %v", unitID, err)
			}
		},
		Changes: func(unitID string, cs []power.Change, r *model.Reading) {
			for _, c := range cs {
				var err error
				if c.Began {
					err = adb.BeginPowerEvent(db.PowerEvent{UnitID: unitID, Kind: string(c.Kind),
						Code: c.Code, Text: c.Text, Fault: c.Fault, Initial: c.Initial, BeganAt: c.At})
				} else {
					err = adb.EndPowerEvent(unitID, string(c.Kind), c.Code, c.At)
				}
				if err != nil {
					log.Printf("[power] could not record %s on %s: %v", c.Kind, unitID, err)
				}
			}
			s.dispatchPower(unitID, cs, r)
		},
	}
	if history {
		hooks.Minute = func(unitID string, m power.Minute) {
			stats := make([]db.PowerStat, 0, len(m.Stats))
			for _, st := range m.Stats {
				stats = append(stats, db.PowerStat{Key: st.Key, Avg: st.Avg, Min: st.Min, Max: st.Max})
			}
			if err := adb.RecordPowerMinute(unitID, m.Start, m.Polls, m.OK, m.ReplyMs, stats); err != nil {
				log.Printf("[power] could not record a minute for %s: %v", unitID, err)
			}
		}
	}
	set := s.powerSettings()
	s.power.mu.Lock()
	s.power.manager = power.NewManager(hooks, set)
	s.power.applied = set
	s.power.mu.Unlock()
	s.powerSync()
}

// powerSync points the pollers at the enabled units in the database. Called at
// start and after every change to a unit.
func (s *Server) powerSync() {
	s.power.mu.Lock()
	m := s.power.manager
	s.power.mu.Unlock()
	if m == nil {
		return
	}
	rows, err := s.auditDB.PowerUnits()
	if err != nil {
		log.Printf("[power] could not read units: %v", err)
		return
	}
	units := map[string]db.PowerUnit{}
	for _, r := range rows {
		units[r.ID] = r
	}
	s.power.mu.Lock()
	s.power.units = units
	s.power.mu.Unlock()
	m.Sync(powerUnits(rows))
}

// powerUnits turns stored units into what the pollers poll: enabled ones whose
// model this build knows and whose slave id fits a byte.
func powerUnits(rows []db.PowerUnit) []power.Unit {
	out := make([]power.Unit, 0, len(rows))
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		mdl := model.ByID(r.Model)
		if mdl == nil {
			log.Printf("[power] %s names model %q, which this build does not have; not polled",
				r.Name, r.Model)
			continue
		}
		if r.SlaveID < 1 || r.SlaveID > 247 {
			log.Printf("[power] %s has slave id %d, outside 1-247; not polled", r.Name, r.SlaveID)
			continue
		}
		out = append(out, power.Unit{ID: r.ID, Model: mdl,
			Addr: net.JoinHostPort(r.Host, strconv.Itoa(r.Port)), Slave: byte(r.SlaveID)})
	}
	return out
}

// powerShutdown stops the pollers and writes the minute in progress. Before
// the database closes.
func (s *Server) powerShutdown() {
	s.power.mu.Lock()
	m := s.power.manager
	s.power.manager = nil
	s.power.mu.Unlock()
	if m != nil {
		m.Stop()
	}
}

// powerSettings reads the three Power/UPS settings, each clamped to the bounds
// the settings write accepts (settings_write_tables.json), with the defaults
// for anything absent or unreadable.
func (s *Server) powerSettings() power.Settings {
	set := power.DefaultSettings()
	if s.store == nil {
		return set
	}
	cfg, err := s.mergedSettings()
	if err != nil {
		return set
	}
	num := func(key string, def, lo, hi float64) float64 {
		v, ok := cfg[key].(float64)
		if !ok {
			if i, isInt := cfg[key].(int); isInt {
				v, ok = float64(i), true
			}
		}
		if !ok || v < lo || v > hi {
			return def
		}
		return v
	}
	set.Interval = time.Duration(num("powerPollSec", set.Interval.Seconds(), 2, 300)) * time.Second
	set.OfflineAfter = int(num("powerOfflineAfter", float64(set.OfflineAfter), 1, 20))
	set.BatteryLowPct = num("powerBatteryLowPct", set.BatteryLowPct, 5, 90)
	return set
}

// powerApplySettings is called after every settings save: the pollers take
// new values at once, and are left alone when nothing of theirs changed.
func (s *Server) powerApplySettings() {
	set := s.powerSettings()
	s.power.mu.Lock()
	m := s.power.manager
	same := s.power.applied == set
	s.power.applied = set
	s.power.mu.Unlock()
	if m != nil && !same {
		m.SetSettings(set)
	}
}
