package server

import (
	"net/http"
	"testing"

	"mikrodash/internal/db"
)

func TestAPowerReportClipsOutagesToItsWindow(t *testing.T) {
	at := func(v int64) *int64 { return &v }
	events := []db.PowerEvent{
		{Kind: "mains_lost", BeganAt: 500, EndedAt: at(1500)},  // began before: 500 ms counted
		{Kind: "mains_lost", BeganAt: 1600, EndedAt: at(1700)}, // inside: 100 ms
		{Kind: "mains_lost", BeganAt: 1800},                    // still out: runs to the end, 200 ms
		{Kind: "event", Code: 3, Fault: true, BeganAt: 900},    // a fault from before the window
		{Kind: "event", Code: 3, Fault: true, BeganAt: 1100},
		{Kind: "event", Code: 9, BeganAt: 1200}, // not a fault
		{Kind: "not_responding", BeganAt: 1300, EndedAt: at(1400)},
	}
	mins := []db.PowerMinuteRow{{Polls: 12, OK: 12}, {Polls: 12, OK: 7}}
	rep := buildPowerReport(1000, 2000, events, mins, map[string]db.PowerPoint{})
	if rep.Outages != 3 || rep.OutageMs != 800 || rep.LongestMs != 500 {
		t.Errorf("outages %d, %d ms, longest %d ms; want 3, 800, 500", rep.Outages, rep.OutageMs, rep.LongestMs)
	}
	if rep.Faults != 1 || rep.NotResponding != 1 {
		t.Errorf("faults %d, not responding %d; want 1 and 1", rep.Faults, rep.NotResponding)
	}
	if rep.Availability != 79.1 { // 19 of 24, cut to one decimal
		t.Errorf("availability %v, want 79.1", rep.Availability)
	}
	if len(rep.Events) != len(events) {
		t.Errorf("listed %d events, want %d", len(rep.Events), len(events))
	}
	if empty := buildPowerReport(0, 1, nil, nil, nil); empty.Availability != -1 || empty.Events == nil {
		t.Errorf("with no polls: %+v; want availability -1 and an empty list", empty)
	}
}

func TestThePowerReportIsGatedBySiteAndReadsTheWindow(t *testing.T) {
	p := newPowerAPI(t)
	p.grant("pw-view@site-1")
	mk := func(name, site string) db.PowerUnit {
		u, err := p.d.CreatePowerUnit(db.PowerUnit{Name: name, SiteID: &site, Model: "powerguard/modbus-v1.1",
			Host: "198.51.100.10", Port: 502, SlaveID: 1, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	mine, other := mk("INV-01", "site-1"), mk("INV-02", "site-2")
	if err := p.d.RecordPowerMinute(mine.ID, 120_000, 12, 9, 100,
		[]db.PowerStat{{Key: "battery_pct", Avg: 80, Min: 60, Max: 100}}); err != nil {
		t.Fatal(err)
	}
	if err := p.d.BeginPowerEvent(db.PowerEvent{UnitID: mine.ID, Kind: "mains_lost", Text: "Mains lost",
		BeganAt: 150_000}); err != nil {
		t.Fatal(err)
	}
	code, body := p.do("GET", "/api/power/units/"+mine.ID+"/report?from=60000&to=180000", "")
	if code != http.StatusOK {
		t.Fatalf("report: %d %v", code, body)
	}
	rep := body["report"].(map[string]any)
	if rep["outageMs"] != float64(30_000) || rep["availability"] != float64(75) {
		t.Errorf("report %v", rep)
	}
	if b := rep["stats"].(map[string]any)["battery_pct"].(map[string]any); b["min"] != float64(60) {
		t.Errorf("battery stats %v", b)
	}
	if code, _ := p.do("GET", "/api/power/units/"+mine.ID+"/report?from=5&to=5", ""); code != http.StatusBadRequest {
		t.Errorf("an empty window: %d, want 400", code)
	}
	if code, _ := p.do("GET", "/api/power/units/"+other.ID+"/report", ""); code != http.StatusNotFound {
		t.Errorf("another site's unit: %d, want 404", code)
	}
}
