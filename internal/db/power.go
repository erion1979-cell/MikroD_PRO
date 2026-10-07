package db

// Reading and writing the Power/UPS tables. What they hold and why is in
// power_schema.go.

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrPowerUnitNotFound is an update or delete naming no unit.
var ErrPowerUnitNotFound = errors.New("no such Power/UPS unit")

// PowerUnit is one row of power_units. The nullable columns are pointers, so an
// unset site or battery capacity reaches the browser as null rather than as ""
// or 0 (see the note on Site).
type PowerUnit struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	SiteID    *string  `json:"siteId"`
	Model     string   `json:"model"`
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	SlaveID   int      `json:"slaveId"`
	RouterID  *string  `json:"routerId"`
	BatteryAh *float64 `json:"batteryAh"`
	Enabled   bool     `json:"enabled"`
	CreatedAt int64    `json:"createdAt"`
}

const powerUnitCols = `id, name, site_id, model, host, port, slave_id, router_id, battery_ah, enabled, created_at`

func scanPowerUnit(row interface{ Scan(...any) error }) (PowerUnit, error) {
	var u PowerUnit
	var enabled int
	err := row.Scan(&u.ID, &u.Name, &u.SiteID, &u.Model, &u.Host, &u.Port, &u.SlaveID,
		&u.RouterID, &u.BatteryAh, &enabled, &u.CreatedAt)
	u.Enabled = enabled != 0
	return u, err
}

// PowerUnits is every unit, by name.
func (d *DB) PowerUnits() ([]PowerUnit, error) {
	rows, err := d.sql.Query(`SELECT ` + powerUnitCols + ` FROM power_units ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PowerUnit{}
	for rows.Next() {
		u, err := scanPowerUnit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreatePowerUnit inserts u with a new ID and creation time, and returns it as
// stored. u.ID and u.CreatedAt are ignored.
func (d *DB) CreatePowerUnit(u PowerUnit) (PowerUnit, error) {
	// The same v4 UUID sites use: one way to mint an id in this package.
	id, err := newSiteID()
	if err != nil {
		return PowerUnit{}, err
	}
	u.ID, u.CreatedAt = id, time.Now().UnixMilli()
	_, err = d.sql.Exec(`INSERT INTO power_units (`+powerUnitCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.Name, u.SiteID, u.Model, u.Host, u.Port, u.SlaveID, u.RouterID, u.BatteryAh,
		boolInt(u.Enabled), u.CreatedAt)
	return u, err
}

// UpdatePowerUnit replaces every editable column of the unit u.ID.
func (d *DB) UpdatePowerUnit(u PowerUnit) error {
	res, err := d.sql.Exec(`UPDATE power_units SET name = ?, site_id = ?, model = ?, host = ?,
	    port = ?, slave_id = ?, router_id = ?, battery_ah = ?, enabled = ? WHERE id = ?`,
		u.Name, u.SiteID, u.Model, u.Host, u.Port, u.SlaveID, u.RouterID, u.BatteryAh,
		boolInt(u.Enabled), u.ID)
	return powerUnitTouched(res, err)
}

// DeletePowerUnit removes a unit, and by the foreign keys its history and events.
func (d *DB) DeletePowerUnit(id string) error {
	res, err := d.sql.Exec(`DELETE FROM power_units WHERE id = ?`, id)
	return powerUnitTouched(res, err)
}

func powerUnitTouched(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrPowerUnitNotFound
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// PowerStat is one measure over one minute.
type PowerStat struct {
	Key           string
	Avg, Min, Max float64
}

// RecordPowerMinute writes one finished minute of one unit, in one transaction.
// A minute written twice (a restart inside the minute) is replaced, not doubled.
func (d *DB) RecordPowerMinute(unitID string, ts int64, polls, ok int, replyMs float64,
	stats []PowerStat) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO power_minutes (unit_id, ts, polls, ok, reply_ms)
	    VALUES (?, ?, ?, ?, ?)`, unitID, ts, polls, ok, replyMs); err != nil {
		return err
	}
	for _, s := range stats {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO power_samples (unit_id, ts, key, avg, min, max)
		    VALUES (?, ?, ?, ?, ?, ?)`, unitID, ts, s.Key, s.Avg, s.Min, s.Max); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PowerEvent is one row of power_events.
type PowerEvent struct {
	ID      int64  `json:"id"`
	UnitID  string `json:"unitId"`
	Kind    string `json:"kind"`
	Code    int    `json:"code"`
	Text    string `json:"text"`
	Fault   bool   `json:"fault"`
	Initial bool   `json:"initial"`
	BeganAt int64  `json:"beganAt"`
	EndedAt *int64 `json:"endedAt"`
}

// BeginPowerEvent records a condition that began.
//
// A condition already open for the unit (same kind and code) is left as it is:
// that is the restart case, where the row written before the restart holds the
// true start and the new process only re-observed it.
func (d *DB) BeginPowerEvent(e PowerEvent) error {
	_, err := d.sql.Exec(`INSERT INTO power_events (unit_id, kind, code, text, fault, initial, began_at)
	    SELECT ?, ?, ?, ?, ?, ?, ?
	     WHERE NOT EXISTS (SELECT 1 FROM power_events
	                        WHERE unit_id = ? AND kind = ? AND code = ? AND ended_at IS NULL)`,
		e.UnitID, e.Kind, e.Code, e.Text, boolInt(e.Fault), boolInt(e.Initial), e.BeganAt,
		e.UnitID, e.Kind, e.Code)
	return err
}

// EndPowerEvent closes the unit's open condition of this kind and code.
func (d *DB) EndPowerEvent(unitID, kind string, code int, endedAt int64) error {
	_, err := d.sql.Exec(`UPDATE power_events SET ended_at = ?
	    WHERE unit_id = ? AND kind = ? AND code = ? AND ended_at IS NULL`,
		endedAt, unitID, kind, code)
	return err
}

// PowerEvents is a unit's events, newest first. Open ones only when openOnly,
// which is what a restart restores its trackers from.
func (d *DB) PowerEvents(unitID string, openOnly bool, limit int) ([]PowerEvent, error) {
	q := `SELECT id, unit_id, kind, code, text, fault, initial, began_at, ended_at
	        FROM power_events WHERE unit_id = ?`
	if openOnly {
		q += ` AND ended_at IS NULL`
	}
	rows, err := d.sql.Query(q+` ORDER BY began_at DESC, id DESC LIMIT ?`, unitID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PowerEvent{}
	for rows.Next() {
		var e PowerEvent
		var fault, initial int
		if err := rows.Scan(&e.ID, &e.UnitID, &e.Kind, &e.Code, &e.Text, &fault, &initial,
			&e.BeganAt, &e.EndedAt); err != nil {
			return nil, err
		}
		e.Fault, e.Initial = fault != 0, initial != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// PowerPoint is one measure over one bucket of a history window.
type PowerPoint struct {
	TS  int64   `json:"t"`
	Avg float64 `json:"avg"`
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// PowerHistory is a unit's measures from `from` to `to`, folded into buckets of
// `bucketMs` (a whole number of minutes): the mean of the minute averages, the
// lowest minimum and the highest maximum, so a dip shorter than a bucket still
// shows as the minimum. Keyed by measure, each series in time order.
func (d *DB) PowerHistory(unitID string, keys []string, from, to, bucketMs int64) (map[string][]PowerPoint, error) {
	out := map[string][]PowerPoint{}
	if len(keys) == 0 || bucketMs < 60000 {
		return out, nil
	}
	args := []any{bucketMs, bucketMs, unitID, from, to}
	marks := make([]string, len(keys))
	for i, k := range keys {
		marks[i] = "?"
		args = append(args, k)
		out[k] = []PowerPoint{}
	}
	rows, err := d.sql.Query(`SELECT key, (ts / ?) * ?, AVG(avg), MIN(min), MAX(max)
	    FROM power_samples WHERE unit_id = ? AND ts >= ? AND ts <= ? AND key IN (`+
		strings.Join(marks, ",")+`) GROUP BY key, ts / `+strconv.FormatInt(bucketMs, 10)+` ORDER BY key, 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var p PowerPoint
		if err := rows.Scan(&k, &p.TS, &p.Avg, &p.Min, &p.Max); err != nil {
			return nil, err
		}
		out[k] = append(out[k], p)
	}
	return out, rows.Err()
}

// PowerMinuteRow is one row of power_minutes.
type PowerMinuteRow struct {
	TS      int64
	Polls   int
	OK      int
	ReplyMs float64
}

// PowerMinutes is a unit's poll counts from `from` to `to`, oldest first.
func (d *DB) PowerMinutes(unitID string, from, to int64) ([]PowerMinuteRow, error) {
	rows, err := d.sql.Query(`SELECT ts, polls, ok, reply_ms FROM power_minutes
	    WHERE unit_id = ? AND ts >= ? AND ts <= ? ORDER BY ts`, unitID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PowerMinuteRow{}
	for rows.Next() {
		var m PowerMinuteRow
		if err := rows.Scan(&m.TS, &m.Polls, &m.OK, &m.ReplyMs); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PowerEventsIn is a unit's events that overlap `from` to `to`, oldest first:
// one that began before the window and ended inside it, or is still open,
// belongs to the window too.
func (d *DB) PowerEventsIn(unitID string, from, to int64) ([]PowerEvent, error) {
	rows, err := d.sql.Query(`SELECT id, unit_id, kind, code, text, fault, initial, began_at, ended_at
	    FROM power_events WHERE unit_id = ? AND began_at <= ? AND (ended_at IS NULL OR ended_at >= ?)
	    ORDER BY began_at, id`, unitID, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PowerEvent{}
	for rows.Next() {
		var e PowerEvent
		var fault, initial int
		if err := rows.Scan(&e.ID, &e.UnitID, &e.Kind, &e.Code, &e.Text, &fault, &initial,
			&e.BeganAt, &e.EndedAt); err != nil {
			return nil, err
		}
		e.Fault, e.Initial = fault != 0, initial != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// PowerStats is each measure's mean, lowest and highest over `from` to `to`:
// the mean of the minute averages, and the extremes of the minute extremes.
// A measure with no rows is absent.
func (d *DB) PowerStats(unitID string, keys []string, from, to int64) (map[string]PowerPoint, error) {
	out := map[string]PowerPoint{}
	if len(keys) == 0 {
		return out, nil
	}
	args := []any{unitID, from, to}
	marks := make([]string, len(keys))
	for i, k := range keys {
		marks[i] = "?"
		args = append(args, k)
	}
	rows, err := d.sql.Query(`SELECT key, AVG(avg), MIN(min), MAX(max) FROM power_samples
	    WHERE unit_id = ? AND ts >= ? AND ts <= ? AND key IN (`+strings.Join(marks, ",")+`)
	    GROUP BY key`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var p PowerPoint
		if err := rows.Scan(&k, &p.Avg, &p.Min, &p.Max); err != nil {
			return nil, err
		}
		p.TS = from
		out[k] = p
	}
	return out, rows.Err()
}

// SavePowerLast keeps a unit's last good reading (JSON) and when it was taken,
// replacing the one before. A unit deleted meanwhile is not an error: there is
// nothing left to keep it for.
func (d *DB) SavePowerLast(unitID string, at int64, reading []byte) error {
	_, err := d.sql.Exec(`INSERT INTO power_last (unit_id, at, reading)
		SELECT ?, ?, ? WHERE EXISTS (SELECT 1 FROM power_units WHERE id = ?)
		ON CONFLICT(unit_id) DO UPDATE SET at = excluded.at, reading = excluded.reading`,
		unitID, at, string(reading), unitID)
	return err
}

// PowerLast is a unit's last kept reading and when it was taken; at is 0 when
// none was ever kept.
func (d *DB) PowerLast(unitID string) (at int64, reading []byte, err error) {
	var s string
	err = d.sql.QueryRow(`SELECT at, reading FROM power_last WHERE unit_id = ?`, unitID).Scan(&at, &s)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, nil
	}
	return at, []byte(s), err
}
