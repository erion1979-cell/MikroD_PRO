package server

// `GET /api/power/units/{id}/export.csv`: a unit's history or events as a CSV
// made to open straight in Excel, for charts drawn there.
//
// ── THE WINDOW AND THE INTERVAL ─────────────────────────────────────────────
//
// The window is a named one (`range=24h|7d|30d`, the unit page's tabs) or
// `from=&to=` (the Reports tabs). `step` folds the stored minutes into longer
// rows - 1m, 5m, 15m, 1h or 1d - because a month at one minute is 43,000 rows
// and a chart of them is a smear; each row keeps the mean, the lowest and the
// highest, so a short dip still shows. Hours and days start on the install's
// display zone's clock, not UTC's.
//
// ── EXCEL READS IT AS IT IS ─────────────────────────────────────────────────
//
// Excel opens a CSV with the separators of the computer's region, so a file
// written one way reads as one column, or as dates, on the other. `sep` picks:
// `comma` (decimal point, the default) or `semicolon` (decimal comma, as Excel
// expects in most of Europe); the page chooses from the browser's language and
// the user can change it. Times are written as Excel recognises them, with the
// zone in the column title rather than after each time; titles name the value
// and its unit; and a byte-order mark makes Excel read the file as UTF-8 (°C).
// A text cell a spreadsheet would run as a formula is defused, as every export
// here does.

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/power/model"
)

// powerExportSteps are the export intervals.
var powerExportSteps = map[string]int64{
	"1m": 60_000, "5m": 300_000, "15m": 900_000, "1h": 3_600_000, "1d": 86_400_000,
}

func (s *Server) powerExport(w http.ResponseWriter, r *http.Request) {
	sess := s.powerSession(w, r)
	if sess == nil {
		return
	}
	u, ok := s.powerFind(w, sess, r.PathValue("id"), "read")
	if !ok {
		return
	}
	q := r.URL.Query()
	rangeKey := q.Get("range")
	var from, to int64
	if rg, named := powerRanges[rangeKey]; named {
		to = time.Now().UnixMilli()
		from = to - rg.span.Milliseconds()
	} else if f, t, given := powerWindow(r); given && q.Get("from") != "" {
		from, to, rangeKey = f, t, "range"
	} else {
		rangeKey = "24h"
		to = time.Now().UnixMilli()
		from = to - powerRanges["24h"].span.Milliseconds()
	}
	stepKey := q.Get("step")
	step, ok := powerExportSteps[stepKey]
	if !ok {
		stepKey, step = "1m", 60_000
	}
	c := csvFormat{sep: ',', dec: '.'}
	if q.Get("sep") == "semicolon" {
		c = csvFormat{sep: ';', dec: ','}
	}
	zone, loc := s.displayTZ(), time.UTC
	if zone != "" {
		if l, err := time.LoadLocation(zone); err == nil {
			loc = l
		} else {
			zone = ""
		}
	}
	if zone == "" {
		zone = "UTC"
	}
	when := func(ms int64) string { return time.UnixMilli(ms).In(loc).Format("2006-01-02 15:04:05") }

	var header []string
	var rows [][]string
	what := "history"
	if q.Get("what") == "events" {
		what = "events"
		events, err := s.auditDB.PowerEventsIn(u.ID, from, to)
		if err != nil {
			log.Printf("[power] export events: %v", err)
			writeJSONErr(w, http.StatusInternalServerError, "export failed")
			return
		}
		header = []string{"Began (" + zone + ")", "Ended (" + zone + ")", "Duration (s)", "Kind", "Code",
			"Event", "Fault", "Already so at start"}
		for _, e := range events {
			ended, dur := "", ""
			if e.EndedAt != nil {
				ended, dur = when(*e.EndedAt), strconv.FormatInt((*e.EndedAt-e.BeganAt)/1000, 10)
			}
			rows = append(rows, []string{when(e.BeganAt), ended, dur, c.text(e.Kind), strconv.Itoa(e.Code),
				c.text(e.Text), yesNo(e.Fault), yesNo(e.Initial)})
		}
	} else {
		_, off := time.UnixMilli(from).In(loc).Zone()
		offMs := int64(off) * 1000
		keys := make([]string, 0, len(model.Measures))
		header = []string{"Time (" + zone + ")"}
		for _, m := range model.Measures {
			keys = append(keys, m.Key)
			header = append(header, m.Label+" ("+m.Unit+")", m.Label+" min ("+m.Unit+")", m.Label+" max ("+m.Unit+")")
		}
		header = append(header, "Polls", "Answered", "Reply time (ms)")
		series, err := s.auditDB.PowerHistory(u.ID, keys, from, to, step, offMs)
		mins, merr := s.auditDB.PowerMinutes(u.ID, from, to)
		if err != nil || merr != nil {
			log.Printf("[power] export history: %v %v", err, merr)
			writeJSONErr(w, http.StatusInternalServerError, "export failed")
			return
		}
		// Poll counts summed per row, the reply time averaged over the polls
		// that answered.
		type acc struct {
			polls, ok int
			reply     float64
		}
		buckets := map[int64]*acc{}
		var order []int64
		for _, m := range mins {
			b := (m.TS+offMs)/step*step - offMs
			a := buckets[b]
			if a == nil {
				a = &acc{}
				buckets[b] = a
				order = append(order, b)
			}
			a.polls += m.Polls
			a.ok += m.OK
			a.reply += m.ReplyMs * float64(m.OK)
		}
		values := map[int64]map[string][3]float64{}
		for key, pts := range series {
			for _, p := range pts {
				if values[p.TS] == nil {
					values[p.TS] = map[string][3]float64{}
				}
				values[p.TS][key] = [3]float64{p.Avg, p.Min, p.Max}
			}
		}
		for _, b := range order {
			a := buckets[b]
			row := []string{when(b)}
			for _, k := range keys {
				if v, has := values[b][k]; has {
					row = append(row, c.num(v[0]), c.num(v[1]), c.num(v[2]))
				} else {
					row = append(row, "", "", "")
				}
			}
			reply := ""
			if a.ok > 0 {
				reply = c.num(a.reply / float64(a.ok))
			}
			rows = append(rows, append(row, strconv.Itoa(a.polls), strconv.Itoa(a.ok), reply))
		}
	}

	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, u.Name)
	suffix := rangeKey
	if what == "history" {
		suffix += "-" + stepKey
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="power-`+name+`-`+what+`-`+suffix+`.csv"`)
	_, _ = w.Write([]byte(c.write(header, rows)))
}

// csvFormat is one of the two ways Excel reads a CSV.
type csvFormat struct{ sep, dec byte }

// num writes a number to two decimals at most, with the format's decimal sign.
func (c csvFormat) num(v float64) string {
	s := strconv.FormatFloat(float64(int64(v*100+copysignHalf(v)))/100, 'f', -1, 64)
	if c.dec != '.' {
		s = strings.Replace(s, ".", string(c.dec), 1)
	}
	return s
}

func copysignHalf(v float64) float64 {
	if v < 0 {
		return -0.5
	}
	return 0.5
}

// text is a cell of free text: one a spreadsheet would run as a formula gets a
// leading apostrophe, which Excel shows as plain text.
func (c csvFormat) text(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// write renders the file: a byte-order mark, the titles, the rows; a cell
// holding the separator, a quote or a line break is quoted.
func (c csvFormat) write(header []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString("\xef\xbb\xbf") // the UTF-8 byte-order mark
	line := func(cells []string) {
		for i, cell := range cells {
			if i > 0 {
				b.WriteByte(c.sep)
			}
			if strings.ContainsAny(cell, string(c.sep)+"\"\n\r") {
				cell = `"` + strings.ReplaceAll(cell, `"`, `""`) + `"`
			}
			b.WriteString(cell)
		}
		b.WriteString("\r\n")
	}
	line(header)
	for _, r := range rows {
		line(r)
	}
	return b.String()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
