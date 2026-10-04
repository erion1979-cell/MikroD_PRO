package server

// The Reports page's Power/UPS tab: one unit over the page's date range, as a
// summary (outages, time on battery, availability, the extremes that matter)
// and the events in the window.

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"mikrodash/internal/db"
)

// PowerReport is what the tab draws.
type PowerReport struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
	// Outages is how many mains outages overlap the window, OutageMs their
	// total time inside it, LongestMs the longest one's (clipped likewise).
	Outages   int   `json:"outages"`
	OutageMs  int64 `json:"outageMs"`
	LongestMs int64 `json:"longestMs"`
	// Faults is how many fault event codes began in the window; NotResponding
	// how many times the unit stopped answering.
	Faults        int `json:"faults"`
	NotResponding int `json:"notResponding"`
	// Availability is the share of polls answered, 0-100; -1 with no polls.
	Availability float64 `json:"availability"`
	// Stats is each charted measure's mean, lowest and highest.
	Stats  map[string]db.PowerPoint `json:"stats"`
	Events []PowerCond              `json:"events"`
}

// buildPowerReport is the summary, from rows already read. Pure, so the rules
// (clipping to the window, an open outage running to its end) are tested
// without a database.
func buildPowerReport(from, to int64, events []db.PowerEvent, mins []db.PowerMinuteRow,
	stats map[string]db.PowerPoint) PowerReport {
	rep := PowerReport{From: from, To: to, Availability: -1, Stats: stats, Events: []PowerCond{}}
	for _, e := range events {
		rep.Events = append(rep.Events, PowerCond{Kind: e.Kind, Code: e.Code, Text: e.Text, Fault: e.Fault,
			Initial: e.Initial, BeganAt: e.BeganAt, EndedAt: e.EndedAt})
		began := e.BeganAt >= from
		switch e.Kind {
		case "mains_lost":
			end := to
			if e.EndedAt != nil && *e.EndedAt < to {
				end = *e.EndedAt
			}
			d := end - max(e.BeganAt, from)
			if d > 0 {
				rep.Outages++
				rep.OutageMs += d
				rep.LongestMs = max(rep.LongestMs, d)
			}
		case "event":
			if e.Fault && began {
				rep.Faults++
			}
		case "not_responding":
			if began {
				rep.NotResponding++
			}
		}
	}
	polls, ok := 0, 0
	for _, m := range mins {
		polls += m.Polls
		ok += m.OK
	}
	if polls > 0 {
		rep.Availability = float64(int(float64(ok)/float64(polls)*1000)) / 10
	}
	return rep
}

// powerReport is `GET /api/power/units/{id}/report?from=&to=` (Unix ms).
func (s *Server) powerReport(w http.ResponseWriter, r *http.Request) {
	sess := s.powerSession(w, r)
	if sess == nil {
		return
	}
	u, ok := s.powerFind(w, sess, r.PathValue("id"), "read")
	if !ok {
		return
	}
	from, to, ok := powerWindow(r)
	if !ok {
		writeJSONErr(w, http.StatusBadRequest, "from and to must be times, from before to")
		return
	}
	events, err := s.auditDB.PowerEventsIn(u.ID, from, to)
	if err == nil {
		var mins []db.PowerMinuteRow
		if mins, err = s.auditDB.PowerMinutes(u.ID, from, to); err == nil {
			var stats map[string]db.PowerPoint
			if stats, err = s.auditDB.PowerStats(u.ID, powerHistoryKeys, from, to); err == nil {
				writeJSON(w, map[string]any{"ok": true, "report": buildPowerReport(from, to, events, mins, stats)})
				return
			}
		}
	}
	log.Printf("[power] report: %v", err)
	writeJSONErr(w, http.StatusInternalServerError, "report failed")
}

// powerWindow reads ?from=&to= (Unix ms). With neither, the last 24 hours.
func powerWindow(r *http.Request) (from, to int64, ok bool) {
	q := r.URL.Query()
	if q.Get("from") == "" && q.Get("to") == "" {
		to = time.Now().UnixMilli()
		return to - 24*time.Hour.Milliseconds(), to, true
	}
	from, e1 := strconv.ParseInt(q.Get("from"), 10, 64)
	to, e2 := strconv.ParseInt(q.Get("to"), 10, 64)
	return from, to, e1 == nil && e2 == nil && from >= 0 && from < to
}
