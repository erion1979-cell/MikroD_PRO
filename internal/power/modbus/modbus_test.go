package modbus

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gateway is a fake Ethernet-to-RS485 converter. Each dial hands the client one
// end of a pipe; `reply` decides what goes back for each request it reads.
type gateway struct {
	t     *testing.T
	reply func(req []byte) [][]byte // frames to write; nil writes nothing

	mu       sync.Mutex
	dials    int
	requests [][]byte
}

func (g *gateway) client(timeout time.Duration) *Client {
	c := New("gateway.test:502", timeout)
	c.dial = func(string, time.Duration) (net.Conn, error) {
		g.mu.Lock()
		g.dials++
		g.mu.Unlock()
		cli, srv := net.Pipe()
		go g.serve(srv)
		return cli, nil
	}
	return c
}

func (g *gateway) serve(conn net.Conn) {
	defer conn.Close()
	for {
		req := make([]byte, 12)
		if _, err := io.ReadFull(conn, req); err != nil {
			return
		}
		g.mu.Lock()
		g.requests = append(g.requests, req)
		g.mu.Unlock()
		for _, f := range g.reply(req) {
			if _, err := conn.Write(f); err != nil {
				return
			}
		}
	}
}

func (g *gateway) dialCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.dials
}

// frame builds a reply ADU: MBAP header then pdu.
func frame(tid uint16, unit byte, pdu ...byte) []byte {
	out := make([]byte, 7, 7+len(pdu))
	binary.BigEndian.PutUint16(out[0:], tid)
	binary.BigEndian.PutUint16(out[4:], uint16(len(pdu)+1))
	out[6] = unit
	return append(out, pdu...)
}

// registers answers a read with these values, echoing the request's id.
func registers(vals []uint16) func([]byte) [][]byte {
	return func(req []byte) [][]byte {
		pdu := []byte{req[7], byte(2 * len(vals))}
		for _, v := range vals {
			pdu = binary.BigEndian.AppendUint16(pdu, v)
		}
		return [][]byte{frame(binary.BigEndian.Uint16(req[0:]), req[6], pdu...)}
	}
}

// onBattery is the verified reading in docs/inverter/powerguard-register-map.md:
// on battery, no mains, no load.
var onBattery = []uint16{
	0, 0, 2205, 505, 0, 0, 0, 118, 0, 47,
	0, 0, 0, 250, 250, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 105, 122,
	0, 0, 260, 0, 0, 0,
}

func TestReadSendsTheDocumentedRequestAndDecodesTheReply(t *testing.T) {
	g := &gateway{t: t, reply: registers(onBattery)}
	c := g.client(time.Second)
	defer c.Close()

	got, err := c.Read(1, ReadInput, 0, 36)
	if err != nil {
		t.Fatal(err)
	}
	// tid 1, protocol 0, length 6, unit 1, function 04, start 0, count 36.
	want := []byte{0, 1, 0, 0, 0, 6, 1, 0x04, 0, 0, 0, 36}
	if string(g.requests[0]) != string(want) {
		t.Errorf("request % x, want % x", g.requests[0], want)
	}
	if len(got) != len(onBattery) {
		t.Fatalf("%d registers, want %d", len(got), len(onBattery))
	}
	for i := range got {
		if got[i] != onBattery[i] {
			t.Errorf("register %03d = %d, want %d", i, got[i], onBattery[i])
		}
	}
}

func TestOneConnectionServesSuccessiveReads(t *testing.T) {
	g := &gateway{t: t, reply: registers([]uint16{7})}
	c := g.client(time.Second)
	defer c.Close()

	for i := 0; i < 3; i++ {
		if _, err := c.Read(1, ReadInput, 0, 1); err != nil {
			t.Fatal(err)
		}
	}
	if n := g.dialCount(); n != 1 {
		t.Errorf("dialled %d times for three reads, want 1", n)
	}
	if tid := binary.BigEndian.Uint16(g.requests[2][0:]); tid != 3 {
		t.Errorf("third request carries transaction id %d, want 3", tid)
	}
}

func TestAWriteFunctionIsRefusedBeforeAnythingIsSent(t *testing.T) {
	g := &gateway{t: t, reply: registers([]uint16{0})}
	c := g.client(time.Second)
	defer c.Close()

	// 05 write coil, 06 write register, 0F write coils, 10 write registers,
	// and the others no read path needs.
	for _, fn := range []Function{0x01, 0x02, 0x05, 0x06, 0x0F, 0x10, 0x14, 0x17, 0x2B} {
		if _, err := c.Read(1, fn, 0, 1); err == nil {
			t.Errorf("function %02x was accepted", byte(fn))
		}
	}
	if n := g.dialCount(); n != 0 {
		t.Errorf("a refused function still dialled the gateway %d time(s)", n)
	}
}

func TestCountAndRangeAreCheckedBeforeSending(t *testing.T) {
	g := &gateway{t: t, reply: registers([]uint16{0})}
	c := g.client(time.Second)
	for _, tc := range []struct{ start, count uint16 }{{0, 0}, {0, MaxCount + 1}, {65535, 2}} {
		if _, err := c.Read(1, ReadInput, tc.start, tc.count); err == nil {
			t.Errorf("start %d count %d was accepted", tc.start, tc.count)
		}
	}
	if n := g.dialCount(); n != 0 {
		t.Errorf("an invalid range still dialled %d time(s)", n)
	}
}

func TestAnExceptionIsReturnedAndKeepsTheConnection(t *testing.T) {
	busy := true
	g := &gateway{t: t}
	g.reply = func(req []byte) [][]byte {
		if busy {
			busy = false
			return [][]byte{frame(binary.BigEndian.Uint16(req[0:]), req[6], 0x84, 0x06)}
		}
		return registers([]uint16{42})(req)
	}
	c := g.client(time.Second)
	defer c.Close()

	_, err := c.Read(1, ReadInput, 0, 1)
	var exc *ExceptionError
	if !errors.As(err, &exc) || !exc.Busy() {
		t.Fatalf("got %v, want a busy exception", err)
	}
	got, err := c.Read(1, ReadInput, 0, 1)
	if err != nil || got[0] != 42 {
		t.Fatalf("read after the exception: %v %v", got, err)
	}
	if n := g.dialCount(); n != 1 {
		t.Errorf("dialled %d times, want 1: an exception reply is a frame in step", n)
	}
}

func TestALateReplyToAnEarlierRequestIsSkipped(t *testing.T) {
	g := &gateway{t: t}
	g.reply = func(req []byte) [][]byte {
		tid := binary.BigEndian.Uint16(req[0:])
		stale := frame(tid-1, req[6], 0x04, 2, 0xde, 0xad)
		return append([][]byte{stale}, registers([]uint16{1234})(req)...)
	}
	c := g.client(time.Second)
	defer c.Close()

	got, err := c.Read(1, ReadInput, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 1234 {
		t.Errorf("got %d: the stale reply's value was taken", got[0])
	}
}

func TestABrokenReplyDropsTheConnection(t *testing.T) {
	cases := map[string]func(req []byte) [][]byte{
		"short byte count": func(req []byte) [][]byte {
			return [][]byte{frame(binary.BigEndian.Uint16(req[0:]), req[6], 0x04, 2, 0, 1)}
		},
		"wrong function": func(req []byte) [][]byte {
			return [][]byte{frame(binary.BigEndian.Uint16(req[0:]), req[6], 0x03, 4, 0, 1, 0, 2)}
		},
		"wrong unit": func(req []byte) [][]byte {
			return [][]byte{frame(binary.BigEndian.Uint16(req[0:]), req[6]+1, 0x04, 4, 0, 1, 0, 2)}
		},
		"bad protocol id": func(req []byte) [][]byte {
			f := registers([]uint16{1, 2})(req)[0]
			f[3] = 1
			return [][]byte{f}
		},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			g := &gateway{t: t, reply: reply}
			c := g.client(time.Second)
			defer c.Close()
			if _, err := c.Read(1, ReadInput, 0, 2); err == nil {
				t.Fatal("a broken reply was accepted")
			}
			g.reply = registers([]uint16{5, 6})
			if _, err := c.Read(1, ReadInput, 0, 2); err != nil {
				t.Fatalf("read after reconnecting: %v", err)
			}
			if n := g.dialCount(); n != 2 {
				t.Errorf("dialled %d times, want 2: the broken stream must be replaced", n)
			}
		})
	}
}

func TestNoReplyTimesOutAndTheNextReadReconnects(t *testing.T) {
	// Atomic: the first connection's goroutine is still reading it when the
	// client has already given up on that connection.
	var silent atomic.Bool
	silent.Store(true)
	g := &gateway{t: t}
	g.reply = func(req []byte) [][]byte {
		if silent.Load() {
			return nil
		}
		return registers([]uint16{9})(req)
	}
	c := g.client(50 * time.Millisecond)
	defer c.Close()

	start := time.Now()
	if _, err := c.Read(1, ReadInput, 0, 1); err == nil {
		t.Fatal("a silent unit produced no error")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("the timeout took %v", d)
	}
	silent.Store(false)
	if _, err := c.Read(1, ReadInput, 0, 1); err != nil {
		t.Fatalf("read after the timeout: %v", err)
	}
	if n := g.dialCount(); n != 2 {
		t.Errorf("dialled %d times, want 2", n)
	}
}

func TestAFailedDialIsReportedAndRetried(t *testing.T) {
	c := New("gateway.test:502", time.Second)
	calls := 0
	c.dial = func(string, time.Duration) (net.Conn, error) {
		calls++
		return nil, errors.New("connection refused")
	}
	for i := 0; i < 2; i++ {
		if _, err := c.Read(1, ReadInput, 0, 1); err == nil {
			t.Fatal("a failed dial produced no error")
		}
	}
	if calls != 2 {
		t.Errorf("dialled %d times, want a fresh attempt per read", calls)
	}
}
