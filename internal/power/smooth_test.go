package power

import (
	"testing"

	"mikrodash/internal/power/model"
)

func readingOf(kv map[string]float64) model.Reading {
	return model.Reading{Values: kv}
}

// A BATTERY SAGGING WITH EACH SWITCH OF THE LOAD SHOWS ITS AVERAGE LEVEL, the
// way a multimeter does, steady once the window is full even when the load is
// on exactly half the time (where a median flips the whole swing). The value
// that really jumps - the load - is left as read.
func TestASaggingBatteryShowsASteadyLevel(t *testing.T) {
	var s smoother
	var shown model.Reading
	for i := 0; i < 12; i++ {
		v := 12.2
		if i%2 == 1 {
			v = 11.0
		}
		raw := readingOf(map[string]float64{"battery_v": v, "load_pct": float64(10 * i)})
		shown = s.add(raw)
		if raw.Values["battery_v"] != v {
			t.Fatalf("the raw reading was changed: %v", raw.Values)
		}
		if shown.Values["load_pct"] != float64(10*i) {
			t.Errorf("load was smoothed: %v", shown.Values["load_pct"])
		}
		if i >= smoothN-1 && shown.Values["battery_v"] != 11.6 {
			t.Errorf("reading %d: shown %v V, want the steady 11.6", i, shown.Values["battery_v"])
		}
	}
	// A single odd reading moves it by a sixth of its size, not the whole.
	for i := 0; i < smoothN; i++ {
		s.add(readingOf(map[string]float64{"battery_v": 12.2}))
	}
	if v := s.add(readingOf(map[string]float64{"battery_v": 11.0})).Values["battery_v"]; v != 12.0 {
		t.Errorf("one dip to 11.0 V shows %v, want 12.0", v)
	}
	// A real change is followed: after a window of it, it is all that shows.
	for i := 0; i < smoothN; i++ {
		shown = s.add(readingOf(map[string]float64{"battery_v": 11.8}))
	}
	if shown.Values["battery_v"] != 11.8 {
		t.Errorf("after a window at 11.8 V, shown %v", shown.Values["battery_v"])
	}
	// A silence starts the window again.
	s.reset()
	if v := s.add(readingOf(map[string]float64{"battery_v": 13.6})).Values["battery_v"]; v != 13.6 {
		t.Errorf("after a reset, shown %v, want the new reading alone", v)
	}
}

// THE PAGE SEES THE STEADY VALUE, THE HISTORY EVERY DIP: the unit's state is
// smoothed, the minute it records keeps the raw lowest.
func TestShownSmoothedRecordedRaw(t *testing.T) {
	net, rec := newFakeNet(), newRecorder()
	m, clock := testManager(t, net, rec)
	u := Unit{ID: "u1", Model: pgModel(t), Addr: "198.51.100.10:502", Slave: 1}
	c := &gateway{client: &fakeClient{n: net, addr: u.Addr}}
	for _, raw := range []uint16{122, 110, 122, 110, 122} { // register 7: battery volts ×10
		net.set(u.Addr, 1, map[int]uint16{7: raw})
		m.pollUnit(c, u)
		*clock += 5000
	}
	if v := rec.stateOf("u1").Reading.Values["battery_v"]; v != 11.72 {
		t.Errorf("shown %v V, want the average 11.72", v)
	}
	*clock += 60_000 // the next minute hands the last one over
	m.pollUnit(c, u)
	rec.mu.Lock()
	mins := rec.minutes["u1"]
	rec.mu.Unlock()
	var low float64 = -1
	for _, mn := range mins {
		for _, st := range mn.Stats {
			if st.Key == "battery_v" && (low < 0 || st.Min < low) {
				low = st.Min
			}
		}
	}
	if low != 11 {
		t.Errorf("the history's lowest battery %v V, want the raw dip 11", low)
	}
}
