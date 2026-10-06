package model

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
)

// The verified readings in docs/inverter/powerguard-register-map.md, registers
// 000-035 as read from a real unit on 2026-10-02.
var (
	vecOnBattery = []uint16{
		0, 0, 2205, 505, 0, 0, 0, 118, 0, 47,
		0, 0, 0, 250, 250, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 105, 122,
		0, 0, 260, 0, 0, 0,
	}
	vecOnMains = []uint16{
		2205, 500, 2200, 500, 0, 0, 0, 138, 0, 100,
		0, 0, 0, 250, 250, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 105, 122,
		0, 0, 259, 0, 0, 0,
	}
	vecAfterOutage = []uint16{
		2230, 500, 2200, 500, 0, 0, 0, 138, 0, 100,
		0, 0, 0, 250, 250, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 105, 122,
		0, 0, 259, 0, 0, 0,
	}
)

func powerguard(t *testing.T) *Model {
	t.Helper()
	m := ByID("powerguard/modbus-v1.1")
	if m == nil {
		_, err := All()
		t.Fatalf("the PowerGuard definition did not load: %v", err)
	}
	return m
}

func decode(t *testing.T, m *Model, regs []uint16) Reading {
	t.Helper()
	r, err := m.Decode([][]uint16{regs})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEveryEmbeddedModelLoads(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no models embedded - the defs walk broke")
	}
}

func TestTheVerifiedReadingsDecodeAsDocumented(t *testing.T) {
	m := powerguard(t)
	cases := []struct {
		name  string
		regs  []uint16
		vals  map[string]float64
		flags map[string]bool
		mode  Mode
	}{
		{
			name: "on battery, no mains, no load", regs: vecOnBattery,
			vals: map[string]float64{
				"input_v": 0, "input_hz": 0, "output_v": 220.5, "output_hz": 50.5, "output_a": 0,
				"load_pct": 0, "battery_v": 11.8, "battery_pct": 47, "dc_bus_a": 0,
				"temp_internal": 25, "temp_ambient": 25,
			},
			flags: map[string]bool{"mains_ok": false, "charger_on": false, "inverter_on": true, "output_on": true},
			mode:  ModeBattery,
		},
		{
			name: "on mains, charging, no load", regs: vecOnMains,
			vals: map[string]float64{
				"input_v": 220.5, "input_hz": 50, "output_v": 220, "output_hz": 50,
				"battery_v": 13.8, "battery_pct": 100,
			},
			flags: map[string]bool{"mains_ok": true, "charger_on": true, "inverter_on": false, "output_on": true},
			mode:  ModeMains,
		},
		{
			name: "on mains after a short outage", regs: vecAfterOutage,
			vals:  map[string]float64{"input_v": 223, "output_v": 220},
			flags: map[string]bool{"mains_ok": true},
			mode:  ModeMains,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := decode(t, m, tc.regs)
			for k, want := range tc.vals {
				got, ok := r.Values[k]
				if !ok || got != want {
					t.Errorf("%s = %v (present %v), want %v", k, got, ok, want)
				}
			}
			for k, want := range tc.flags {
				if r.Flags[k] != want {
					t.Errorf("flag %s = %v, want %v", k, r.Flags[k], want)
				}
			}
			if r.Mode != tc.mode {
				t.Errorf("mode %s, want %s", r.Mode, tc.mode)
			}
			// The outage left no trace: mains loss is not an event code.
			if r.EventCode != 0 || r.EventText != "No event" {
				t.Errorf("event %d %q, want 0 \"No event\"", r.EventCode, r.EventText)
			}
			if r.Raw["warning_bits"] != 0 || r.Raw["error_bits"] != 0 {
				t.Errorf("raw %v, want zeros", r.Raw)
			}
		})
	}
}

func TestApparentPowerIsOutputVoltsTimesAmps(t *testing.T) {
	m := powerguard(t)
	regs := append([]uint16(nil), vecOnMains...)
	regs[2], regs[4] = 2301, 126 // 230.1 V, 12.6 A
	r := decode(t, m, regs)
	if r.ApparentVA == nil || *r.ApparentVA != 2899 {
		t.Errorf("apparent power %v, want 2899 VA (230.1 x 12.6 = 2899.26)", r.ApparentVA)
	}
}

func TestTheEventCodeDecidesFault(t *testing.T) {
	m := powerguard(t)
	cases := []struct {
		code uint16
		mode Mode
		text string
	}{
		{3, ModeFault, "Output overload protection"},
		{6, ModeFault, "Low battery voltage protection"},
		// ECO starting is a notice, not a fault: the mode follows the flags.
		{9, ModeMains, "ECO starts"},
		// A code the manufacturer never listed is treated as a fault and said so.
		{12, ModeFault, "Unknown event 12"},
	}
	for _, tc := range cases {
		regs := append([]uint16(nil), vecOnMains...)
		regs[35] = tc.code
		r := decode(t, m, regs)
		if r.Mode != tc.mode || r.EventText != tc.text || r.EventCode != int(tc.code) {
			t.Errorf("code %d: mode %s %q, want %s %q", tc.code, r.Mode, r.EventText, tc.mode, tc.text)
		}
	}
}

// Both verified on-mains readings have the charger running too, so without this
// case a rule keyed on the charger bit would pass for the mains bit.
func TestMainsWithTheChargerIdleIsStillOnMains(t *testing.T) {
	m := powerguard(t)
	regs := append([]uint16(nil), vecOnMains...)
	regs[32] = 0x0101 // mains normal + output on, charger idle
	if r := decode(t, m, regs); r.Mode != ModeMains || r.Flags["charger_on"] {
		t.Errorf("mode %s charger %v, want mains with the charger idle", r.Mode, r.Flags["charger_on"])
	}
}

func TestOutputOffWhenNeitherMainsNorInverter(t *testing.T) {
	m := powerguard(t)
	regs := append([]uint16(nil), vecOnBattery...)
	regs[32] = 0
	if r := decode(t, m, regs); r.Mode != ModeOff || r.Flags["output_on"] {
		t.Errorf("status 0 decodes as mode %s, output_on %v", r.Mode, r.Flags["output_on"])
	}
}

func TestAShortReplyIsRefused(t *testing.T) {
	m := powerguard(t)
	if _, err := m.Decode([][]uint16{vecOnMains[:35]}); err == nil {
		t.Error("35 registers were accepted for a 36-register read")
	}
	if _, err := m.Decode(nil); err == nil {
		t.Error("no replies were accepted")
	}
}

func TestSignedFieldsAreTwosComplement(t *testing.T) {
	def := mutate(t, func(d map[string]any) {
		fields := d["fields"].([]any)
		fields[len(fields)-1].(map[string]any)["signed"] = true // temp_ambient, reg 14
	})
	m, err := parse("signed.json", def)
	if err != nil {
		t.Fatal(err)
	}
	regs := append([]uint16(nil), vecOnMains...)
	regs[14] = 0xFF9C // -100 -> -10.0 °C
	r, _ := m.Decode([][]uint16{regs})
	if r.Values["temp_ambient"] != -10 {
		t.Errorf("temp_ambient %v, want -10", r.Values["temp_ambient"])
	}
}

// mutate returns the real PowerGuard definition with one change applied.
func mutate(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	b, err := defs.ReadFile("defs/powerguard/modbus-v1.1.json")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	change(d)
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func field(d map[string]any, i int) map[string]any { return d["fields"].([]any)[i].(map[string]any) }

func TestABrokenDefinitionIsRefused(t *testing.T) {
	cases := map[string]func(map[string]any){
		"a write function":           func(d map[string]any) { d["reads"].([]any)[0].(map[string]any)["function"] = 6 },
		"a read of 126 registers":    func(d map[string]any) { d["reads"].([]any)[0].(map[string]any)["count"] = 126 },
		"a register outside reads":   func(d map[string]any) { field(d, 0)["reg"] = 36 },
		"an unknown measure":         func(d map[string]any) { field(d, 0)["key"] = "input_volts" },
		"a duplicate measure":        func(d map[string]any) { field(d, 1)["key"] = "input_v" },
		"a negative divisor":         func(d map[string]any) { field(d, 0)["div"] = -10 },
		"a misspelt property":        func(d map[string]any) { field(d, 0)["divide"] = 10 },
		"an unknown flag":            func(d map[string]any) { d["flags"].(map[string]any)["bits"].(map[string]any)["eco"] = 3 },
		"a bit past 15":              func(d map[string]any) { d["flags"].(map[string]any)["bits"].(map[string]any)["output_on"] = 16 },
		"no mains flag":              func(d map[string]any) { delete(d["flags"].(map[string]any)["bits"].(map[string]any), "mains_ok") },
		"an unknown raw register":    func(d map[string]any) { d["raw"].([]any)[0].(map[string]any)["key"] = "alarm_bits" },
		"an event code that is text": func(d map[string]any) { d["event"].(map[string]any)["codes"].(map[string]any)["x"] = "?" },
		"no not-fault codes":         func(d map[string]any) { d["event"].(map[string]any)["notFault"] = []any{} },
		"an unknown kind":            func(d map[string]any) { d["kind"] = "generator" },
		"no producer name":           func(d map[string]any) { d["producerName"] = "" },
		"an ambiguous function": func(d map[string]any) {
			d["reads"] = append(d["reads"].([]any), map[string]any{"function": 3, "start": 0, "count": 10})
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parse("broken.json", mutate(t, change)); err == nil {
				t.Error("accepted")
			}
		})
	}
	// The unchanged definition must pass, or every case above passes for the
	// wrong reason.
	if _, err := parse("ok.json", mutate(t, func(map[string]any) {})); err != nil {
		t.Fatalf("the unchanged definition is refused: %v", err)
	}
}

func TestAFileMustLiveWhereItsIDSays(t *testing.T) {
	good := mutate(t, func(map[string]any) {})
	fsys := fstest.MapFS{"defs/other/modbus-v1.1.json": {Data: good}}
	if _, err := load(fsys, "defs"); err == nil || !strings.Contains(err.Error(), "belongs at") {
		t.Errorf("a misplaced definition loaded: %v", err)
	}
	fsys = fstest.MapFS{"defs/powerguard/modbus-v1.1.json": {Data: good}}
	if all, err := load(fsys, "defs"); err != nil || len(all) != 1 {
		t.Errorf("a correctly placed definition: %v %v", all, err)
	}
}

// A PRODUCT READS WITH THE MAP IT USES: the same registers, so the verified
// readings decode the same through HP-10212 as through the map, under its own
// name and with its details.
func TestAProductReadsWithItsMap(t *testing.T) {
	hp := ByID("powerguard/hp-10212")
	if hp == nil {
		_, err := All()
		t.Fatalf("HP-10212 did not load: %v", err)
	}
	if hp.ModelName != "HP-10212" || len(hp.Details) == 0 || hp.Serial != "9600 8N1" {
		t.Errorf("name, details or serial lost: %+v", hp)
	}
	for _, regs := range [][]uint16{vecOnBattery, vecOnMains} {
		a, b := decode(t, hp, regs), decode(t, powerguard(t), regs)
		if a.Mode != b.Mode || a.Values["battery_v"] != b.Values["battery_v"] || a.EventText != b.EventText {
			t.Errorf("HP-10212 reads %+v, the map %+v", a, b)
		}
	}
}

func TestAProductMustUseARealMapAndNoRegistersOfItsOwn(t *testing.T) {
	product := func(over map[string]any) []byte {
		d := map[string]any{"producer": "powerguard", "producerName": "PowerGuard", "model": "p1",
			"modelName": "P1", "kind": "inverter", "uses": "modbus-v1.1"}
		for k, v := range over {
			d[k] = v
		}
		b, _ := json.Marshal(d)
		return b
	}
	mapFile := mutate(t, func(map[string]any) {})
	cases := map[string]fstest.MapFS{
		"a map that is not there": {"defs/powerguard/p1.json": {Data: product(map[string]any{"uses": "nope"})}},
		"a map of another brand": {
			"defs/powerguard/p1.json":     {Data: product(nil)},
			"defs/other/modbus-v1.1.json": {Data: mutate(t, func(d map[string]any) { d["producer"] = "other" })},
		},
		"a map that uses another": {
			"defs/powerguard/modbus-v1.1.json": {Data: mapFile},
			"defs/powerguard/p1.json":          {Data: product(nil)},
			"defs/powerguard/p2.json":          {Data: product(map[string]any{"model": "p2", "uses": "p1"})},
		},
		"registers of its own": {
			"defs/powerguard/modbus-v1.1.json": {Data: mapFile},
			"defs/powerguard/p1.json": {Data: product(map[string]any{
				"reads": []any{map[string]any{"function": 4, "start": 0, "count": 1}}})},
		},
		"an empty detail": {
			"defs/powerguard/modbus-v1.1.json": {Data: mapFile},
			"defs/powerguard/p1.json":          {Data: product(map[string]any{"details": []any{""}})},
		},
		"a detail too long": {
			"defs/powerguard/modbus-v1.1.json": {Data: mapFile},
			"defs/powerguard/p1.json":          {Data: product(map[string]any{"details": []any{strings.Repeat("x", 121)}})},
		},
	}
	for name, fsys := range cases {
		if _, err := load(fsys, "defs"); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	ok := fstest.MapFS{
		"defs/powerguard/modbus-v1.1.json": {Data: mapFile},
		"defs/powerguard/p1.json":          {Data: product(map[string]any{"details": []any{"1 kW"}})},
	}
	if all, err := load(ok, "defs"); err != nil || len(all) != 2 || len(all[1].Reads) == 0 {
		t.Errorf("a product using its map: %v %v", all, err)
	}
}
