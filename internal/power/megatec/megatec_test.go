package megatec

import (
	"bufio"
	"errors"
	"math"
	"net"
	"strings"
	"testing"
	"time"
)

// The protocol document's own example lines.
const (
	q1Online  = "(208.4 140.0 208.4 034 59.9 2.05 35.0 00110000"
	q1Standby = "(229.8 229.8 230.1 012 50.0 13.6 25.0 00001001"
	fRating   = "#220.0 004 096.0 50.0"
)

func TestAStatusLineParses(t *testing.T) {
	s, err := ParseStatus(q1Online)
	if err != nil {
		t.Fatal(err)
	}
	if s.InputV != 208.4 || s.InputFaultV != 140 || s.OutputV != 208.4 || s.LoadPct != 34 ||
		s.InputHz != 59.9 || s.BatteryV != 2.05 || s.TempC != 35 {
		t.Errorf("numbers: %+v", s)
	}
	// 00110000: b5 bypass and b4 failed; b3 0, so online.
	if s.UtilityFail() || s.BatteryLow() || !s.BypassOrAVR() || !s.Failed() || s.Standby() || !s.PerCell() {
		t.Errorf("bits %08b read wrong", s.Bits)
	}
	s, err = ParseStatus(q1Standby)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Standby() || s.PerCell() || s.Failed() || s.Bits&1 != 1 {
		t.Errorf("standby bits %08b or battery %v read wrong", s.Bits, s.BatteryV)
	}
}

func TestADashedValueIsAbsentNotZero(t *testing.T) {
	s, err := ParseStatus("(230.0 230.0 230.0 010 50.0 2.27 --.- 00000000")
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(s.TempC) {
		t.Errorf("an unmeasured temperature read as %v", s.TempC)
	}
}

func TestABrokenLineIsRefused(t *testing.T) {
	for _, line := range []string{
		"", "Q1", "208.4 140.0 208.4 034 59.9 2.05 35.0 00110000",
		"(208.4 140.0 208.4 034 59.9 2.05 35.0", "(208.4 140.0 208.4 034 59.9 2.05 35.0 0011000",
		"(208.4 140.0 208.4 034 59.9 2.05 35.0 00110002",
	} {
		if _, err := ParseStatus(line); err == nil {
			t.Errorf("%q accepted", line)
		}
	}
	if _, err := ParseRating("#220.0 004 096.0"); err == nil {
		t.Error("a short rating accepted")
	}
}

func TestARatingLineParses(t *testing.T) {
	r, err := ParseRating(fRating)
	if err != nil || r.Volts != 220 || r.Amps != 4 || r.BatteryV != 96 || r.Hz != 50 {
		t.Errorf("%+v %v", r, err)
	}
}

// fakeUPS answers on a pipe as a converter in transparent mode would.
func fakeUPS(t *testing.T, answer func(cmd string) string) (*Client, *[]string) {
	t.Helper()
	var sent []string
	c := New("ups:4001", time.Second)
	c.dial = func(string, time.Duration) (net.Conn, error) {
		here, there := net.Pipe()
		go func() {
			r := bufio.NewReader(there)
			for {
				cmd, err := r.ReadString('\r')
				if err != nil {
					return
				}
				cmd = strings.TrimSuffix(cmd, "\r")
				sent = append(sent, cmd)
				if a := answer(cmd); a != "" {
					there.Write([]byte(a + "\r"))
				}
			}
		}()
		return here, nil
	}
	return c, &sent
}

func TestAQueryReturnsTheLine(t *testing.T) {
	c, sent := fakeUPS(t, func(cmd string) string {
		if cmd == "Q1" {
			return q1Online
		}
		return cmd // echoed: not supported
	})
	defer c.Close()
	line, err := c.Query(QueryStatus)
	if err != nil || line != q1Online {
		t.Fatalf("%q %v", line, err)
	}
	if _, err := c.Query(QueryRating); !errors.Is(err, ErrUnsupported) {
		t.Errorf("an echoed F read as %v", err)
	}
	if strings.Join(*sent, ",") != "Q1,F" {
		t.Errorf("sent %v", *sent)
	}
}

// ONLY THE TWO READ COMMANDS EXIST: the zero Command, the only other value
// another package can make, is refused before anything is sent.
func TestOnlyTheTwoReadCommandsExist(t *testing.T) {
	dialled := false
	c := New("ups:4001", time.Second)
	c.dial = func(string, time.Duration) (net.Conn, error) { dialled = true; return nil, errors.New("no") }
	if _, err := c.Query(Command{}); err == nil || dialled {
		t.Errorf("the zero command was sent: %v, dialled %v", err, dialled)
	}
}

func TestASilentUPSTimesOut(t *testing.T) {
	c, _ := fakeUPS(t, func(string) string { return "" })
	c.timeout = 50 * time.Millisecond
	defer c.Close()
	_, err := c.Query(QueryStatus)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("silence read as %v, want a timeout", err)
	}
	if c.conn != nil {
		t.Error("the connection was kept after a timeout")
	}
}

func TestAnUnreachableConverterIsAConnectError(t *testing.T) {
	c := New("ups:4001", time.Second)
	c.dial = func(string, time.Duration) (net.Conn, error) { return nil, errors.New("connection refused") }
	var ce *ConnectError
	if _, err := c.Query(QueryStatus); !errors.As(err, &ce) {
		t.Errorf("got %v, want a ConnectError", err)
	}
}
