package db

import (
	"strings"
	"testing"
)

// powerSQL is the stored DDL for everything migrations 35 and 37 own.
func powerSQL(t *testing.T, d *DB) string {
	t.Helper()
	rows, err := d.sql.Query(`SELECT name, sql FROM sqlite_master
	    WHERE name LIKE 'power_%' OR name = 'idx_power_events_unit' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var name string
		var sql *string // an autoindex has no SQL
		if err := rows.Scan(&name, &sql); err != nil {
			t.Fatal(err)
		}
		b.WriteString(name + ": ")
		if sql != nil {
			b.WriteString(*sql)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// MIGRATIONS 35 AND 37 BUILD WHAT A FRESH DATABASE IS BORN WITH, and they can
// run twice. Winding back to before 35 replays 37 too, so power_last goes with
// the rest.
func TestMigrationThirtyFiveBuildsWhatAFreshDatabaseHas(t *testing.T) {
	d := openTest(t, t.TempDir())
	fresh := powerSQL(t, d)
	for _, want := range []string{"power_units", "power_minutes", "power_samples", "power_events",
		"idx_power_events_unit", "power_last"} {
		if !strings.Contains(fresh, want+": ") {
			t.Fatalf("a fresh database has no %s:\n%s", want, fresh)
		}
	}
	cfgExec(t, d,
		`DROP INDEX idx_power_events_unit`, `DROP TABLE power_last`,
		`DROP TABLE power_events`, `DROP TABLE power_samples`, `DROP TABLE power_minutes`,
		`DROP TABLE power_units`,
		`DELETE FROM schema_version WHERE version >= 35`)
	if got := powerSQL(t, d); got != "" {
		t.Fatalf("the wind-back left %s", got)
	}
	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := powerSQL(t, d); got != fresh {
		t.Errorf("migrated:\n%s\nfresh:\n%s", got, fresh)
	}
	cfgExec(t, d, `DELETE FROM schema_version WHERE version >= 35`)
	if _, err := d.Migrate(); err != nil {
		t.Errorf("migrations 35 and 37 failed on a database that already has the tables: %v", err)
	}
}

func newPowerUnit(t *testing.T, d *DB) PowerUnit {
	t.Helper()
	site := "site-a"
	u, err := d.CreatePowerUnit(PowerUnit{Name: "INV-01", SiteID: &site,
		Model: "powerguard/modbus-v1.1", Host: "198.51.100.10", Port: 502, SlaveID: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAPowerUnitRoundTripsWithItsNulls(t *testing.T) {
	d := openTest(t, t.TempDir())
	u := newPowerUnit(t, d)
	if u.ID == "" || u.CreatedAt == 0 {
		t.Fatalf("created %+v without an id or time", u)
	}
	all, err := d.PowerUnits()
	if err != nil || len(all) != 1 {
		t.Fatalf("%v %v", all, err)
	}
	got := all[0]
	if got.Name != "INV-01" || *got.SiteID != "site-a" || got.Port != 502 || got.SlaveID != 1 ||
		!got.Enabled || got.RouterID != nil || got.BatteryAh != nil {
		t.Errorf("read back %+v", got)
	}

	ah := 200.0
	got.Name, got.SiteID, got.BatteryAh, got.Enabled = "INV-02", nil, &ah, false
	if err := d.UpdatePowerUnit(got); err != nil {
		t.Fatal(err)
	}
	all, _ = d.PowerUnits()
	if all[0].Name != "INV-02" || all[0].SiteID != nil || *all[0].BatteryAh != 200 || all[0].Enabled {
		t.Errorf("after update %+v", all[0])
	}
	if err := d.UpdatePowerUnit(PowerUnit{ID: "missing"}); err != ErrPowerUnitNotFound {
		t.Errorf("updating a missing unit: %v", err)
	}
	if err := d.DeletePowerUnit("missing"); err != ErrPowerUnitNotFound {
		t.Errorf("deleting a missing unit: %v", err)
	}
}

func TestDeletingAUnitDeletesItsHistoryAndEvents(t *testing.T) {
	d := openTest(t, t.TempDir())
	u := newPowerUnit(t, d)
	if err := d.RecordPowerMinute(u.ID, 60_000, 12, 12, 140,
		[]PowerStat{{Key: "input_v", Avg: 230, Min: 228, Max: 231}}); err != nil {
		t.Fatal(err)
	}
	if err := d.BeginPowerEvent(PowerEvent{UnitID: u.ID, Kind: "mains_lost", Text: "Mains lost", BeganAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.DeletePowerUnit(u.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"power_minutes", "power_samples", "power_events"} {
		var n int
		if err := d.sql.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s kept %d row(s) of a deleted unit", table, n)
		}
	}
}

func TestAMinuteWrittenTwiceIsReplaced(t *testing.T) {
	d := openTest(t, t.TempDir())
	u := newPowerUnit(t, d)
	for _, avg := range []float64{230, 229} {
		if err := d.RecordPowerMinute(u.ID, 60_000, 6, 5, 150,
			[]PowerStat{{Key: "input_v", Avg: avg, Min: 1, Max: 2}}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	var avg float64
	if err := d.sql.QueryRow(`SELECT COUNT(*), MAX(avg) FROM power_samples`).Scan(&n, &avg); err != nil {
		t.Fatal(err)
	}
	if n != 1 || avg != 229 {
		t.Errorf("%d row(s), avg %v: want the one, later write", n, avg)
	}
}

func TestAnOpenEventIsNotOpenedTwiceAndClosesOnce(t *testing.T) {
	d := openTest(t, t.TempDir())
	u := newPowerUnit(t, d)
	begin := func(kind string, code int, at int64) {
		t.Helper()
		if err := d.BeginPowerEvent(PowerEvent{UnitID: u.ID, Kind: kind, Code: code, Text: kind, BeganAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	begin("mains_lost", 0, 1000)
	// A restart re-observes the same outage: the row keeps its true start.
	begin("mains_lost", 0, 9000)
	begin("event", 3, 2000)
	open, err := d.PowerEvents(u.ID, true, 10)
	if err != nil || len(open) != 2 {
		t.Fatalf("open %+v %v, want two", open, err)
	}
	if open[1].Kind != "mains_lost" || open[1].BeganAt != 1000 {
		t.Errorf("the outage %+v, want its original start 1000", open[1])
	}

	if err := d.EndPowerEvent(u.ID, "event", 3, 3000); err != nil {
		t.Fatal(err)
	}
	// Once closed, the same code can begin again as a new row.
	begin("event", 3, 4000)
	all, _ := d.PowerEvents(u.ID, false, 10)
	// Newest first: the second overload (open), the first (closed), the outage.
	if len(all) != 3 || all[0].EndedAt != nil || all[1].EndedAt == nil || all[2].EndedAt != nil {
		t.Fatalf("events %+v", all)
	}
	if ended := all[1]; ended.Code != 3 || ended.BeganAt != 2000 || *ended.EndedAt != 3000 {
		t.Errorf("the closed one %+v", ended)
	}
}

func TestPowerHistoryFoldsMinutesIntoBuckets(t *testing.T) {
	d := openTest(t, t.TempDir())
	u := newPowerUnit(t, d)
	// Twenty minutes of input voltage; a dip to 0 V in minute 13.
	for m := int64(0); m < 20; m++ {
		mn, av := 228.0, 230.0
		if m == 13 {
			mn, av = 0, 200
		}
		if err := d.RecordPowerMinute(u.ID, m*60_000, 12, 12, 100, []PowerStat{
			{Key: "input_v", Avg: av, Min: mn, Max: 232},
			{Key: "load_pct", Avg: 40, Min: 40, Max: 40},
		}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := d.PowerHistory(u.ID, []string{"input_v", "battery_pct"}, 0, 19*60_000, 600_000)
	if err != nil {
		t.Fatal(err)
	}
	in := h["input_v"]
	if len(in) != 2 || in[0].TS != 0 || in[1].TS != 600_000 {
		t.Fatalf("input_v buckets %+v, want two ten-minute buckets", in)
	}
	if in[0].Min != 228 || in[1].Min != 0 || in[1].Max != 232 || in[1].Avg != 227 {
		t.Errorf("buckets %+v: the dip must survive as the second bucket's minimum, avg (9x230+200)/10", in)
	}
	if _, asked := h["load_pct"]; asked {
		t.Error("a measure not asked for was returned")
	}
	if b, ok := h["battery_pct"]; !ok || b == nil || len(b) != 0 {
		t.Errorf("a measure with no rows is %v, want an empty list", b)
	}
	// The window bounds hold.
	h, _ = d.PowerHistory(u.ID, []string{"input_v"}, 600_000, 19*60_000, 600_000)
	if len(h["input_v"]) != 1 {
		t.Errorf("a window from minute 10 returned %d buckets", len(h["input_v"]))
	}
}

func TestPowerEventsInAWindowIncludeTheOnesOverlappingIt(t *testing.T) {
	d := openTest(t, t.TempDir())
	u := newPowerUnit(t, d)
	add := func(kind string, began int64, ended *int64) {
		t.Helper()
		if err := d.BeginPowerEvent(PowerEvent{UnitID: u.ID, Kind: kind, Text: kind, BeganAt: began}); err != nil {
			t.Fatal(err)
		}
		if ended != nil {
			if err := d.EndPowerEvent(u.ID, kind, 0, *ended); err != nil {
				t.Fatal(err)
			}
		}
	}
	at := func(v int64) *int64 { return &v }
	add("before", 100, at(200))    // over before the window
	add("straddle", 500, at(1500)) // began before, ended inside
	add("inside", 1200, at(1300))
	add("open", 1800, nil) // still going
	add("after", 3000, at(3100))
	got, err := d.PowerEventsIn(u.ID, 1000, 2000)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range got {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "straddle,inside,open" {
		t.Errorf("window 1000-2000 returned %v", kinds)
	}
	mins, _ := d.PowerMinutes(u.ID, 0, 1)
	if mins == nil {
		t.Error("no minutes is nil, not an empty list")
	}
}

func TestPowerStatsAreTheMeanOfAveragesAndTheExtremesOfExtremes(t *testing.T) {
	d := openTest(t, t.TempDir())
	u := newPowerUnit(t, d)
	for i, s := range []PowerStat{
		{Key: "input_v", Avg: 230, Min: 228, Max: 232},
		{Key: "input_v", Avg: 220, Min: 180, Max: 225},
		{Key: "input_v", Avg: 0, Min: 0, Max: 0}, // outside the window
	} {
		if err := d.RecordPowerMinute(u.ID, int64(i+1)*60_000, 12, 12, 100, []PowerStat{s,
			{Key: "load_pct", Avg: 40, Min: 30, Max: 50}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.PowerStats(u.ID, []string{"input_v", "battery_pct"}, 60_000, 120_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("stats %v: want input_v only (load_pct not asked, battery_pct has no rows)", got)
	}
	if p := got["input_v"]; p.Avg != 225 || p.Min != 180 || p.Max != 232 {
		t.Errorf("input_v %+v, want avg 225, min 180, max 232", p)
	}
}

// A UNIT'S LAST READING IS ONE ROW, replaced each time, gone with the unit, and
// never kept for a unit that no longer exists.
func TestAUnitsLastReadingIsKeptAndGoesWithIt(t *testing.T) {
	d := openTest(t, t.TempDir())
	if at, raw, err := d.PowerLast("nobody"); err != nil || at != 0 || raw != nil {
		t.Fatalf("no reading kept: %d %q %v", at, raw, err)
	}
	u := newPowerUnit(t, d)
	for _, at := range []int64{1000, 2000} {
		if err := d.SavePowerLast(u.ID, at, []byte(`{"n":`+strings.Repeat("1", int(at/1000))+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	if at, raw, err := d.PowerLast(u.ID); err != nil || at != 2000 || string(raw) != `{"n":11}` {
		t.Errorf("kept %d %q %v, want the second", at, raw, err)
	}
	if err := d.SavePowerLast("gone", 3000, []byte(`{}`)); err != nil {
		t.Errorf("keeping for a deleted unit: %v", err)
	}
	if at, _, _ := d.PowerLast("gone"); at != 0 {
		t.Error("a reading was kept for a unit that does not exist")
	}
	if err := d.DeletePowerUnit(u.ID); err != nil {
		t.Fatal(err)
	}
	if at, _, _ := d.PowerLast(u.ID); at != 0 {
		t.Error("the last reading outlived its unit")
	}
}
