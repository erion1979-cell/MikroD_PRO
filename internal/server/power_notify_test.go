package server

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/alertdispatch"
	"mikrodash/internal/db"
	"mikrodash/internal/power"
	"mikrodash/internal/power/model"
)

// recordingDoer stands in for the network: every notification a channel would
// send arrives here as an HTTP request.
type recordingDoer struct {
	mu   sync.Mutex
	sent []string
}

func (d *recordingDoer) Do(r *http.Request) (*http.Response, error) {
	body := ""
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}
	d.mu.Lock()
	d.sent = append(d.sent, r.URL.Host+r.URL.Path+" "+body)
	d.mu.Unlock()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
}

func (d *recordingDoer) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		d.mu.Lock()
		got := append([]string(nil), d.sent...)
		d.mu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// powerNotifyFixture is the Power/UPS API fixture with a unit on site 1, an
// enabled dispatcher sending through a recording client, and two channels: one
// install channel for mains events, one owned by the signed-in user for every
// Power/UPS event.
func powerNotifyFixture(t *testing.T) (*powerAPI, *recordingDoer, db.PowerUnit) {
	t.Helper()
	p := newPowerAPI(t)
	p.grant("administrator@global")
	doer := &recordingDoer{}
	p.srv.dispatch = alertdispatch.New(true, nil, doer, nil, func() int64 { return time.Now().UnixMilli() })
	for _, ch := range []string{
		`{"name":"site phones","kind":"webhook","enabled":true,"owner":"install",
		  "config":{"urls":["ntfy://ntfy.test/install"]},"events":["power_mains_lost"]}`,
		`{"name":"mine","kind":"webhook","enabled":true,
		  "config":{"urls":["ntfy://ntfy.test/user"]},
		  "events":["power_mains_lost","power_event","power_output_off","power_battery_low","power_not_responding"]}`,
	} {
		if code, body := p.do("POST", "/api/notify-channels", ch); code != http.StatusOK {
			t.Fatalf("creating a channel: %d %v", code, body)
		}
	}
	site := "site-1"
	u, err := p.d.CreatePowerUnit(db.PowerUnit{Name: "INV-01", SiteID: &site, Model: "powerguard/modbus-v1.1",
		Host: "198.51.100.10", Port: 502, SlaveID: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	p.srv.power.mu.Lock()
	p.srv.power.units = map[string]db.PowerUnit{u.ID: u}
	p.srv.power.mu.Unlock()
	return p, doer, u
}

func TestAPowerOutageIsSentToTheChannelsThatWantIt(t *testing.T) {
	p, doer, u := powerNotifyFixture(t)
	p.srv.dispatchPower(u.ID, []power.Change{
		{Cond: power.Cond{Kind: power.KindMainsLost, Text: "Mains lost"}, Began: true, At: 1000},
		// A kind an older build recorded (warning bits, from 033) is never sent.
		{Cond: power.Cond{Kind: "warning_bits", Code: 4, Text: "Warning bits 4"}, Began: true, At: 1000},
	}, nil)
	got := doer.wait(t, 2)
	if len(got) != 2 {
		t.Fatalf("sent %v, want the outage to both channels", got)
	}
	for _, m := range got {
		if !strings.Contains(m, "Mains lost on INV-01 (Site One): running on battery") {
			t.Errorf("message %q does not name the unit, its site and what happened", m)
		}
	}

	// An overload goes only to the channel subscribed to event codes.
	p.srv.dispatchPower(u.ID, []power.Change{
		{Cond: power.Cond{Kind: power.KindEvent, Code: 3, Text: "Output overload protection", Fault: true},
			At: 49_000, Since: 1000},
	}, nil)
	got = doer.wait(t, 3)
	if len(got) != 3 || !strings.Contains(got[2], "/user ") ||
		!strings.Contains(got[2], "Output overload protection (code 03) cleared after 48 s") {
		t.Errorf("after the overload cleared: %v", got)
	}
}

// A MESSAGE CARRIES THE READING THAT SHOWED THE CHANGE, appended to {{detail}}
// and as variables of its own; a Megatec UPS's battery % is marked estimated.
func TestAPowerMessageCarriesTheReading(t *testing.T) {
	r := &model.Reading{Values: map[string]float64{"input_v": 0, "output_v": 229.64, "load_pct": 41,
		"battery_pct": 85, "battery_v": 12.63}}
	f, _, _ := powerFired(power.Change{Cond: power.Cond{Kind: power.KindMainsLost}, Began: true}, r, false)
	want := "running on battery · input 0.0 V · output 229.6 V · load 41 % · battery 85 % (12.6 V)"
	if f.Detail != want {
		t.Errorf("detail %q, want %q", f.Detail, want)
	}
	if f.Vars["batteryPct"] != "85" || f.Vars["outputV"] != "229.6" || f.Vars["load"] != "41" || f.Vars["batteryV"] != "12.6" {
		t.Errorf("vars %v", f.Vars)
	}
	f, _, _ = powerFired(power.Change{Cond: power.Cond{Kind: power.KindBatteryLow, Text: "Battery low"}, Began: true},
		&model.Reading{Values: map[string]float64{"battery_pct": 18}}, true)
	if f.Detail != "Battery low · battery ~18 %" {
		t.Errorf("an estimated battery: %q", f.Detail)
	}

	// And through the dispatcher, to the channels.
	p, doer, u := powerNotifyFixture(t)
	p.srv.dispatchPower(u.ID, []power.Change{
		{Cond: power.Cond{Kind: power.KindMainsLost, Text: "Mains lost"}, Began: true, At: 1000},
	}, r)
	got := doer.wait(t, 2)
	if len(got) != 2 || !strings.Contains(got[0], "running on battery · input 0.0 V") {
		t.Errorf("sent %v, want the reading in the message", got)
	}
}

func TestAUsersChannelHearsOnlyAboutSitesTheyMayRead(t *testing.T) {
	p, doer, u := powerNotifyFixture(t)
	// The user's grant now covers site 2 only: their channel is silent about a
	// unit on site 1, while the install's channel still hears it.
	p.grant("pw-view@site-2")
	p.srv.dispatchPower(u.ID, []power.Change{
		{Cond: power.Cond{Kind: power.KindMainsLost, Text: "Mains lost"}, Began: true, At: 1000},
	}, nil)
	got := doer.wait(t, 2)
	if len(got) != 1 || !strings.Contains(got[0], "/install ") {
		t.Errorf("sent %v, want the install channel only", got)
	}
}

func TestPowerNotificationsNeedTheDispatchSwitch(t *testing.T) {
	p, doer, u := powerNotifyFixture(t)
	p.srv.dispatch = alertdispatch.New(false, nil, doer, nil, func() int64 { return 0 })
	p.srv.dispatchPower(u.ID, []power.Change{
		{Cond: power.Cond{Kind: power.KindMainsLost, Text: "Mains lost"}, Began: true, At: 1000},
	}, nil)
	if got := doer.wait(t, 1); len(got) != 0 {
		t.Errorf("with -alert-dispatch off, sent %v", got)
	}
}

func TestPowerFiredReadsLikeAnAlert(t *testing.T) {
	cases := []struct {
		c      power.Change
		up     bool
		name   string
		detail string
	}{
		{power.Change{Cond: power.Cond{Kind: power.KindMainsLost}, Began: true}, false, "Mains Lost", "running on battery"},
		{power.Change{Cond: power.Cond{Kind: power.KindMainsLost}, At: 5_341_000, Since: 1000}, true, "Mains Restored",
			"back on mains after 1 h 29 min"},
		{power.Change{Cond: power.Cond{Kind: power.KindNotResponding}, Began: true, Initial: true}, false,
			"Unit Not Responding", "no reply from the unit (already so when monitoring began)"},
		{power.Change{Cond: power.Cond{Kind: power.KindBatteryLow, Text: "Battery low"}, Began: true}, false,
			"Battery Low", "Battery low"},
	}
	for _, c := range cases {
		f, _, ok := powerFired(c.c, nil, false)
		if !ok || f.Up != c.up || f.AlertType != c.name || f.Detail != c.detail {
			t.Errorf("%s: got %+v", c.c.Kind, f)
		}
	}
	if _, _, ok := powerFired(power.Change{Cond: power.Cond{Kind: "error_bits"}}, nil, false); ok {
		t.Error("an old error-bits condition is sent")
	}
}
