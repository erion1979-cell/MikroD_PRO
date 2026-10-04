package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// LIVE UPDATES GO ONLY WHERE THE LIST WOULD: a viewer receives a unit's
// `power:state` only while on the page and only for a site they may read,
// asked at each send.
func TestPowerLiveUpdatesFollowSitePermission(t *testing.T) {
	p := newPowerAPI(t)
	p.grant("pw-view@site-1")
	srv := p.srv
	srv.power.mu.Lock()
	srv.power.siteOf = map[string]string{"u1": "site-1", "u2": "site-2"}
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
	srv.powerWatch(cn, true)
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
	srv.powerWatch(cn, false)
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
