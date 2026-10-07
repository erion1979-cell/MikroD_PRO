package power

// The live values a cycling load makes jump, shown as the average of the last
// few readings.
//
// ── WHY ─────────────────────────────────────────────────────────────────────
//
// A load that switches on and off (a thermostat heater, a pump) makes a weak
// battery sag and recover with each switch, and the inverter's output dip with
// it. A poll every 5 s is a snapshot that lands on either side at random, so
// the page jumped 12.2 → 11.0 → 12.2 V where a multimeter, which averages,
// showed a steady value. The average of the last `smoothN` readings is what
// the multimeter shows: steady, and still following a real change within a
// few polls.
//
// THE AVERAGE, NOT THE MEDIAN. A median was tried first and is wrong here: a
// load on half the time fills the window with 3 dips and 2 not, then 2 and 3,
// and the median flips the whole swing between them. The average holds the
// level, and a single odd reading moves it by only 1/smoothN of its size.
//
// ── WHAT IS NOT SMOOTHED ────────────────────────────────────────────────────
//
// Only the values in `smoothKeys`: output and battery voltage, and the
// voltage-based battery %. Load and current jump because the load really does;
// they are left alone, as is the mains input voltage.
// The minute history is fed the RAW readings, so each minute's lowest and
// highest still record every dip - the early warning of a battery that sags
// under load - and the exports and reports with them. The smoothed reading is
// what the page shows, what the conditions (battery low) are decided on, and
// what is kept across a restart.
//
// A failed poll starts the window again: after a silence the old readings
// say nothing about now.

import (
	"math"

	"mikrodash/internal/power/model"
)

// smoothN is how many readings the average spans: 6 polls, 30 s at the
// default interval.
const smoothN = 6

// smoothKeys are the measures shown smoothed.
var smoothKeys = []string{"output_v", "battery_v", "battery_pct"}

// smoother holds one unit's recent values of the smoothed measures.
type smoother struct {
	recent map[string][]float64
}

// add records a reading and returns a copy whose smoothed measures are the
// average of the window, to two decimals; the reading passed in is not changed.
func (s *smoother) add(r model.Reading) model.Reading {
	if s.recent == nil {
		s.recent = map[string][]float64{}
	}
	out := r
	out.Values = make(map[string]float64, len(r.Values))
	for k, v := range r.Values {
		out.Values[k] = v
	}
	for _, k := range smoothKeys {
		v, has := r.Values[k]
		if !has {
			continue
		}
		w := append(s.recent[k], v)
		if len(w) > smoothN {
			w = w[len(w)-smoothN:]
		}
		s.recent[k] = w
		out.Values[k] = mean(w)
	}
	return out
}

// reset forgets the window.
func (s *smoother) reset() { s.recent = nil }

// mean of a non-empty list, to two decimals.
func mean(xs []float64) float64 {
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return math.Round(sum/float64(len(xs))*100) / 100
}
