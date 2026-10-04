package server

import (
	"database/sql"
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/store"
)

// fakeConverter is a Modbus TCP gateway on a real local port, answering every
// read with the verified on-battery PowerGuard reading
// (docs/inverter/powerguard-register-map.md).
func fakeConverter(t *testing.T) (host string, port int) {
	t.Helper()
	onBattery := []uint16{
		0, 0, 2205, 505, 0, 0, 0, 118, 0, 47,
		0, 0, 0, 250, 250, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 105, 122,
		0, 0, 260, 0, 0, 0,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					req := make([]byte, 12)
					if _, err := io.ReadFull(c, req); err != nil {
						return
					}
					start, count := binary.BigEndian.Uint16(req[8:]), binary.BigEndian.Uint16(req[10:])
					pdu := []byte{req[7], byte(2 * count)}
					for _, v := range onBattery[start : start+count] {
						pdu = binary.BigEndian.AppendUint16(pdu, v)
					}
					head := make([]byte, 7)
					copy(head, req[:4])
					binary.BigEndian.PutUint16(head[4:], uint16(len(pdu)+1))
					head[6] = req[6]
					if _, err := c.Write(append(head, pdu...)); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ = strconv.Atoi(p)
	return h, port
}

func powerServer(t *testing.T, noPool bool) (*Server, *db.DB, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	host, port := fakeConverter(t)
	if _, err := d.CreatePowerUnit(db.PowerUnit{Name: "INV-01", Model: "powerguard/modbus-v1.1",
		Host: host, Port: port, SlaveID: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, Options{WebDir: t.TempDir(), AuditDB: d, NoPool: noPool, History: true})
	if err != nil {
		t.Fatal(err)
	}
	return srv, d, dir
}

// THE WIRING, END TO END: a unit stored in the database is polled through a
// real TCP connection as soon as the server starts, with no page open, and
// what it finds is recorded.
func TestAStoredUnitIsPolledAndItsOutageRecorded(t *testing.T) {
	srv, d, dir := powerServer(t, false)
	units, _ := d.PowerUnits()

	deadline := time.Now().Add(5 * time.Second)
	var events []db.PowerEvent
	for len(events) == 0 && time.Now().Before(deadline) {
		events, _ = d.PowerEvents(units[0].ID, true, 10)
		time.Sleep(10 * time.Millisecond)
	}
	if len(events) != 1 || events[0].Kind != "mains_lost" || !events[0].Initial {
		t.Fatalf("open events %+v, want the outage the unit started in", events)
	}

	// Shutdown writes the minute in progress, under -history.
	srv.Shutdown()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var minutes int
	if err := h.QueryRow(`SELECT COUNT(*) FROM power_minutes`).Scan(&minutes); err != nil {
		t.Fatal(err)
	}
	if minutes != 1 {
		t.Errorf("%d minute rows after shutdown, want the one in progress", minutes)
	}
}

func TestNoPoolPollsNothing(t *testing.T) {
	srv, _, _ := powerServer(t, true)
	defer srv.Shutdown()
	if srv.power.manager != nil {
		t.Error("-no-pool still started the Power/UPS pollers")
	}
}
