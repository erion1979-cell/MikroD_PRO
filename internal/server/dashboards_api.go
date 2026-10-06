package server

// `/api/dashboards` - a user's named dashboards beyond the first
// (docs/dashboards/PLAN.md).
//
// ── THE FIRST DASHBOARD STAYS WHERE IT WAS ──────────────────────────────────
//
// The dashboard everyone already has is still `/api/dashboard-layout` and its
// `user_layouts` row `dashboard`. This row, `dashboards`, holds only what is
// new: that dashboard's name and the others. So an install that never adds a
// dashboard reads and writes exactly what it did before.
//
// ── VALIDATED, NOT STORED AS SENT ───────────────────────────────────────────
//
// The dashboard layout route stores `cards` as an opaque array because the
// original did. This one is new, so it stores only what it has checked: every
// name, id, card type and position. A card names its type from
// `internal/dashcards`; one of the types that can follow a device of its own
// (dashwatch.go) may name a router, and a Traffic card an interface. Whether
// the viewer may see that router is asked when the card is watched, every
// time, not here: a grant can change after a dashboard is saved.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"mikrodash/internal/audit"
	"mikrodash/internal/dashcards"
)

const (
	dashboardsMax     = 20 // the first one included
	dashboardCardsMax = 100
	dashboardNameMax  = 40
	dashboardRowsMax  = 500
	dashboardHMax     = 100
)

var (
	dashboardIDRe  = regexp.MustCompile(`^[a-z0-9]{1,24}$`)
	dashRouterIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	dashCardUIDRe  = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	dashboardsKind = "dashboards" // the user_layouts row
)

// DashboardCard is one card on a named dashboard.
type DashboardCard struct {
	UID    string `json:"uid"`
	Type   string `json:"type"`
	Router string `json:"router"`
	Iface  string `json:"iface"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	W      int    `json:"w"`
	H      int    `json:"h"`
}

// Dashboard is one named dashboard.
type Dashboard struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Cards []DashboardCard `json:"cards"`
}

// Dashboards is the stored row.
type Dashboards struct {
	MainName string      `json:"mainName"`
	List     []Dashboard `json:"list"`
}

func (s *Server) registerDashboards(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dashboards", s.dashboardsGet)
	mux.HandleFunc("POST /api/dashboards", s.dashboardsSave)
}

func (s *Server) dashboardsGet(w http.ResponseWriter, r *http.Request) {
	sess := s.layoutSession(w, r)
	if sess == nil || !s.mayUseDashboard(w, sess) {
		return
	}
	out := Dashboards{MainName: "Overview", List: []Dashboard{}}
	if raw, err := s.ownLayout(sess, dashboardsKind); err == nil && raw != nil {
		var stored Dashboards
		// A row this build cannot read, or that no longer validates (a card
		// type since removed), is answered as no dashboards rather than as an
		// error: the first dashboard is untouched either way.
		if json.Unmarshal(raw, &stored) == nil {
			if clean, msg := cleanDashboards(stored); msg == "" {
				out = clean
			}
		}
	}
	writeJSON(w, map[string]any{"ok": true, "dashboards": out})
}

func (s *Server) dashboardsSave(w http.ResponseWriter, r *http.Request) {
	sess := s.layoutSession(w, r)
	if sess == nil || !s.mayUseDashboard(w, sess) {
		return
	}
	var body Dashboards
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "not a dashboards list")
		return
	}
	clean, msg := cleanDashboards(body)
	if msg != "" {
		writeJSONErr(w, http.StatusBadRequest, msg)
		return
	}
	user := s.layoutUser(sess)
	if user == "" {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if err := s.auditDB.SetLayout(user, dashboardsKind, clean); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not save the dashboards")
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "layout.update", TargetType: "layout", TargetName: dashboardsKind,
	})
	writeJSON(w, map[string]any{"ok": true, "dashboards": clean})
}

// cleanDashboards checks a list and returns it tidied (names trimmed, nil
// lists made empty), or the reason it is refused.
func cleanDashboards(in Dashboards) (Dashboards, string) {
	out := Dashboards{List: []Dashboard{}}
	var ok bool
	if out.MainName, ok = dashboardName(in.MainName, "Overview"); !ok {
		return out, "the first dashboard's name is not usable"
	}
	if len(in.List)+1 > dashboardsMax {
		return out, "at most 20 dashboards"
	}
	types := map[string]bool{}
	for _, c := range dashcards.All {
		types[c.ID] = true
	}
	ids := map[string]bool{}
	for _, d := range in.List {
		if !dashboardIDRe.MatchString(d.ID) || ids[d.ID] {
			return out, "a dashboard id is missing, malformed or repeated"
		}
		ids[d.ID] = true
		name, ok := dashboardName(d.Name, "")
		if !ok {
			return out, "every dashboard needs a name of 1 to 40 characters"
		}
		if len(d.Cards) > dashboardCardsMax {
			return out, "at most 100 cards on one dashboard"
		}
		cd := Dashboard{ID: d.ID, Name: name, Cards: []DashboardCard{}}
		uids := map[string]bool{}
		for _, c := range d.Cards {
			if !dashCardUIDRe.MatchString(c.UID) || uids[c.UID] {
				return out, "a card id is missing, malformed or repeated"
			}
			uids[c.UID] = true
			if !types[c.Type] {
				return out, "unknown card type " + c.Type
			}
			if c.Router != "" && (!dashWatchCards[c.Type] || !dashRouterIDRe.MatchString(c.Router)) {
				return out, "only a device card may name a device, and by its id"
			}
			if c.Iface != "" {
				if _, ok := dashboardName(c.Iface, ""); !ok || c.Type != "card-traffic" ||
					len(c.Iface) > 64 {
					return out, "only a Traffic card may name an interface"
				}
			}
			if c.X < 1 || c.W < 1 || c.X+c.W-1 > dashcards.Cols ||
				c.Y < 1 || c.Y > dashboardRowsMax || c.H < 1 || c.H > dashboardHMax {
				return out, "a card is outside the grid"
			}
			cd.Cards = append(cd.Cards, c)
		}
		out.List = append(out.List, cd)
	}
	return out, ""
}

// dashboardName trims a name and checks it: 1 to 40 characters, no control
// characters. An empty one becomes `def` when there is a default.
func dashboardName(s, def string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = def
	}
	if s == "" || len([]rune(s)) > dashboardNameMax {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return s, true
}
