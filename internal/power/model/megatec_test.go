package model

import (
	"math"
	"testing"

	"mikrodash/internal/power/megatec"
)

func megatecModel(t *testing.T) *Model {
	t.Helper()
	m := ByID("powerguard/megatec")
	if m == nil {
		_, err := All()
		t.Fatalf("the Megatec map did not load: %v", err)
	}
	if m.Protocol != "megatec" || m.Topology != "online" {
		t.Fatalf("protocol %q topology %q", m.Protocol, m.Topology)
	}
	return m
}

func status(t *testing.T, line string) megatec.Status {
	t.Helper()
	s, err := megatec.ParseStatus(line)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// AN ONLINE UPS ON MAINS: per-cell battery voltage times the cells the rating
// gives, a charging battery at 100 %, the inverter on, no bypass.
func TestAnOnlineUPSOnMains(t *testing.T) {
	rating := &megatec.Rating{Volts: 220, Amps: 4, BatteryV: 96, Hz: 50}
	r := megatecModel(t).FromMegatec(status(t, "(230.0 230.0 230.0 012 50.0 2.27 25.0 00000000"), rating)
	if r.Mode != ModeMains || r.EventCode != 0 || r.Topology != "online" {
		t.Errorf("mode %s event %d topology %q", r.Mode, r.EventCode, r.Topology)
	}
	if r.Values["battery_v"] != 108.96 || r.Values["battery_pct"] != 100 || r.Values["load_pct"] != 12 {
		t.Errorf("values %v", r.Values)
	}
	want := map[string]bool{"mains_ok": true, "charger_on": true, "inverter_on": true, "output_on": true,
		"bypass": false, "battery_low": false}
	for k, v := range want {
		if got, ok := r.Flags[k]; !ok || got != v {
			t.Errorf("flag %s = %v (present %v), want %v", k, got, ok, v)
		}
	}
}

func TestAnOnlineUPSOnBatteryAndOnBypass(t *testing.T) {
	m := megatecModel(t)
	r := m.FromMegatec(status(t, "(000.0 000.0 230.0 030 00.0 1.95 30.0 11000000"), nil)
	if r.Mode != ModeBattery || r.Flags["mains_ok"] || !r.Flags["inverter_on"] || r.Flags["charger_on"] || !r.Flags["battery_low"] {
		t.Errorf("on battery: mode %s flags %v", r.Mode, r.Flags)
	}
	// No rating: a percentage from the cell, but no battery voltage.
	if _, has := r.Values["battery_v"]; has || r.Values["battery_pct"] != 50 {
		t.Errorf("battery without a rating: %v", r.Values)
	}
	// On battery, a standby UPS's inverter runs.
	if r := m.FromMegatec(status(t, "(000.0 000.0 225.0 030 00.0 11.8 30.0 10001000"), nil); r.Mode != ModeBattery || !r.Flags["inverter_on"] {
		t.Errorf("standby on battery: mode %s flags %v", r.Mode, r.Flags)
	}
	r = m.FromMegatec(status(t, "(230.0 230.0 230.0 030 50.0 2.27 30.0 00100000"), nil)
	if r.Mode != ModeMains || !r.Flags["bypass"] || r.Flags["inverter_on"] {
		t.Errorf("on bypass: mode %s flags %v", r.Mode, r.Flags)
	}
}

// A STANDBY UPS: bit 5 is its voltage regulator, not a bypass, and its
// inverter runs only on battery.
func TestAStandbyUPSIsNotOnBypassWhileRegulating(t *testing.T) {
	r := megatecModel(t).FromMegatec(status(t, "(190.0 190.0 225.0 020 50.0 13.0 25.0 00101000"),
		&megatec.Rating{Volts: 230, BatteryV: 12, Hz: 50})
	if _, has := r.Flags["bypass"]; has || r.Flags["inverter_on"] || r.Mode != ModeMains || r.Topology != "offline" {
		t.Errorf("standby regulating: mode %s flags %v", r.Mode, r.Flags)
	}
	if r.Values["battery_v"] != 13 || r.Values["battery_pct"] != 100 {
		t.Errorf("standby battery %v", r.Values)
	}
}

func TestAFailedUPSIsAFault(t *testing.T) {
	r := megatecModel(t).FromMegatec(status(t, "(230.0 230.0 000.0 000 50.0 2.20 25.0 00010000"), nil)
	if r.Mode != ModeFault || r.EventCode != MegatecFailed || r.Flags["output_on"] || r.Flags["inverter_on"] {
		t.Errorf("failed: mode %s event %d flags %v", r.Mode, r.EventCode, r.Flags)
	}
	if _, has := r.Values["temp_internal"]; !has {
		t.Error("temperature lost")
	}
	r = megatecModel(t).FromMegatec(status(t, "(230.0 230.0 230.0 000 50.0 2.20 --.- 00000100"), nil)
	if r.Mode != ModeMains || r.EventCode != MegatecTest {
		t.Errorf("testing: mode %s event %d", r.Mode, r.EventCode)
	}
	if _, has := r.Values["temp_internal"]; has || math.IsNaN(r.Values["battery_pct"]) {
		t.Errorf("a dashed temperature was kept: %v", r.Values)
	}
}

func TestAMegatecModelIsNotDecodedAsRegisters(t *testing.T) {
	if _, err := megatecModel(t).Decode(nil); err == nil {
		t.Error("a Megatec model decoded Modbus registers")
	}
}
