package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/power"
)

// powerAPI is a signed-in server with an empty database, two sites and two
// roles for the Power/UPS page, and no pollers (-no-pool): these tests are
// about who may do what, not about polling.
type powerAPI struct {
	t     *testing.T
	h     http.Handler
	token string
	sql   *sql.DB
	d     *db.DB
	srv   *Server
}

func newPowerAPI(t *testing.T) *powerAPI {
	t.Helper()
	const pw = "correct-horse-battery-staple"
	st := authFixture(t, pw)
	dir := t.TempDir()
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	for _, q := range []string{
		`INSERT INTO sites (id, name, created_at) VALUES ('site-1', 'Site One', 0), ('site-2', 'Site Two', 0)`,
		`INSERT INTO roles (id, name, created_at) VALUES ('pw-op', 'Power operator', 0), ('pw-view', 'Power viewer', 0)`,
		`INSERT INTO role_pages (role_id, page, access) VALUES ('pw-op', 'power-ups', 'write'), ('pw-view', 'power-ups', 'read')`,
		`DELETE FROM grants`,
	} {
		if _, err := h.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	srv, err := New(st, Options{WebDir: t.TempDir(), AuditDB: d, NoPool: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	handler := srv.Handler()
	rec := postLogin(handler, "someone", pw)
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in: %d", rec.Code)
	}
	token := strings.SplitN(strings.TrimPrefix(rec.Header().Get("Set-Cookie"), "mikrodash_sid="), ";", 2)[0]
	p := &powerAPI{t: t, h: handler, token: token, sql: h, d: d, srv: srv}
	p.grant() // start from none, whatever sign-in wrote
	return p
}

// grant replaces the user's grants: each is "role@scope", scope "global" or a
// site id.
func (p *powerAPI) grant(grants ...string) {
	p.t.Helper()
	if _, err := p.sql.Exec(`DELETE FROM grants`); err != nil {
		p.t.Fatal(err)
	}
	for i, g := range grants {
		role, scope, _ := strings.Cut(g, "@")
		typ, id := "site", scope
		if scope == "global" {
			typ, id = "global", ""
		}
		if _, err := p.sql.Exec(`INSERT INTO grants (id, principal_type, principal_id, role_id, scope_type, scope_id, created_at)
		    VALUES (?, 'user', 'u-1', ?, ?, ?, 0)`, "g"+string(rune('0'+i)), role, typ, id); err != nil {
			p.t.Fatal(err)
		}
	}
}

func (p *powerAPI) do(method, path, body string) (int, map[string]any) {
	p.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Cookie", "mikrodash_sid="+p.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func unitJSON(name, site, host string, slave int) string {
	b, _ := json.Marshal(map[string]any{"name": name, "siteId": site, "model": "powerguard/modbus-v1.1",
		"host": host, "port": 502, "slaveId": slave})
	return string(b)
}

func (p *powerAPI) listedNames() []string {
	p.t.Helper()
	code, body := p.do("GET", "/api/power", "")
	if code != http.StatusOK {
		p.t.Fatalf("list: %d %v", code, body)
	}
	var names []string
	for _, u := range body["units"].([]any) {
		names = append(names, u.(map[string]any)["name"].(string))
	}
	return names
}

func TestPowerUnitsAreGatedBySite(t *testing.T) {
	p := newPowerAPI(t)

	// No grant: nothing to see, nowhere to add.
	if code, _ := p.do("POST", "/api/power/units", unitJSON("INV-01", "site-1", "198.51.100.10", 1)); code != http.StatusForbidden {
		t.Errorf("create with no grant: %d, want 403", code)
	}

	p.grant("pw-op@site-1")
	if code, body := p.do("POST", "/api/power/units", unitJSON("INV-01", "site-1", "198.51.100.10", 1)); code != http.StatusOK {
		t.Fatalf("create on a site held: %d %v", code, body)
	}
	if code, _ := p.do("POST", "/api/power/units", unitJSON("INV-02", "site-2", "198.51.100.11", 1)); code != http.StatusForbidden {
		t.Errorf("create on another site: %d, want 403", code)
	}
	if code, _ := p.do("POST", "/api/power/units", unitJSON("INV-03", "", "198.51.100.12", 1)); code != http.StatusForbidden {
		t.Errorf("create with no site on a site grant: %d, want 403", code)
	}

	// A unit on site 2, put there by somebody who may.
	site2 := "site-2"
	other, err := p.d.CreatePowerUnit(db.PowerUnit{Name: "INV-SITE2", SiteID: &site2,
		Model: "powerguard/modbus-v1.1", Host: "198.51.100.20", Port: 502, SlaveID: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.listedNames(); len(got) != 1 || got[0] != "INV-01" {
		t.Errorf("a site-1 operator sees %v, want only INV-01", got)
	}
	// Another site's unit is answered as missing, not as forbidden.
	if code, _ := p.do("DELETE", "/api/power/units/"+other.ID, ""); code != http.StatusNotFound {
		t.Errorf("deleting another site's unit: %d, want 404", code)
	}
	_, body := p.do("GET", "/api/power", "")
	if ws := body["writableSites"].([]any); len(ws) != 1 || ws[0] != "site-1" {
		t.Errorf("writable sites %v, want [site-1]", ws)
	}

	// Moving a unit to a site the caller cannot write is refused.
	mine := body["units"].([]any)[0].(map[string]any)["id"].(string)
	if code, _ := p.do("PUT", "/api/power/units/"+mine, unitJSON("INV-01", "site-2", "198.51.100.10", 1)); code != http.StatusForbidden {
		t.Errorf("moving to another site: %d, want 403", code)
	}

	// Read-only on site 1: sees it, cannot change it.
	p.grant("pw-view@site-1")
	if got := p.listedNames(); len(got) != 1 {
		t.Errorf("a viewer sees %v", got)
	}
	if code, _ := p.do("PUT", "/api/power/units/"+mine, unitJSON("INV-01b", "site-1", "198.51.100.10", 1)); code != http.StatusForbidden {
		t.Errorf("a viewer's edit: %d, want 403", code)
	}
	if code, body := p.do("GET", "/api/power/units/"+mine+"/events", ""); code != http.StatusOK || body["events"] == nil {
		t.Errorf("a viewer's events: %d %v", code, body)
	}
	if code, body := p.do("GET", "/api/power/units/"+mine+"/history?range=7d", ""); code != http.StatusOK ||
		body["bucketMs"] != float64(30*60_000) || body["series"].(map[string]any)["input_v"] == nil {
		t.Errorf("a viewer's history: %d %v", code, body)
	}
	if code, _ := p.do("GET", "/api/power/units/"+other.ID+"/history", ""); code != http.StatusNotFound {
		t.Errorf("another site's history: %d, want 404", code)
	}

	// A global grant reaches every site, and units with none.
	p.grant("pw-view@global")
	if got := p.listedNames(); len(got) != 2 {
		t.Errorf("a global viewer sees %v, want both", got)
	}
}

func TestPowerUnitWritesAreValidatedAndAudited(t *testing.T) {
	p := newPowerAPI(t)
	p.grant("pw-op@global")

	bad := map[string]string{
		"no name":       unitJSON("", "", "198.51.100.10", 1),
		"slave id 0":    unitJSON("A", "", "198.51.100.10", 0),
		"slave id 248":  unitJSON("A", "", "198.51.100.10", 248),
		"a bad host":    unitJSON("A", "", "not a host!", 1),
		"unknown site":  unitJSON("A", "site-9", "198.51.100.10", 1),
		"unknown model": strings.Replace(unitJSON("A", "", "198.51.100.10", 1), "powerguard/modbus-v1.1", "acme/x", 1),
		"bad port":      strings.Replace(unitJSON("A", "", "198.51.100.10", 1), `"port":502`, `"port":0`, 1),
	}
	for name, body := range bad {
		if code, _ := p.do("POST", "/api/power/units", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}

	code, body := p.do("POST", "/api/power/units", unitJSON("INV-01", "site-1", "198.51.100.10", 1))
	if code != http.StatusOK {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["unit"].(map[string]any)["id"].(string)
	// The same converter and slave id is the same unit: refused.
	if code, body := p.do("POST", "/api/power/units", unitJSON("INV-02", "", "198.51.100.10", 1)); code != http.StatusConflict ||
		!strings.Contains(body["error"].(string), "INV-01") {
		t.Errorf("a second unit on one converter and slave id: %d %v", code, body)
	}
	// The same converter with another slave id is a second unit.
	if code, _ := p.do("POST", "/api/power/units", unitJSON("INV-02", "", "198.51.100.10", 2)); code != http.StatusOK {
		t.Errorf("a second slave id on one converter: %d", code)
	}
	// Saving a unit unchanged is not a clash with itself.
	if code, body := p.do("PUT", "/api/power/units/"+id, unitJSON("INV-01 Server room", "site-1", "198.51.100.10", 1)); code != http.StatusOK {
		t.Errorf("rename: %d %v", code, body)
	}
	if code, _ := p.do("DELETE", "/api/power/units/"+id, ""); code != http.StatusOK {
		t.Errorf("delete: %d", code)
	}

	rows, err := p.sql.Query(`SELECT action FROM audit_events WHERE action LIKE 'power.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	want := "power.unit.create power.unit.create power.unit.update power.unit.delete"
	if got := strings.Join(actions, " "); got != want {
		t.Errorf("audited %q, want %q", got, want)
	}
}

// A MEGATEC UPS HAS A CONVERTER TO ITSELF: RS232 is point to point and the
// protocol has no address, so no second unit may share its converter, whatever
// slave ID either names, and its own slave ID is stored as 1.
func TestAMegatecUPSHasAConverterToItself(t *testing.T) {
	p := newPowerAPI(t)
	p.grant("pw-op@global")
	megatec := func(name, host string, slave int) string {
		return strings.Replace(unitJSON(name, "", host, slave), "powerguard/modbus-v1.1", "powerguard/megatec", 1)
	}
	code, body := p.do("POST", "/api/power/units", megatec("UPS-01", "198.51.100.40", 7))
	if code != http.StatusOK {
		t.Fatalf("create: %d %v", code, body)
	}
	unit := body["unit"].(map[string]any)
	if unit["slaveId"] != float64(1) {
		t.Errorf("a Megatec UPS stored slave ID %v, want 1", unit["slaveId"])
	}
	for name, b := range map[string]string{
		"a second Megatec UPS": megatec("UPS-02", "198.51.100.40", 2),
		"a Modbus unit":        unitJSON("INV-01", "", "198.51.100.40", 3),
	} {
		if code, body := p.do("POST", "/api/power/units", b); code != http.StatusConflict ||
			!strings.Contains(body["error"].(string), "UPS-01") {
			t.Errorf("%s on its converter: %d %v", name, code, body)
		}
	}
	if code, _ := p.do("POST", "/api/power/units", megatec("UPS-02", "198.51.100.41", 1)); code != http.StatusOK {
		t.Errorf("a Megatec UPS on another converter: %d", code)
	}
	_, list := p.do("GET", "/api/power", "")
	for _, u := range list["units"].([]any) {
		if u.(map[string]any)["protocol"] != "megatec" {
			t.Errorf("listed without its protocol: %v", u)
		}
	}
}

// LIVE UPDATES GO ONLY WHERE THE LIST WOULD: a viewer receives a unit's
// `power:state` only while on the page and only for a site they may read,
// asked at each send.
func TestPowerLiveUpdatesFollowSitePermission(t *testing.T) {
	p := newPowerAPI(t)
	p.grant("pw-view@site-1")
	srv := p.srv
	srv.power.mu.Lock()
	one, two := "site-1", "site-2"
	srv.power.units = map[string]db.PowerUnit{"u1": {ID: "u1", SiteID: &one}, "u2": {ID: "u2", SiteID: &two}}
	srv.power.mu.Unlock()

	client := hub.NewClient("viewer", 16)
	srv.hub.Add(client)
	cn := &conn{srv: srv, c: client, sess: &Session{Username: "someone", AuthMode: "modern"}}
	received := func() []string {
		var ids []string
		for {
			select {
			case frame := <-client.Send:
				var f struct {
					Event string     `json:"event"`
					Data  PowerState `json:"data"`
				}
				if err := json.Unmarshal(frame, &f); err != nil || f.Event != "power:state" {
					t.Fatalf("unexpected frame %s", frame)
				}
				ids = append(ids, f.Data.UnitID)
			default:
				return ids
			}
		}
	}
	push := func() {
		for _, id := range []string{"u1", "u2", "unknown"} {
			srv.powerPush(power.State{UnitID: id, Online: true})
		}
	}

	push()
	if got := received(); len(got) != 0 {
		t.Errorf("a socket not on the page received %v", got)
	}
	srv.powerWatch(cn, "page", true)
	push()
	if got := strings.Join(received(), ","); got != "u1" {
		t.Errorf("a site-1 viewer received %q, want only u1", got)
	}
	// Revoked between two polls: the next one is not sent.
	p.grant()
	push()
	if got := received(); len(got) != 0 {
		t.Errorf("after the grant was revoked the viewer received %v", got)
	}
	p.grant("pw-view@global")
	srv.powerWatch(cn, "page", false)
	push()
	if got := received(); len(got) != 0 {
		t.Errorf("after leaving the page the viewer received %v", got)
	}
}

// SAVING SETTINGS REACHES THE POLLERS. A setting rendered, validated and saved
// but never read is the commonest defect in this codebase (AI_CONTEXT.md), so
// this goes through the real save route rather than asking powerSettings.
func TestSavedPowerSettingsReachThePollers(t *testing.T) {
	p := newPowerAPI(t)
	p.grant("administrator@global")
	code, body := p.do("POST", "/api/settings", `{"powerPollSec":10,"powerOfflineAfter":5,"powerBatteryLowPct":30}`)
	if code != http.StatusOK {
		t.Fatalf("save: %d %v", code, body)
	}
	if got := p.srv.power.applied; got.Interval != 10*time.Second || got.OfflineAfter != 5 || got.BatteryLowPct != 30 {
		t.Errorf("after the save the pollers run with %+v", got)
	}
	// Outside the bounds is refused by the write, and the pollers keep theirs.
	if code, _ := p.do("POST", "/api/settings", `{"powerPollSec":1}`); code == http.StatusOK {
		if p.srv.power.applied.Interval != 10*time.Second {
			t.Errorf("a 1 s interval reached the pollers: %v", p.srv.power.applied.Interval)
		}
	}
}

// THE DASHBOARD CARD'S "THIS SITE": ?router= narrows the list to the units at
// that router's sites, and falls back to every unit when none is there.
func TestPowerListNarrowsToARoutersSites(t *testing.T) {
	p := newPowerAPI(t)
	p.grant("pw-view@global")
	routers := `[{"id":"r-A","label":"A","host":"198.51.100.1","siteIds":["site-1"]},
	             {"id":"r-B","label":"B","host":"198.51.100.2","siteIds":["site-9"]}]`
	if err := os.WriteFile(filepath.Join(p.srv.store.Dir, "routers.json"), []byte(routers), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, site := range []string{"site-1", "site-2"} {
		s := site
		if _, err := p.d.CreatePowerUnit(db.PowerUnit{Name: "INV-" + site, SiteID: &s, Model: "powerguard/modbus-v1.1",
			Host: "198.51.100.10", Port: 502, SlaveID: i + 1, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	names := func(path string) (string, bool) {
		_, body := p.do("GET", path, "")
		var out []string
		for _, u := range body["units"].([]any) {
			out = append(out, u.(map[string]any)["name"].(string))
		}
		return strings.Join(out, ","), body["scoped"] == true
	}
	if got, scoped := names("/api/power?router=r-A"); got != "INV-site-1" || !scoped {
		t.Errorf("router A: %q scoped=%v, want only its site's unit", got, scoped)
	}
	if got, scoped := names("/api/power?router=r-B"); got != "INV-site-1,INV-site-2" || scoped {
		t.Errorf("router B (no units at its site): %q scoped=%v, want all", got, scoped)
	}
	if got, scoped := names("/api/power"); got != "INV-site-1,INV-site-2" || scoped {
		t.Errorf("no router: %q scoped=%v", got, scoped)
	}
}

func TestPowerExportWritesHistoryAndEventsAsCSV(t *testing.T) {
	p := newPowerAPI(t)
	p.grant("pw-view@global")
	site := "site-1"
	u, err := p.d.CreatePowerUnit(db.PowerUnit{Name: "INV 01/Server", SiteID: &site, Model: "powerguard/modbus-v1.1",
		Host: "198.51.100.10", Port: 502, SlaveID: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli() / 60_000 * 60_000
	for i, v := range []float64{229.5, 1} {
		ts := now - int64(2-i)*60_000
		if err := p.d.RecordPowerMinute(u.ID, ts, 12, 11, 140, []db.PowerStat{{Key: "input_v", Avg: v, Min: v - 1, Max: v + 1}}); err != nil {
			t.Fatal(err)
		}
	}
	// A text a spreadsheet would execute, as a hostile model definition could
	// put in an event name.
	if err := p.d.BeginPowerEvent(db.PowerEvent{UnitID: u.ID, Kind: "event", Code: 3, Text: "=HYPERLINK(1)",
		Fault: true, BeganAt: now - 90_000}); err != nil {
		t.Fatal(err)
	}
	get := func(q string) (string, string) {
		req := httptest.NewRequest("GET", "/api/power/units/"+u.ID+"/export.csv?"+q, nil)
		req.Header.Set("Cookie", "mikrodash_sid="+p.token)
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
		// Excel's form: a byte-order mark, CRLF lines, the last one ended.
		body := rec.Body.String()
		if !strings.HasPrefix(body, "\xef\xbb\xbf") || !strings.HasSuffix(body, "\r\n") {
			t.Fatalf("%s: not a UTF-8 BOM file of CRLF lines: %q", q, body)
		}
		return rec.Header().Get("Content-Disposition"), strings.TrimSuffix(strings.TrimPrefix(body, "\xef\xbb\xbf"), "\r\n")
	}
	disp, body := get("range=24h")
	lines := strings.Split(body, "\r\n")
	if !strings.Contains(disp, `filename="power-INV-01-Server-history-24h-1m.csv"`) {
		t.Errorf("disposition %q", disp)
	}
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "Time (UTC),Input voltage (V),Input voltage min (V),Input voltage max (V),Input frequency (Hz),") ||
		!strings.HasSuffix(lines[0], ",Polls,Answered,Reply time (ms)") {
		t.Fatalf("history CSV:\n%s", body)
	}
	if !strings.Contains(lines[1], ",229.5,228.5,230.5,") || !strings.HasSuffix(lines[1], ",12,11,140") ||
		!strings.Contains(lines[2], ",1,0,2,") {
		t.Errorf("history rows:\n%s", body)
	}
	_, body = get("range=7d&what=events")
	lines = strings.Split(body, "\r\n")
	if len(lines) != 2 || lines[0] != "Began (UTC),Ended (UTC),Duration (s),Kind,Code,Event,Fault,Already so at start" {
		t.Fatalf("events CSV:\n%s", body)
	}
	if !strings.Contains(lines[1], ",,,event,3,'=HYPERLINK(1),yes,no") {
		t.Errorf("event row %q: an open event has no end, and the formula must be defused", lines[1])
	}
	// The Reports tab's window: only the first minute falls inside it.
	disp, body = get(fmt.Sprintf("from=%d&to=%d", now-150_000, now-90_000))
	if lines = strings.Split(body, "\r\n"); len(lines) != 2 || !strings.Contains(lines[1], ",229.5,") ||
		!strings.Contains(disp, "-history-range-1m.csv") {
		t.Errorf("from/to export %q:\n%s", disp, body)
	}
}

// THE EXPORT FOLDS MINUTES INTO THE CHOSEN INTERVAL, and writes Excel's other
// format on request: semicolons, decimal commas. Fixed times, so no run can
// straddle an hour.
func TestPowerExportFoldsIntoAnIntervalAndWritesDecimalCommas(t *testing.T) {
	p := newPowerAPI(t)
	p.grant("pw-view@global")
	site := "site-1"
	u, err := p.d.CreatePowerUnit(db.PowerUnit{Name: "INV-02", SiteID: &site, Model: "powerguard/modbus-v1.1",
		Host: "198.51.100.11", Port: 502, SlaveID: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	hour := time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC).UnixMilli()
	for i, v := range []float64{230, 220.5, 210} { // minutes 0, 1 and 70
		ts := hour + []int64{0, 60_000, 70 * 60_000}[i]
		if err := p.d.RecordPowerMinute(u.ID, ts, 12, 10, 100+float64(i)*10, []db.PowerStat{
			{Key: "input_v", Avg: v, Min: v - 1, Max: v + 1}}); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/power/units/%s/export.csv?from=%d&to=%d&step=1h&sep=semicolon",
		u.ID, hour, hour+2*3_600_000), nil)
	req.Header.Set("Cookie", "mikrodash_sid="+p.token)
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	lines := strings.Split(strings.TrimSuffix(strings.TrimPrefix(rec.Body.String(), "\xef\xbb\xbf"), "\r\n"), "\r\n")
	if rec.Code != http.StatusOK || len(lines) != 3 || !strings.HasPrefix(lines[0], "Time (UTC);Input voltage (V);") {
		t.Fatalf("%d:\n%s", rec.Code, rec.Body.String())
	}
	// The first hour: two minutes, mean 225.25, lowest 219.5, highest 231;
	// polls summed, the reply time averaged over the answered polls.
	if !strings.HasPrefix(lines[1], "2026-03-10 14:00:00;225,25;219,5;231;") || !strings.HasSuffix(lines[1], ";24;20;105") {
		t.Errorf("first hour %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "2026-03-10 15:00:00;210;209;211;") || !strings.HasSuffix(lines[2], ";12;10;120") {
		t.Errorf("second hour %q", lines[2])
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "-history-range-1h.csv") {
		t.Errorf("disposition %q", rec.Header().Get("Content-Disposition"))
	}
}
