// Package model holds the Power/UPS model definitions and the one decoder that
// reads them.
//
// ── A MODEL IS DATA, NOT CODE ───────────────────────────────────────────────
//
// Each supported model is one JSON file under defs/<producer>/, embedded in the
// binary. It says which registers to read and what each one means, mapped onto
// the fixed names in `Measures`, `Flags` and `RawKeys`. Everything downstream -
// the page, the alerts, the history - reads only those names, so supporting a
// new model is adding a file, never new polling code.
//
// ── EVERY DEFINITION IS CHECKED, AND A BAD ONE IS REFUSED ──────────────────
//
// `parse` refuses an unknown JSON key (a typo would otherwise silently drop a
// field), a register outside every read, a function other than 03/04, a name
// that is not in the catalogue, and a duplicate. `TestEveryEmbeddedModelLoads`
// loads the real files, so a broken definition fails the build's tests rather
// than a poll.
package model

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"sync"

	"mikrodash/internal/power/modbus"
)

// Measure is one named quantity a model may report.
type Measure struct {
	Key   string
	Label string
	Unit  string
}

// Measures is every quantity a definition may map a register onto.
var Measures = []Measure{
	{"input_v", "Input voltage", "V"},
	{"input_hz", "Input frequency", "Hz"},
	{"output_v", "Output voltage", "V"},
	{"output_hz", "Output frequency", "Hz"},
	{"output_a", "Output current", "A"},
	{"load_pct", "Output load", "%"},
	{"battery_v", "Battery voltage", "V"},
	{"battery_pct", "Battery capacity", "%"},
	{"dc_bus_a", "DC bus current", "A"},
	{"temp_internal", "Internal temperature", "°C"},
	{"temp_ambient", "Ambient temperature", "°C"},
}

// Flags is every status bit a definition may name.
var Flags = []string{"mains_ok", "charger_on", "inverter_on", "output_on"}

// RawKeys is every register a definition may pass through undecoded, for bit
// fields whose meaning the manufacturer has not documented yet.
var RawKeys = []string{"warning_bits", "error_bits"}

// Mode is what a unit is doing, derived the same way for every model.
type Mode string

const (
	ModeMains   Mode = "mains"
	ModeBattery Mode = "battery"
	ModeFault   Mode = "fault"
	ModeOff     Mode = "off"
)

// Read is one request a poll makes.
type Read struct {
	Function modbus.Function `json:"function"`
	Start    uint16          `json:"start"`
	Count    uint16          `json:"count"`
}

// Field maps one register onto a measure.
type Field struct {
	Key string `json:"key"`
	Reg uint16 `json:"reg"`
	// Function is needed only when the reads use both 03 and 04, where an
	// address alone does not say which table it is in.
	Function modbus.Function `json:"function,omitempty"`
	// Div divides the raw value: 10 for "0.1 V per unit". 0 or absent is 1.
	// A divisor rather than a 0.1 multiplier, because 2205/10 is 220.5 and
	// 2205*0.1 is 220.50000000000003.
	Div    int  `json:"div,omitempty"`
	Signed bool `json:"signed,omitempty"`
}

// RawReg passes one register through undecoded.
type RawReg struct {
	Key      string          `json:"key"`
	Reg      uint16          `json:"reg"`
	Function modbus.Function `json:"function,omitempty"`
}

// FlagReg names bits of one status register.
type FlagReg struct {
	Reg      uint16          `json:"reg"`
	Function modbus.Function `json:"function,omitempty"`
	Bits     map[string]uint `json:"bits"`
}

// EventReg is the register holding the current event code.
type EventReg struct {
	Reg      uint16          `json:"reg"`
	Function modbus.Function `json:"function,omitempty"`
	// NotFault lists the codes that do not put the unit in fault: "no event",
	// and notices such as ECO mode starting.
	NotFault []int             `json:"notFault"`
	Codes    map[string]string `json:"codes"`
}

// Model is one validated definition.
type Model struct {
	Producer     string   `json:"producer"`
	ProducerName string   `json:"producerName"`
	Model        string   `json:"model"`
	ModelName    string   `json:"modelName"`
	Kind         string   `json:"kind"`
	Serial       string   `json:"serial"`
	Reads        []Read   `json:"reads"`
	Fields       []Field  `json:"fields"`
	Flags        FlagReg  `json:"flags"`
	Raw          []RawReg `json:"raw"`
	Event        EventReg `json:"event"`
	codes        map[int]string
	notFault     map[int]bool
}

// ID is the model's stored identity: producer/model.
func (m *Model) ID() string { return m.Producer + "/" + m.Model }

// Reading is one decoded poll.
type Reading struct {
	// Values holds every measure the model reports, by key.
	Values map[string]float64
	Flags  map[string]bool
	Raw    map[string]uint16
	// ApparentVA is output voltage times output current: CALCULATED, not read
	// from a register, and absent when the model reports either one not at all.
	ApparentVA *float64
	EventCode  int
	EventText  string
	Mode       Mode
}

// Decode turns one poll's registers into a Reading. regs[i] is the reply to
// m.Reads[i].
func (m *Model) Decode(regs [][]uint16) (Reading, error) {
	if len(regs) != len(m.Reads) {
		return Reading{}, fmt.Errorf("%s: %d replies for %d reads", m.ID(), len(regs), len(m.Reads))
	}
	for i, r := range m.Reads {
		if len(regs[i]) != int(r.Count) {
			return Reading{}, fmt.Errorf("%s: read %d returned %d registers, want %d",
				m.ID(), i, len(regs[i]), r.Count)
		}
	}
	// Every address was checked against a read in parse, so `at` cannot miss.
	at := func(fn modbus.Function, reg uint16) uint16 {
		i, off, _ := m.locate(fn, reg)
		return regs[i][off]
	}

	out := Reading{Values: map[string]float64{}, Flags: map[string]bool{}, Raw: map[string]uint16{}}
	for _, f := range m.Fields {
		raw := at(f.Function, f.Reg)
		v := float64(raw)
		if f.Signed {
			v = float64(int16(raw))
		}
		out.Values[f.Key] = v / float64(f.Div)
	}
	status := at(m.Flags.Function, m.Flags.Reg)
	for name, bit := range m.Flags.Bits {
		out.Flags[name] = status>>bit&1 == 1
	}
	for _, r := range m.Raw {
		out.Raw[r.Key] = at(r.Function, r.Reg)
	}
	if v, okV := out.Values["output_v"]; okV {
		if a, okA := out.Values["output_a"]; okA {
			va := float64(int64(v*a + 0.5))
			out.ApparentVA = &va
		}
	}

	out.EventCode = int(at(m.Event.Function, m.Event.Reg))
	if text, ok := m.codes[out.EventCode]; ok {
		out.EventText = text
	} else {
		out.EventText = "Unknown event " + strconv.Itoa(out.EventCode)
	}
	switch {
	case !m.notFault[out.EventCode]:
		out.Mode = ModeFault
	case out.Flags["mains_ok"]:
		out.Mode = ModeMains
	case out.Flags["inverter_on"]:
		out.Mode = ModeBattery
	default:
		out.Mode = ModeOff
	}
	return out, nil
}

// locate finds which read holds (fn, reg) and where in its reply.
func (m *Model) locate(fn modbus.Function, reg uint16) (read, offset int, ok bool) {
	for i, r := range m.Reads {
		if r.Function == fn && reg >= r.Start && uint32(reg) < uint32(r.Start)+uint32(r.Count) {
			return i, int(reg - r.Start), true
		}
	}
	return 0, 0, false
}

// parse decodes and validates one definition. name is for messages only.
func parse(name string, b []byte) (*Model, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m Model
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	fail := func(format string, a ...any) (*Model, error) {
		return nil, fmt.Errorf("%s: "+format, append([]any{name}, a...)...)
	}
	if m.Producer == "" || m.Model == "" || m.ProducerName == "" || m.ModelName == "" {
		return fail("producer, producerName, model and modelName are all required")
	}
	if m.Kind != "inverter" && m.Kind != "ups" {
		return fail("kind %q: want inverter or ups", m.Kind)
	}
	if len(m.Reads) == 0 {
		return fail("no reads")
	}
	fns := map[modbus.Function]bool{}
	for i, r := range m.Reads {
		if r.Function != modbus.ReadHolding && r.Function != modbus.ReadInput {
			return fail("read %d: function %d is not 3 or 4; only reads are allowed", i, r.Function)
		}
		if r.Count == 0 || r.Count > modbus.MaxCount || uint32(r.Start)+uint32(r.Count) > 0x10000 {
			return fail("read %d: start %d count %d out of range", i, r.Start, r.Count)
		}
		fns[r.Function] = true
	}
	// An address with no function named belongs to the only function read.
	only := modbus.Function(0)
	if len(fns) == 1 {
		for f := range fns {
			only = f
		}
	}
	place := func(what string, fn *modbus.Function, reg uint16) error {
		if *fn == 0 {
			if only == 0 {
				return fmt.Errorf("%s: reads use both 03 and 04, so it must name its function", what)
			}
			*fn = only
		}
		if _, _, ok := m.locate(*fn, reg); !ok {
			return fmt.Errorf("%s: register %d (function %02d) is in no read", what, reg, *fn)
		}
		return nil
	}

	known := map[string]bool{}
	for _, ms := range Measures {
		known[ms.Key] = true
	}
	seen := map[string]bool{}
	for i := range m.Fields {
		f := &m.Fields[i]
		if !known[f.Key] {
			return fail("field %q is not a known measure", f.Key)
		}
		if seen[f.Key] {
			return fail("field %q appears twice", f.Key)
		}
		seen[f.Key] = true
		if f.Div == 0 {
			f.Div = 1
		}
		if f.Div < 0 {
			return fail("field %q: div %d", f.Key, f.Div)
		}
		if err := place("field "+f.Key, &f.Function, f.Reg); err != nil {
			return fail("%v", err)
		}
	}

	if err := place("flags", &m.Flags.Function, m.Flags.Reg); err != nil {
		return fail("%v", err)
	}
	knownFlag := map[string]bool{}
	for _, f := range Flags {
		knownFlag[f] = true
	}
	for name, bit := range m.Flags.Bits {
		if !knownFlag[name] {
			return fail("flag %q is not a known flag", name)
		}
		if bit > 15 {
			return fail("flag %q: bit %d of a 16-bit register", name, bit)
		}
	}
	// The mode rule reads these two; a model without them cannot say whether
	// it is on mains or on battery.
	for _, need := range []string{"mains_ok", "inverter_on"} {
		if _, ok := m.Flags.Bits[need]; !ok {
			return fail("flags must name %q", need)
		}
	}

	knownRaw := map[string]bool{}
	for _, k := range RawKeys {
		knownRaw[k] = true
	}
	for i := range m.Raw {
		r := &m.Raw[i]
		if !knownRaw[r.Key] || seen[r.Key] {
			return fail("raw %q is unknown or repeated", r.Key)
		}
		seen[r.Key] = true
		if err := place("raw "+r.Key, &r.Function, r.Reg); err != nil {
			return fail("%v", err)
		}
	}

	if err := place("event", &m.Event.Function, m.Event.Reg); err != nil {
		return fail("%v", err)
	}
	if len(m.Event.NotFault) == 0 {
		return fail("event.notFault must list at least the no-event code")
	}
	m.codes = map[int]string{}
	for k, text := range m.Event.Codes {
		code, err := strconv.Atoi(k)
		if err != nil || code < 0 || code > 0xFFFF {
			return fail("event code %q is not a register value", k)
		}
		m.codes[code] = text
	}
	m.notFault = map[int]bool{}
	for _, c := range m.Event.NotFault {
		m.notFault[c] = true
	}
	return &m, nil
}

// load reads every definition under root in fsys, sorted by ID.
func load(fsys fs.FS, root string) ([]*Model, error) {
	var out []*Model
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".json" {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		m, err := parse(p, b)
		if err != nil {
			return err
		}
		// The file's place must agree with what it says it is, so a copied
		// file left with the old producer cannot shadow the original. This is
		// also what makes IDs unique: one path, one ID.
		if want := path.Join(root, m.Producer, m.Model+".json"); p != want {
			return fmt.Errorf("%s: declares %s, so it belongs at %s", p, m.ID(), want)
		}
		out = append(out, m)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, err
}

//go:embed defs
var defs embed.FS

var (
	catOnce sync.Once
	catAll  []*Model
	catErr  error
)

// All is every embedded model, sorted by ID. The error is a broken definition,
// which `TestEveryEmbeddedModelLoads` keeps out of a release.
func All() ([]*Model, error) {
	catOnce.Do(func() { catAll, catErr = load(defs, "defs") })
	return catAll, catErr
}

// ByID finds one model, or nil.
func ByID(id string) *Model {
	all, _ := All()
	for _, m := range all {
		if m.ID() == id {
			return m
		}
	}
	return nil
}
