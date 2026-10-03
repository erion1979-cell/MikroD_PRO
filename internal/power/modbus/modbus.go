// Package modbus is a Modbus TCP client that can only READ.
//
// ── WHY IT IS WRITTEN HERE AND NOT IMPORTED ─────────────────────────────────
//
// A read request is a seven-byte header and a five-byte body, and the reply is
// one length-prefixed frame. That is about a hundred lines, against a library
// whose write half this module must never use. CLAUDE.md asks that a dependency
// have a reason better than convenience; this one would not.
//
// ── IT CANNOT WRITE, BY CONSTRUCTION ────────────────────────────────────────
//
// The units this talks to ACCEPT write function codes (05, 06, 0F, 10), and a
// write changes a real setting on a real inverter. So `Function` has exactly two
// values, `Read` refuses any other before a byte is sent, and nothing in this
// package can build a request carrying a value.
// `TestAWriteFunctionIsRefusedBeforeAnythingIsSent` holds the first half.
//
// ── ONE CONNECTION, ONE REQUEST AT A TIME ───────────────────────────────────
//
// The Ethernet-to-RS485 converters accept few clients and drop the oldest when
// another connects, so a Client keeps ONE connection open and reconnects only
// after an error. The serial line behind it carries one request at a time, so
// Read holds a lock for the whole exchange: several units on one converter are
// polled one after another, never interleaved.
package modbus

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Function is a Modbus read function code. Only the two below exist.
type Function byte

const (
	// ReadHolding is function 03, Read Holding Registers.
	ReadHolding Function = 0x03
	// ReadInput is function 04, Read Input Registers.
	ReadInput Function = 0x04
)

// MaxCount is the most registers one read may ask for (Modbus Application
// Protocol v1.1b3, section 6.3/6.4: 1 to 125).
const MaxCount = 125

// maxFrame bounds the length field of a reply header. A Modbus TCP ADU is at
// most 260 bytes, so the length (unit id + PDU) is at most 254. A larger value
// means the stream is out of step, not that a big reply is coming.
const maxFrame = 254

// ExceptionError is a well-formed reply in which the unit refused the request.
// The connection is still in step after one, so it is kept open.
type ExceptionError struct {
	Function Function
	Code     byte
}

func (e *ExceptionError) Error() string {
	return fmt.Sprintf("unit replied with Modbus exception %d (%s) to function %02d",
		e.Code, exceptionName(e.Code), e.Function)
}

// Busy reports exception 06, Server Device Busy, which a unit answers when it
// is polled faster than it can serve.
func (e *ExceptionError) Busy() bool { return e.Code == 0x06 }

func exceptionName(code byte) string {
	switch code {
	case 0x01:
		return "illegal function"
	case 0x02:
		return "illegal data address"
	case 0x03:
		return "illegal data value"
	case 0x04:
		return "server device failure"
	case 0x05:
		return "acknowledge"
	case 0x06:
		return "server device busy"
	case 0x0A:
		return "gateway path unavailable"
	case 0x0B:
		return "gateway target device failed to respond"
	}
	return "unknown"
}

// Client reads registers through one Modbus TCP gateway.
type Client struct {
	addr    string
	timeout time.Duration
	dial    func(addr string, timeout time.Duration) (net.Conn, error)

	mu   sync.Mutex
	conn net.Conn
	tid  uint16
}

// New returns a client for addr (host:port). Nothing is dialled until the first
// Read. timeout bounds the connect and each whole request-reply exchange.
func New(addr string, timeout time.Duration) *Client {
	return &Client{addr: addr, timeout: timeout, dial: func(a string, t time.Duration) (net.Conn, error) {
		return net.DialTimeout("tcp", a, t)
	}}
}

// Addr is the gateway address this client reads through.
func (c *Client) Addr() string { return c.addr }

// Close drops the connection. A later Read dials again.
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

// Read reads count registers starting at start from the unit with id unit.
//
// Any error other than an *ExceptionError closes the connection: after a
// timeout or a malformed frame the byte stream can no longer be trusted, and the
// next Read starts clean on a new one.
func (c *Client) Read(unit byte, fn Function, start, count uint16) ([]uint16, error) {
	if fn != ReadHolding && fn != ReadInput {
		return nil, fmt.Errorf("modbus: function %02d is not a read function; refused", byte(fn))
	}
	if count == 0 || count > MaxCount {
		return nil, fmt.Errorf("modbus: register count %d outside 1..%d", count, MaxCount)
	}
	if uint32(start)+uint32(count) > 0x10000 {
		return nil, fmt.Errorf("modbus: registers %d+%d run past 65535", start, count)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		conn, err := c.dial(c.addr, c.timeout)
		if err != nil {
			return nil, fmt.Errorf("modbus: connect %s: %w", c.addr, err)
		}
		c.conn = conn
	}
	regs, err := c.exchangeLocked(unit, fn, start, count)
	var exc *ExceptionError
	if err != nil && !errors.As(err, &exc) {
		c.dropLocked()
	}
	return regs, err
}

func (c *Client) exchangeLocked(unit byte, fn Function, start, count uint16) ([]uint16, error) {
	if err := c.conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return nil, fmt.Errorf("modbus: %w", err)
	}
	c.tid++
	tid := c.tid

	var req [12]byte
	binary.BigEndian.PutUint16(req[0:], tid)
	binary.BigEndian.PutUint16(req[2:], 0) // protocol id: Modbus
	binary.BigEndian.PutUint16(req[4:], 6) // unit id + 5-byte PDU
	req[6] = unit
	req[7] = byte(fn)
	binary.BigEndian.PutUint16(req[8:], start)
	binary.BigEndian.PutUint16(req[10:], count)
	if _, err := c.conn.Write(req[:]); err != nil {
		return nil, fmt.Errorf("modbus: send: %w", err)
	}

	for {
		var head [7]byte
		if _, err := io.ReadFull(c.conn, head[:]); err != nil {
			return nil, fmt.Errorf("modbus: reply header: %w", err)
		}
		gotTID := binary.BigEndian.Uint16(head[0:])
		proto := binary.BigEndian.Uint16(head[2:])
		length := binary.BigEndian.Uint16(head[4:])
		if proto != 0 {
			return nil, fmt.Errorf("modbus: reply protocol id %d, want 0", proto)
		}
		if length < 3 || length > maxFrame {
			return nil, fmt.Errorf("modbus: reply length %d out of range", length)
		}
		body := make([]byte, length-1)
		if _, err := io.ReadFull(c.conn, body); err != nil {
			return nil, fmt.Errorf("modbus: reply body: %w", err)
		}
		// A reply to an EARLIER request that timed out arrives late and in
		// order. It is a complete frame, so it is skipped and the stream stays
		// in step; only a reply carrying this request's id answers it.
		if gotTID != tid {
			continue
		}
		if head[6] != unit {
			return nil, fmt.Errorf("modbus: reply from unit %d, asked unit %d", head[6], unit)
		}
		return decodePDU(fn, count, body)
	}
}

// decodePDU turns a reply PDU into register values.
func decodePDU(fn Function, count uint16, pdu []byte) ([]uint16, error) {
	switch pdu[0] {
	case byte(fn) | 0x80:
		return nil, &ExceptionError{Function: fn, Code: pdu[1]}
	case byte(fn):
	default:
		return nil, fmt.Errorf("modbus: reply function %02d, asked %02d", pdu[0], byte(fn))
	}
	n := int(pdu[1])
	if n != int(count)*2 || len(pdu) != 2+n {
		return nil, fmt.Errorf("modbus: reply carries %d bytes in a %d-byte frame, want %d",
			n, len(pdu)-2, int(count)*2)
	}
	out := make([]uint16, count)
	for i := range out {
		out[i] = binary.BigEndian.Uint16(pdu[2+2*i:])
	}
	return out, nil
}
