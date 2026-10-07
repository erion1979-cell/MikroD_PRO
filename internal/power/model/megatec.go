package model

// A Megatec UPS's reading. The protocol fixes its fields, so a Megatec model
// file holds no registers and this file is its whole decoder.
//
// ── WHAT IS READ, AND WHAT IS INFERRED ──────────────────────────────────────
//
// Read from the UPS: input and output voltage, load %, input frequency,
// battery voltage, temperature, and the status bits - utility fail, battery
// low, bypass, UPS failed, standby/online, test, shutdown pending.
//
// Inferred, because Megatec does not report it (agreed 2026-10-07):
//
//   - battery_pct, ESTIMATED from the voltage per cell, linearly between
//     1.733 V (0 %) and 2.167 V (100 %): 104/120 and 130/120 of a 2 V cell,
//     the range Network UPS Tools guesses for this protocol. A charging
//     battery reads 100 %. The page says "estimated".
//   - charger_on: mains present and the UPS not failed.
//   - output_on: an output voltage of at least 50 V.
//   - inverter_on: online, always unless on bypass or failed; standby, only
//     while the utility has failed.
//
// Bit 5 means bypass only on an online UPS; on a standby one it is the voltage
// regulator boosting or bucking, which is normal operation, so it is not
// reported as bypass.
//
// ── THE BATTERY'S SIZE COMES FROM THE RATING ────────────────────────────────
//
// An online UPS reports volts per cell, a standby one the whole battery. The
// rating line (F) gives the battery's nominal voltage, so cells = nominal / 2.
// Without it, a per-cell report gives a percentage but no battery voltage,
// and a whole-battery report gives a voltage but no percentage.

import (
	"math"

	"mikrodash/internal/power/megatec"
)

const (
	cellEmpty = 2.0 * 104 / 120
	cellFull  = 2.0 * 130 / 120
	// outputOnV is the least output voltage counted as the output being on.
	outputOnV = 50
)

// The event codes a Megatec reading carries; the protocol has none of its own.
const (
	MegatecFailed   = 1
	MegatecShutdown = 2
	MegatecTest     = 3
)

// FromMegatec turns one status line, and the rating when the UPS gave one,
// into a Reading.
func (m *Model) FromMegatec(s megatec.Status, rating *megatec.Rating) Reading {
	out := Reading{Values: map[string]float64{}, Flags: map[string]bool{}, Raw: map[string]uint16{}}
	set := func(key string, v float64) {
		if !math.IsNaN(v) {
			out.Values[key] = v
		}
	}
	set("input_v", s.InputV)
	set("output_v", s.OutputV)
	set("load_pct", s.LoadPct)
	set("input_hz", s.InputHz)
	set("temp_internal", s.TempC)

	cells := 0.0
	if rating != nil && rating.BatteryV >= 3 {
		cells = math.Round(rating.BatteryV / 2)
	}
	cell := math.NaN()
	switch {
	case math.IsNaN(s.BatteryV):
	case s.PerCell():
		cell = s.BatteryV
		if cells > 0 {
			out.Values["battery_v"] = math.Round(s.BatteryV*cells*100) / 100
		}
	default:
		out.Values["battery_v"] = s.BatteryV
		if cells > 0 {
			cell = s.BatteryV / cells
		}
	}
	if !math.IsNaN(cell) {
		pct := (cell - cellEmpty) / (cellFull - cellEmpty) * 100
		out.Values["battery_pct"] = math.Round(math.Max(0, math.Min(100, pct)))
	}

	online := !s.Standby()
	bypass := online && s.BypassOrAVR()
	out.Flags["mains_ok"] = !s.UtilityFail()
	out.Flags["battery_low"] = s.BatteryLow()
	out.Flags["charger_on"] = !s.UtilityFail() && !s.Failed()
	if online {
		out.Flags["bypass"] = bypass
		out.Flags["inverter_on"] = !bypass && !s.Failed()
	} else {
		out.Flags["inverter_on"] = s.UtilityFail()
	}
	if !math.IsNaN(s.OutputV) {
		out.Flags["output_on"] = s.OutputV >= outputOnV
	}

	switch {
	case s.Failed():
		out.EventCode, out.EventText = MegatecFailed, "UPS failed"
	case s.ShutdownActive():
		out.EventCode, out.EventText = MegatecShutdown, "Shutdown pending"
	case s.Testing():
		out.EventCode, out.EventText = MegatecTest, "Battery test in progress"
	default:
		out.EventText = "No event"
	}
	switch {
	case s.Failed():
		out.Mode = ModeFault
	case out.Flags["mains_ok"]:
		out.Mode = ModeMains
	case out.Flags["inverter_on"]:
		out.Mode = ModeBattery
	default:
		out.Mode = ModeOff
	}
	return out
}
