package power

import (
	"fmt"
	"strings"
	"testing"

	"mikrodash/internal/power/model"
)

// The verified on-mains reading from docs/inverter/powerguard-register-map.md.
// Each test edits a copy: 032 is the status register, 035 the event code,
// 009 battery %.
var onMains = []uint16{
	2205, 500, 2200, 500, 0, 0, 0, 138, 0, 100,
	0, 0, 0, 250, 250, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 105, 122,
	0, 0, 259, 0, 0, 0,
}

const (
	statusMains   = 259 // mains + charger + output on
	statusBattery = 260 // inverter + output on
	statusOff     = 0
)

// reading decodes onMains with register edits applied, as "reg=value" pairs.
func reading(t *testing.T, edits map[int]uint16) model.Reading {
	t.Helper()
	m := model.ByID("powerguard/modbus-v1.1")
	if m == nil {
		t.Fatal("the PowerGuard definition did not load")
	}
	regs := append([]uint16(nil), onMains...)
	for reg, v := range edits {
		regs[reg] = v
	}
	r, err := m.Decode([][]uint16{regs})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func battery(pct uint16) map[int]uint16 { return map[int]uint16{32: statusBattery, 0: 0, 9: pct} }

// show renders changes compactly so a whole sequence reads as one string:
// "+mains_lost@1000" began, "-mains_lost@5000(1000)" ended, "~" initial.
func show(cs []Change) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		name := string(c.Kind)
		if c.Code != 0 {
			name += fmt.Sprintf(":%d", c.Code)
		}
		switch {
		case c.Began && c.Initial:
			parts = append(parts, fmt.Sprintf("~%s@%d", name, c.At))
		case c.Began:
			parts = append(parts, fmt.Sprintf("+%s@%d", name, c.At))
		default:
			parts = append(parts, fmt.Sprintf("-%s@%d(%d)", name, c.At, c.Since))
		}
	}
	return strings.Join(parts, " ")
}

func expect(t *testing.T, step string, got []Change, want string) {
	t.Helper()
	if s := show(got); s != want {
		t.Errorf("%s: got %q, want %q", step, s, want)
	}
}

func TestStartingOnMainsReportsNothing(t *testing.T) {
	tr := NewTracker()
	expect(t, "first reading", tr.Success(reading(t, nil), 1000), "")
	if !tr.Online() {
		t.Error("not online after a good reading")
	}
}

func TestStartingOnBatteryIsReportedAsInitial(t *testing.T) {
	tr := NewTracker()
	expect(t, "first reading", tr.Success(reading(t, battery(80)), 1000), "~mains_lost@1000")
}

func TestAnOutageBeginsAndEndsWithItsDuration(t *testing.T) {
	tr := NewTracker()
	tr.Success(reading(t, nil), 1000)
	expect(t, "mains lost", tr.Success(reading(t, battery(90)), 6000), "+mains_lost@6000")
	expect(t, "still out", tr.Success(reading(t, battery(88)), 11000), "")
	expect(t, "restored", tr.Success(reading(t, nil), 5_000_000), "-mains_lost@5000000(6000)")
}

func TestAnEventCodeIsRaisedClearedAndReplaced(t *testing.T) {
	tr := NewTracker()
	tr.Success(reading(t, nil), 1000)
	expect(t, "overload", tr.Success(reading(t, map[int]uint16{35: 3}), 2000), "+event:3@2000")
	// Straight from one code to another: the first ends, the second begins.
	expect(t, "03 -> 06", tr.Success(reading(t, map[int]uint16{35: 6}), 3000), "-event:3@3000(2000) +event:6@3000")
	expect(t, "cleared", tr.Success(reading(t, nil), 4000), "-event:6@4000(3000)")
}

func TestFaultIsCarriedOnTheEvent(t *testing.T) {
	tr := NewTracker()
	tr.Success(reading(t, nil), 1000)
	eco := tr.Success(reading(t, map[int]uint16{35: 9}), 2000)
	if len(eco) != 1 || eco[0].Fault || eco[0].Text != "ECO starts" {
		t.Errorf("ECO: %+v, want one non-fault event", eco)
	}
	trip := tr.Success(reading(t, map[int]uint16{35: 3}), 3000)
	if len(trip) != 2 || !trip[1].Fault || trip[1].Text != "Output overload protection" {
		t.Errorf("overload: %+v, want the beginning to be a fault", trip)
	}
}

func TestOutputOff(t *testing.T) {
	tr := NewTracker()
	tr.Success(reading(t, nil), 1000)
	// Status 1: mains normal, output off.
	expect(t, "off", tr.Success(reading(t, map[int]uint16{32: 1}), 2000), "+output_off@2000")
	expect(t, "on", tr.Success(reading(t, nil), 3000), "-output_off@3000(2000)")
}

func TestBatteryLowOnlyCountsOnBattery(t *testing.T) {
	tr := NewTracker()
	tr.Success(reading(t, nil), 1000)
	// On mains the percentage is the charger's voltage talking: ignored.
	expect(t, "mains at 10 %", tr.Success(reading(t, map[int]uint16{9: 10}), 2000), "")
	expect(t, "battery at 21 %", tr.Success(reading(t, battery(21)), 3000), "+mains_lost@3000")
	expect(t, "20 % is low", tr.Success(reading(t, battery(20)), 4000), "+battery_low@4000")
	// Wobbling just above the threshold does not end it...
	expect(t, "22 %", tr.Success(reading(t, battery(22)), 5000), "")
	expect(t, "24 %", tr.Success(reading(t, battery(24)), 6000), "")
	// ...climbing clear of it does.
	expect(t, "25 %", tr.Success(reading(t, battery(25)), 7000), "-battery_low@7000(4000)")
	expect(t, "18 %", tr.Success(reading(t, battery(18)), 8000), "+battery_low@8000")
	// Mains returning ends it too, whatever the percentage says.
	expect(t, "mains back", tr.Success(reading(t, map[int]uint16{9: 18}), 9000),
		"-battery_low@9000(8000) -mains_lost@9000(3000)")
}

func TestTheThresholdIsTheTrackersSetting(t *testing.T) {
	tr := NewTracker()
	tr.BatteryLowPct = 40
	tr.Success(reading(t, nil), 1000)
	expect(t, "40 %", tr.Success(reading(t, battery(40)), 2000), "+battery_low@2000 +mains_lost@2000")
}

func TestNotRespondingAfterThreeFailuresDatedToTheFirst(t *testing.T) {
	tr := NewTracker()
	tr.Success(reading(t, nil), 1000)
	expect(t, "fail 1", tr.Failure(6000), "")
	expect(t, "fail 2", tr.Failure(11000), "")
	if !tr.Online() {
		t.Error("offline after two failures")
	}
	expect(t, "fail 3", tr.Failure(16000), "+not_responding@6000")
	if tr.Online() {
		t.Error("online after three failures")
	}
	expect(t, "fail 4", tr.Failure(21000), "")
	expect(t, "back", tr.Success(reading(t, nil), 26000), "-not_responding@26000(6000)")
	if !tr.Online() {
		t.Error("not online after answering again")
	}
}

func TestAFailureBetweenGoodPollsResetsTheCount(t *testing.T) {
	tr := NewTracker()
	tr.Success(reading(t, nil), 1000)
	tr.Failure(2000)
	tr.Failure(3000)
	tr.Success(reading(t, nil), 4000)
	tr.Failure(5000)
	expect(t, "two more failures", tr.Failure(6000), "")
	expect(t, "third in a row", tr.Failure(7000), "+not_responding@5000")
}

func TestAUnitThatNeverAnsweredIsInitiallyNotResponding(t *testing.T) {
	tr := NewTracker()
	tr.Failure(1000)
	tr.Failure(2000)
	expect(t, "third", tr.Failure(3000), "~not_responding@1000")
}

func TestWhatHappenedDuringSilenceIsSettledByTheNextReading(t *testing.T) {
	tr := NewTracker()
	tr.Success(reading(t, battery(70)), 1000)
	for ts := int64(2000); ts <= 4000; ts += 1000 {
		tr.Failure(ts)
	}
	// Mains came back while nobody could see: it ends at the first reading
	// that shows it, alongside the silence.
	expect(t, "back on mains", tr.Success(reading(t, nil), 9000),
		"-mains_lost@9000(1000) -not_responding@9000(2000)")
}

func TestUndocumentedBitsAreReportedByValue(t *testing.T) {
	tr := NewTracker()
	tr.Success(reading(t, nil), 1000)
	expect(t, "warning 4", tr.Success(reading(t, map[int]uint16{33: 4}), 2000), "+warning_bits:4@2000")
	expect(t, "warning 6, error 1", tr.Success(reading(t, map[int]uint16{33: 6, 34: 1}), 3000),
		"-warning_bits:4@3000(2000) +error_bits:1@3000 +warning_bits:6@3000")
	expect(t, "clear", tr.Success(reading(t, nil), 4000),
		"-error_bits:1@4000(3000) -warning_bits:6@4000(3000)")
}

func TestOpenListsWhatIsTrueNow(t *testing.T) {
	tr := NewTracker()
	tr.Success(reading(t, nil), 1000)
	tr.Success(reading(t, battery(15)), 2000)
	if got := show(tr.Open()); got != "+battery_low@2000 +mains_lost@2000" {
		t.Errorf("open: %q", got)
	}
}

func TestARestoredOutageCarriesOnOrEnds(t *testing.T) {
	restored := []Change{{Cond: Cond{Kind: KindMainsLost, Text: "Mains lost"}, Began: true, At: 500}}

	still := NewTracker()
	still.Restore(restored)
	// Still on battery after the restart: nothing begins again, and the
	// outage keeps its original start.
	expect(t, "still out", still.Success(reading(t, battery(60)), 9000), "")
	if got := show(still.Open()); got != "+mains_lost@500" {
		t.Errorf("open %q, want the outage from 500", got)
	}

	over := NewTracker()
	over.Restore(restored)
	expect(t, "ended while stopped", over.Success(reading(t, nil), 9000), "-mains_lost@9000(500)")
}
