package power

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"mikrodash/internal/power/modbus"
)

// gatewayAt serves each connection with `serve` on a real local port, as a
// converter on the network would.
func gatewayAt(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); serve(c) }()
		}
	}()
	return l.Addr().String()
}

func causeOfRead(t *testing.T, addr string) Cause {
	t.Helper()
	c := modbus.New(addr, 300*time.Millisecond)
	defer c.Close()
	_, err := c.Read(1, modbus.ReadInput, 0, 1)
	if err == nil {
		t.Fatal("the read succeeded")
	}
	return CauseOf(err)
}

func TestAConverterThatRefusesTheConnectionIsTheConvertersFault(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens there now: the connect is refused
	if got := causeOfRead(t, addr); got != CauseConverter {
		t.Errorf("cause %q, want %q", got, CauseConverter)
	}
}

func TestAConverterThatConnectsButNeverRepliesMeansTheUnitIsSilent(t *testing.T) {
	addr := gatewayAt(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	if got := causeOfRead(t, addr); got != CauseUnit {
		t.Errorf("cause %q, want %q", got, CauseUnit)
	}
}

func TestTheConvertersOwnNoReplyExceptionMeansTheUnitIsSilent(t *testing.T) {
	for _, code := range []byte{0x0A, 0x0B} {
		addr := gatewayAt(t, func(c net.Conn) {
			req := make([]byte, 12)
			if _, err := io.ReadFull(c, req); err != nil {
				return
			}
			out := make([]byte, 9)
			copy(out[0:2], req[0:2])
			binary.BigEndian.PutUint16(out[4:], 3)
			out[6], out[7], out[8] = req[6], req[7]|0x80, code
			_, _ = c.Write(out)
		})
		if got := causeOfRead(t, addr); got != CauseUnit {
			t.Errorf("exception %02X: cause %q, want %q", code, got, CauseUnit)
		}
	}
}

func TestAnErrorNamingNeitherDeviceIsUnknown(t *testing.T) {
	for _, err := range []error{
		errors.New("modbus: reply length 9000 out of range"),
		&modbus.ExceptionError{Function: modbus.ReadInput, Code: 0x02},
		io.ErrUnexpectedEOF,
	} {
		if got := CauseOf(err); got != CauseUnknown {
			t.Errorf("%v: cause %q, want none", err, got)
		}
	}
}

func TestTheNotRespondingConditionSaysWhichDevice(t *testing.T) {
	for why, want := range map[Cause]string{
		CauseConverter: "Not responding - converter not reachable",
		CauseUnit:      "Not responding - converter OK, inverter silent",
		CauseUnknown:   "Not responding",
	} {
		tr := NewTracker()
		var got []Change
		for ts := int64(1000); ts <= 3000; ts += 1000 {
			got = tr.Failure(ts, why)
		}
		if len(got) != 1 || got[0].Cond.Text != want {
			t.Errorf("%q: %+v, want one change reading %q", why, got, want)
		}
	}
}
