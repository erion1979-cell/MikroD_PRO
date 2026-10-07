package server

// The Power/UPS page's REST routes: the units, with their live state, and the
// writes that add, change and remove them.
//
// ── PERMISSION IS PER SITE ──────────────────────────────────────────────────
//
// A unit belongs to a site, not to a router, so every check here is
// `rbac.CanPageOnSites` on the `power-ups` page: a global grant, or a grant on
// the unit's site. A unit with no site is reachable from a global grant only.
// Moving a unit between sites needs write on BOTH, or a site-scoped operator
// could hand a unit to a site they cannot see, or take one from a site they
// cannot manage.
//
// ── WHAT A UNIT IS ──────────────────────────────────────────────────────────
//
// The converter's host and port, and the unit's Modbus slave id. No converter
// type: any converter running as a Modbus TCP gateway is addressed the same way
// (docs/inverter/converters.md). Two units at one host, port and slave id would
// be one unit polled twice, so that is refused.

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/power"
	"mikrodash/internal/power/model"
	"mikrodash/internal/store"
)

const powerPage = "power-ups"

func (s *Server) registerPower(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/power", s.powerList)
	mux.HandleFunc("POST /api/power/units", s.powerCreate)
	mux.HandleFunc("PUT /api/power/units/{id}", s.powerUpdate)
	mux.HandleFunc("DELETE /api/power/units/{id}", s.powerDelete)
	mux.HandleFunc("GET /api/power/units/{id}/events", s.powerEvents)
	mux.HandleFunc("GET /api/power/units/{id}/history", s.powerHistory)
	mux.HandleFunc("GET /api/power/units/{id}/export.csv", s.powerExport)
	mux.HandleFunc("GET /api/power/units/{id}/report", s.powerReport)
}

// powerMay answers whether this session may use the page at `access` for a
// unit at siteID ("" for none).
func (s *Server) powerMay(sess *Session, access, siteID string) bool {
	if sess == nil {
		return false
	}
	// 'none' auth mode is implicitly administrator, the one short circuit
	// `mayManagePrincipals` also takes. A unit is app configuration, like a
	// site, not a write to a router.
	if sess.AuthMode == "none" {
		return true
	}
	return s.powerMayUser(s.userIDFor(sess.Username), access, siteID)
}

// powerMayUser is powerMay for a user id: what a notification channel's owner
// is asked (power_notify.go), where there is no session.
func (s *Server) powerMayUser(userID, access, siteID string) bool {
	if s.rbac == nil || !s.rbac.Available() {
		return false
	}
	var sites []string
	if siteID != "" {
		sites = []string{siteID}
	}
	return permitted(s.rbac.CanPageOnSites(userID, powerPage, access, sites))
}

// powerSession is the signed-in session, with the database present.
func (s *Server) powerSession(w http.ResponseWriter, r *http.Request) *Session {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return nil
	}
	if s.auditDB == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "database unavailable")
		return nil
	}
	return sess
}

func siteOf(u db.PowerUnit) string {
	if u.SiteID == nil {
		return ""
	}
	return *u.SiteID
}

// PowerCond is one condition true now, or one row of the events list.
type PowerCond struct {
	Kind    string `json:"kind"`
	Code    int    `json:"code"`
	Text    string `json:"text"`
	Fault   bool   `json:"fault"`
	Initial bool   `json:"initial"`
	BeganAt int64  `json:"beganAt"`
	EndedAt *int64 `json:"endedAt"`
}

// PowerState is a unit as the poller last saw it.
type PowerState struct {
	UnitID string `json:"unitId"`
	Online bool   `json:"online"`
	// HasReading is false until the unit first answers; every reading field
	// below is then zero and means nothing.
	HasReading bool               `json:"hasReading"`
	Mode       string             `json:"mode"`
	Values     map[string]float64 `json:"values"`
	Flags      map[string]bool    `json:"flags"`
	Raw        map[string]int     `json:"raw"`
	ApparentVA *float64           `json:"apparentVa"`
	EventCode  int                `json:"eventCode"`
	EventText  string             `json:"eventText"`
	LastOK     int64              `json:"lastOk"`
	ReplyMs    float64            `json:"replyMs"`
	Polls      int64              `json:"polls"`
	Answered   int64              `json:"answered"`
	LastError  string             `json:"lastError"`
	// Cause is which device the last poll failed at: "converter" (no
	// connection to it), "unit" (the converter answers, the unit behind it
	// does not), or "" (neither can be told, or the last poll answered).
	Cause string      `json:"cause"`
	Open  []PowerCond `json:"open"`
	// Topology is what the unit itself reports it is, "offline" or "online";
	// "" when it does not say (Modbus), and the model's then stands.
	Topology string `json:"topology"`
}

func powerStateView(st power.State) PowerState {
	out := PowerState{UnitID: st.UnitID, Online: st.Online, LastOK: st.LastOK, ReplyMs: st.ReplyMs,
		Polls: st.Polls, Answered: st.Answered, LastError: st.LastError, Cause: string(st.Cause),
		Values: map[string]float64{}, Flags: map[string]bool{}, Raw: map[string]int{}, Open: []PowerCond{}}
	if r := st.Reading; r != nil {
		out.HasReading = true
		out.Mode, out.EventCode, out.EventText, out.ApparentVA = string(r.Mode), r.EventCode, r.EventText, r.ApparentVA
		out.Topology = r.Topology
		for k, v := range r.Values {
			out.Values[k] = v
		}
		for k, v := range r.Flags {
			out.Flags[k] = v
		}
		for k, v := range r.Raw {
			out.Raw[k] = int(v)
		}
	}
	for _, c := range st.Open {
		out.Open = append(out.Open, PowerCond{Kind: string(c.Kind), Code: c.Code, Text: c.Text,
			Fault: c.Fault, Initial: c.Initial, BeganAt: c.At})
	}
	return out
}

// powerUnitView is one unit as the page lists it.
type powerUnitView struct {
	db.PowerUnit
	ProducerName string `json:"producerName"`
	ModelName    string `json:"modelName"`
	Serial       string `json:"serial"`
	// Topology is the model's, "offline" or "online", and decides how the page
	// draws the power flow; empty for a model this build does not have.
	Topology string `json:"topology"`
	// Protocol is "modbus" or "megatec"; a Megatec unit has no slave ID.
	Protocol string `json:"protocol"`
	CanWrite bool   `json:"canWrite"`
	// State is nil when nothing polls the unit: disabled, -no-pool, or a model
	// this build does not have.
	State *PowerState `json:"state"`
}

type powerModelView struct {
	ID string `json:"id"`
	// Producer is the brand's id (the defs/<producer>/ folder), which the
	// form's Brand list groups the models by.
	Producer     string `json:"producer"`
	ProducerName string `json:"producerName"`
	ModelName    string `json:"modelName"`
	Kind         string `json:"kind"`
	// Protocol is "modbus" or "megatec": the form asks a slave ID only for
	// Modbus.
	Protocol string `json:"protocol"`
	// Details are the product's technical details, for the form's "?".
	Details []string `json:"details"`
	// Default is the model a new unit starts on in the form.
	Default bool `json:"default"`
}

// powerStates is the pollers' current state per unit id; empty when they are
// not running.
func (s *Server) powerStates() map[string]power.State {
	s.power.mu.Lock()
	m := s.power.manager
	s.power.mu.Unlock()
	out := map[string]power.State{}
	if m == nil {
		return out
	}
	for _, st := range m.Snapshot() {
		out[st.UnitID] = st
	}
	return out
}

// powerList is `GET /api/power`: every unit the caller may read, the models a
// unit may name, and where the caller may add one.
func (s *Server) powerList(w http.ResponseWriter, r *http.Request) {
	sess := s.powerSession(w, r)
	if sess == nil {
		return
	}
	rows, err := s.auditDB.PowerUnits()
	if err != nil {
		log.Printf("[power] list: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "unit list failed")
		return
	}
	states := s.powerStates()
	units := []powerUnitView{}
	for _, u := range rows {
		if !s.powerMay(sess, "read", siteOf(u)) {
			continue
		}
		v := powerUnitView{PowerUnit: u, CanWrite: s.powerMay(sess, "write", siteOf(u))}
		if m := model.ByID(u.Model); m != nil {
			v.ProducerName, v.ModelName, v.Serial, v.Topology, v.Protocol = m.ProducerName, m.ModelName, m.Serial, m.Topology, m.Protocol
		}
		if st, ok := states[u.ID]; ok {
			sv := powerStateView(st)
			v.State = &sv
		}
		units = append(units, v)
	}
	scope := s.powerRouterSites(r.URL.Query().Get("router"))
	if len(scope) > 0 {
		var here []powerUnitView
		for _, v := range units {
			if scope[siteOf(v.PowerUnit)] {
				here = append(here, v)
			}
		}
		// A router whose sites hold no unit shows everything rather than an
		// empty card; `scoped` tells the card which it is showing.
		if len(here) > 0 {
			units = here
		} else {
			scope = nil
		}
	}

	models := []powerModelView{}
	all, _ := model.All()
	for _, m := range all {
		models = append(models, powerModelView{ID: m.ID(), Producer: m.Producer, ProducerName: m.ProducerName,
			ModelName: m.ModelName, Kind: m.Kind, Protocol: m.Protocol, Details: append([]string{}, m.Details...), Default: m.Default})
	}

	// WHERE THE CALLER MAY ADD OR MOVE A UNIT: the sites they hold write on,
	// and "" for no site when that is a global grant. Asked here so the form
	// offers only what the write would accept.
	writable := []string{}
	if s.powerMay(sess, "write", "") {
		writable = append(writable, "")
	}
	sites, err := s.auditDB.ListSites()
	if err != nil {
		log.Printf("[power] sites: %v", err)
	}
	for _, st := range sites {
		if s.powerMay(sess, "write", st.ID) {
			writable = append(writable, st.ID)
		}
	}

	writeJSON(w, map[string]any{"ok": true, "units": units, "models": models,
		"writableSites": writable, "polling": s.powerPolling(), "scoped": len(scope) > 0})
}

// powerRouterSites is the sites of the router `?router=` names, for the
// Dashboard card's "this site" view. Only narrows what the caller may already
// read, so it needs no permission of its own.
func (s *Server) powerRouterSites(routerID string) map[string]bool {
	if routerID == "" || s.store == nil {
		return nil
	}
	all, _ := s.store.Routers()
	for _, rt := range all {
		if rt.ID == routerID {
			out := map[string]bool{}
			for _, id := range store.RouterSiteIDs(rt) {
				out[id] = true
			}
			return out
		}
	}
	return nil
}

// powerPolling reports whether the pollers run in this process at all, so the
// page can say why every unit is blank under -no-pool.
func (s *Server) powerPolling() bool {
	s.power.mu.Lock()
	defer s.power.mu.Unlock()
	return s.power.manager != nil
}

// powerUnitBody is what the form posts.
type powerUnitBody struct {
	Name      string   `json:"name"`
	SiteID    string   `json:"siteId"`
	Model     string   `json:"model"`
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	SlaveID   int      `json:"slaveId"`
	RouterID  string   `json:"routerId"`
	BatteryAh *float64 `json:"batteryAh"`
	Enabled   *bool    `json:"enabled"`
}

var reHostname = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// validate checks the body and returns the unit it describes, or a message for
// the operator naming the field.
func (s *Server) powerValidate(b powerUnitBody) (db.PowerUnit, string) {
	u := db.PowerUnit{Name: strings.TrimSpace(b.Name), Model: b.Model, Host: strings.TrimSpace(b.Host),
		Port: b.Port, SlaveID: b.SlaveID, BatteryAh: b.BatteryAh, Enabled: b.Enabled == nil || *b.Enabled}
	if u.Name == "" || len(u.Name) > 64 {
		return u, "Name must be 1 to 64 characters"
	}
	mdl := model.ByID(u.Model)
	if mdl == nil {
		return u, "Choose a model"
	}
	if mdl.Protocol == "megatec" {
		// Megatec has no address; the form does not ask, and 1 is stored.
		u.SlaveID = 1
	}
	if net.ParseIP(u.Host) == nil && !reHostname.MatchString(u.Host) {
		return u, "Enter the converter's IP address, e.g. 192.168.20.83"
	}
	if u.Port < 1 || u.Port > 65535 {
		return u, "Port must be 1 to 65535"
	}
	if u.SlaveID < 1 || u.SlaveID > 247 {
		return u, "Slave ID must be 1 to 247"
	}
	if u.BatteryAh != nil && (*u.BatteryAh <= 0 || *u.BatteryAh > 100000) {
		return u, "Battery capacity must be a positive number of Ah, or empty"
	}
	if site := strings.TrimSpace(b.SiteID); site != "" {
		known := false
		if sites, err := s.auditDB.ListSites(); err == nil {
			for _, st := range sites {
				known = known || st.ID == site
			}
		}
		if !known {
			return u, "That site does not exist"
		}
		u.SiteID = &site
	}
	if rid := strings.TrimSpace(b.RouterID); rid != "" {
		if _, ok := s.routerExists(rid); !ok {
			return u, "That router does not exist"
		}
		u.RouterID = &rid
	}
	return u, ""
}

// powerClash finds another unit already at this converter with this slave id,
// or at all when either is a Megatec UPS, which has a converter to itself:
// RS232 is point to point and Megatec carries no address.
func (s *Server) powerClash(u db.PowerUnit) (string, error) {
	rows, err := s.auditDB.PowerUnits()
	if err != nil {
		return "", err
	}
	alone := func(id string) bool { m := model.ByID(id); return m != nil && m.Protocol == "megatec" }
	for _, o := range rows {
		if o.ID != u.ID && strings.EqualFold(o.Host, u.Host) && o.Port == u.Port &&
			(o.SlaveID == u.SlaveID || alone(o.Model) || alone(u.Model)) {
			return o.Name, nil
		}
	}
	return "", nil
}

func (s *Server) powerDecode(w http.ResponseWriter, r *http.Request) (db.PowerUnit, bool) {
	var b powerUnitBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&b); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "malformed request")
		return db.PowerUnit{}, false
	}
	u, msg := s.powerValidate(b)
	if msg != "" {
		writeJSONErr(w, http.StatusBadRequest, msg)
		return u, false
	}
	return u, true
}

func (s *Server) powerCheckClash(w http.ResponseWriter, u db.PowerUnit) bool {
	other, err := s.powerClash(u)
	if err != nil {
		log.Printf("[power] clash check: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "unit check failed")
		return false
	}
	if other != "" {
		writeJSONErr(w, http.StatusConflict, other+" already uses this converter and slave ID: "+
			"each unit on one converter needs its own slave ID, and a Megatec UPS needs a converter of its own")
		return false
	}
	return true
}

func powerAuditFields(u db.PowerUnit) map[string]any {
	return map[string]any{"name": u.Name, "siteId": siteOf(u), "model": u.Model, "host": u.Host,
		"port": u.Port, "slaveId": u.SlaveID, "enabled": u.Enabled}
}

// powerCreate is `POST /api/power/units`.
func (s *Server) powerCreate(w http.ResponseWriter, r *http.Request) {
	sess := s.powerSession(w, r)
	if sess == nil {
		return
	}
	u, ok := s.powerDecode(w, r)
	if !ok {
		return
	}
	if !s.powerMay(sess, "write", siteOf(u)) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}
	if !s.powerCheckClash(w, u) {
		return
	}
	created, err := s.auditDB.CreatePowerUnit(u)
	if err != nil {
		log.Printf("[power] create: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "unit create failed")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "power.unit.create", TargetType: "power-unit",
		TargetID: created.ID, TargetName: created.Name, After: powerAuditFields(created)})
	s.powerSync()
	writeJSON(w, map[string]any{"ok": true, "unit": created})
}

// powerFind loads a unit the caller may act on at `access`. A unit they may not
// read is answered as missing, so its existence is not disclosed.
func (s *Server) powerFind(w http.ResponseWriter, sess *Session, id, access string) (db.PowerUnit, bool) {
	rows, err := s.auditDB.PowerUnits()
	if err != nil {
		log.Printf("[power] find: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "unit lookup failed")
		return db.PowerUnit{}, false
	}
	for _, u := range rows {
		if u.ID != id {
			continue
		}
		if !s.powerMay(sess, "read", siteOf(u)) {
			break
		}
		if !s.powerMay(sess, access, siteOf(u)) {
			writeJSONErr(w, http.StatusForbidden, "Not permitted")
			return u, false
		}
		return u, true
	}
	writeJSONErr(w, http.StatusNotFound, "No such unit")
	return db.PowerUnit{}, false
}

// powerUpdate is `PUT /api/power/units/{id}`.
func (s *Server) powerUpdate(w http.ResponseWriter, r *http.Request) {
	sess := s.powerSession(w, r)
	if sess == nil {
		return
	}
	before, ok := s.powerFind(w, sess, r.PathValue("id"), "write")
	if !ok {
		return
	}
	u, ok := s.powerDecode(w, r)
	if !ok {
		return
	}
	// Write on the DESTINATION too: see the file header.
	if !s.powerMay(sess, "write", siteOf(u)) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted on that site")
		return
	}
	u.ID, u.CreatedAt = before.ID, before.CreatedAt
	if !s.powerCheckClash(w, u) {
		return
	}
	if err := s.auditDB.UpdatePowerUnit(u); err != nil {
		if errors.Is(err, db.ErrPowerUnitNotFound) {
			writeJSONErr(w, http.StatusNotFound, "No such unit")
			return
		}
		log.Printf("[power] update: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "unit update failed")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "power.unit.update", TargetType: "power-unit",
		TargetID: u.ID, TargetName: u.Name, Before: powerAuditFields(before), After: powerAuditFields(u)})
	s.powerSync()
	writeJSON(w, map[string]any{"ok": true, "unit": u})
}

// powerDelete is `DELETE /api/power/units/{id}`. Its history and events go
// with it (the foreign keys).
func (s *Server) powerDelete(w http.ResponseWriter, r *http.Request) {
	sess := s.powerSession(w, r)
	if sess == nil {
		return
	}
	u, ok := s.powerFind(w, sess, r.PathValue("id"), "write")
	if !ok {
		return
	}
	if err := s.auditDB.DeletePowerUnit(u.ID); err != nil && !errors.Is(err, db.ErrPowerUnitNotFound) {
		log.Printf("[power] delete: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "unit delete failed")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{Action: "power.unit.delete", TargetType: "power-unit",
		TargetID: u.ID, TargetName: u.Name, Before: powerAuditFields(u)})
	s.powerSync()
	writeJSON(w, map[string]any{"ok": true})
}

// powerEvents is `GET /api/power/units/{id}/events?limit=N`, newest first.
func (s *Server) powerEvents(w http.ResponseWriter, r *http.Request) {
	sess := s.powerSession(w, r)
	if sess == nil {
		return
	}
	u, ok := s.powerFind(w, sess, r.PathValue("id"), "read")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 500 {
		limit = 50
	}
	rows, err := s.auditDB.PowerEvents(u.ID, false, limit)
	if err != nil {
		log.Printf("[power] events: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "event list failed")
		return
	}
	out := make([]PowerCond, 0, len(rows))
	for _, e := range rows {
		out = append(out, PowerCond{Kind: e.Kind, Code: e.Code, Text: e.Text, Fault: e.Fault,
			Initial: e.Initial, BeganAt: e.BeganAt, EndedAt: e.EndedAt})
	}
	writeJSON(w, map[string]any{"ok": true, "events": out})
}

// powerRanges are the history windows the page offers, each with the bucket
// that keeps it near 300 points: one per 5 minutes over a day, per 30 over a
// week, per 2 hours over a month.
var powerRanges = map[string]struct{ span, bucket time.Duration }{
	"24h": {24 * time.Hour, 5 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 30 * time.Minute},
	"30d": {30 * 24 * time.Hour, 2 * time.Hour},
}

// powerHistoryKeys are the measures the charts draw: the two voltages on one,
// battery and load percentages on the other.
var powerHistoryKeys = []string{"input_v", "output_v", "battery_pct", "load_pct"}

// powerHistory is `GET /api/power/units/{id}/history?range=24h|7d|30d`.
func (s *Server) powerHistory(w http.ResponseWriter, r *http.Request) {
	sess := s.powerSession(w, r)
	if sess == nil {
		return
	}
	u, ok := s.powerFind(w, sess, r.PathValue("id"), "read")
	if !ok {
		return
	}
	rg, ok := powerRanges[r.URL.Query().Get("range")]
	if !ok {
		rg = powerRanges["24h"]
	}
	to := time.Now().UnixMilli()
	from := to - rg.span.Milliseconds()
	series, err := s.auditDB.PowerHistory(u.ID, powerHistoryKeys, from, to, rg.bucket.Milliseconds(), 0)
	if err != nil {
		log.Printf("[power] history: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "history failed")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "from": from, "to": to,
		"bucketMs": rg.bucket.Milliseconds(), "series": series})
}
