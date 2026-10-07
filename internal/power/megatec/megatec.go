// Package megatec reads a UPS speaking the Megatec protocol (the "Q1" protocol)
// through a serial-to-Ethernet converter in transparent mode, and can only READ.
//
// ── THE PROTOCOL ────────────────────────────────────────────────────────────
//
// Plain ASCII over RS232, normally 2400 baud 8N1: a command ending in a
// carriage return, and one line back ending in one. Two commands are used:
//
//	Q1  status:  (MMM.M NNN.N PPP.P QQQ RR.R S.SS TT.T b7b6b5b4b3b2b1b0
//	             input V, input fault V, output V, load %, input Hz,
//	             battery V, temperature °C, and eight status bits
//	F   rating:  #MMM.M QQQ SS.SS RR.R
//	             rated V, rated A, rated battery V, rated Hz
//
// A UPS that does not know a command echoes it back, which is ErrUnsupported.
// Battery voltage is per cell on an online UPS (S.SS) and the whole battery on
// a standby one (SS.S).
//
// ── IT CANNOT WRITE, BY CONSTRUCTION ────────────────────────────────────────
//
// The same port takes commands that act: T (battery test), S (shut the output
// down), C (cancel), Q (silence the beeper). A Command can only be one of the
// two values below - its text is unexported, so no other package can build
// one - and Query refuses the zero value before a byte is sent.
// `TestOnlyTheTwoReadCommandsExist` holds that.
//
// ── ONE CONNECTION, ONE UPS ─────────────────────────────────────────────────
//
// RS232 is point to point and Megatec carries no address, so a converter
// serves exactly one UPS. A Client keeps one connection open and drops it on
// any error, so a late reply to a timed-out command can never be read as the
// answer to the next one.
package megatec

import (
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Command is one of the read commands below; nothing else can be sent.
type Command struct{ text string }

var (
	// QueryStatus is Q1, the status line.
	QueryStatus = Command{"Q1"}
	// QueryRating is F, the rating line.
	QueryRating = Command{"F"}
)

// maxLine bounds a reply. Q1's is 46 characters; a longer run without a
// carriage return means the stream is not Megatec.
const maxLine = 128

// ErrUnsupported is a UPS echoing a command it does not know.
var ErrUnsupported = errors.New("megatec: the UPS does not support this command")

// ConnectError is a failure to open a connection to the converter: it is off,
// unplugged or unreachable, whatever the UPS behind it does.
type ConnectError struct {
	Addr string
	Err  error
}

func (e *ConnectError) Error() string { return "megatec: connect " + e.Addr + ": " + e.Err.Error() }
func (e *ConnectError) Unwrap() error { return e.Err }

// Client talks to one UPS through one converter.
type Client struct {
	addr    string
	timeout time.Duration
	dial    func(addr string, timeout time.Duration) (net.Conn, error)

	mu   sync.Mutex
	conn net.Conn
}

// New returns a client for addr (host:port). Nothing is dialled until the first
// Query. timeout bounds the connect and each whole command-reply exchange.
func New(addr string, timeout time.Duration) *Client {
	return &Client{addr: addr, timeout: timeout, dial: func(a string, t time.Duration) (net.Conn, error) {
		return net.DialTimeout("tcp", a, t)
	}}
}

// Close drops the connection. A later Query dials again.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropLocked()
}

func (c *Client) dropLocked() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// Query sends cmd and returns the reply line, without its carriage return.
func (c *Client) Query(cmd Command) (string, error) {
	if cmd != QueryStatus && cmd != QueryRating {
		return "", fmt.Errorf("megatec: command %q is not a read command; refused", cmd.text)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		conn, err := c.dial(c.addr, c.timeout)
		if err != nil {
			return "", &ConnectError{Addr: c.addr, Err: err}
		}
		c.conn = conn
	}
	line, err := c.exchangeLocked(cmd)
	if err != nil {
		c.dropLocked()
		return "", err
	}
	if line == cmd.text {
		return "", ErrUnsupported
	}
	return line, nil
}

func (c *Client) exchangeLocked(cmd Command) (string, error) {
	if err := c.conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return "", fmt.Errorf("megatec: %w", err)
	}
	if _, err := c.conn.Write([]byte(cmd.text + "\r")); err != nil {
		return "", fmt.Errorf("megatec: send: %w", err)
	}
	var line []byte
	buf := make([]byte, 64)
	for {
		n, err := c.conn.Read(buf)
		for _, b := range buf[:n] {
			if b == '\r' {
				return strings.TrimSpace(string(line)), nil
			}
			line = append(line, b)
		}
		if len(line) > maxLine {
			return "", fmt.Errorf("megatec: %d bytes without an end of line; not a Megatec reply", len(line))
		}
		if err != nil {
			return "", fmt.Errorf("megatec: reply: %w", err)
		}
	}
}

// Status is one Q1 line. A number the UPS did not send is NaN.
type Status struct {
	InputV, InputFaultV, OutputV, LoadPct, InputHz, BatteryV, TempC float64
	// Bits are the eight status bits, b7 the highest.
	Bits byte
}

// The status bits, as the protocol numbers them.
func (s Status) UtilityFail() bool { return s.Bits&0x80 != 0 }
func (s Status) BatteryLow() bool  { return s.Bits&0x40 != 0 }

// BypassOrAVR is bypass on an online UPS, and boost or buck (the automatic
// voltage regulator) on a standby one.
func (s Status) BypassOrAVR() bool { return s.Bits&0x20 != 0 }
func (s Status) Failed() bool      { return s.Bits&0x10 != 0 }

// Standby is b3: 1 for a standby (offline or line-interactive) UPS, 0 online.
func (s Status) Standby() bool        { return s.Bits&0x08 != 0 }
func (s Status) Testing() bool        { return s.Bits&0x04 != 0 }
func (s Status) ShutdownActive() bool { return s.Bits&0x02 != 0 }

// PerCell reports a battery voltage given per cell (S.SS, an online UPS)
// rather than for the whole battery. No battery of any size reads under 3 V
// as a whole, and no single cell reads over it.
func (s Status) PerCell() bool { return s.BatteryV < 3 }

// ParseStatus reads a Q1 line.
func ParseStatus(line string) (Status, error) {
	if !strings.HasPrefix(line, "(") {
		return Status{}, fmt.Errorf("megatec: status line %q does not start with \"(\"", clip(line))
	}
	f := strings.Fields(line[1:])
	if len(f) != 8 {
		return Status{}, fmt.Errorf("megatec: status line has %d fields, want 8", len(f))
	}
	bits := f[7]
	if len(bits) != 8 || strings.Trim(bits, "01") != "" {
		return Status{}, fmt.Errorf("megatec: status bits %q are not eight 0s and 1s", clip(bits))
	}
	b, _ := strconv.ParseUint(bits, 2, 8)
	return Status{InputV: num(f[0]), InputFaultV: num(f[1]), OutputV: num(f[2]), LoadPct: num(f[3]),
		InputHz: num(f[4]), BatteryV: num(f[5]), TempC: num(f[6]), Bits: byte(b)}, nil
}

// Rating is one F line. A number the UPS did not send is NaN.
type Rating struct {
	Volts, Amps, BatteryV, Hz float64
}

// ParseRating reads an F line.
func ParseRating(line string) (Rating, error) {
	if !strings.HasPrefix(line, "#") {
		return Rating{}, fmt.Errorf("megatec: rating line %q does not start with \"#\"", clip(line))
	}
	f := strings.Fields(line[1:])
	if len(f) != 4 {
		return Rating{}, fmt.Errorf("megatec: rating line has %d fields, want 4", len(f))
	}
	return Rating{Volts: num(f[0]), Amps: num(f[1]), BatteryV: num(f[2]), Hz: num(f[3])}, nil
}

// num reads one field; some units send dashes for a value they do not measure.
func num(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(v, 0) {
		return math.NaN()
	}
	return v
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
