package power

// Why a poll failed: the converter, or the unit behind it.
//
// ── TWO DEVICES, ONE SILENCE ────────────────────────────────────────────────
//
// A unit is read through an Ethernet-to-RS485 converter, and either can be the
// one that is off. The reply tells them apart:
//
//   - no TCP connection to the converter at all: the CONVERTER is off,
//     unplugged or unreachable, whatever the unit behind it is doing;
//   - connected, but no reply before the timeout, or the converter's own
//     exception 0A/0B ("gateway path unavailable", "target device failed to
//     respond"): the converter is up and the UNIT is silent - switched off,
//     or its RS485 wiring is broken.
//
// Anything else (a malformed frame, another exception, a reset connection)
// names neither, and is reported as plain "not responding".
//
// A converter switched off under an open connection first times out (the
// request still leaves this host), and only the next poll's connect fails. The
// unit is declared down after several failures in a row, so by then the cause
// is the converter's.

import (
	"errors"
	"net"

	"mikrodash/internal/power/modbus"
)

// Cause is why a poll failed, as far as the failure can tell.
type Cause string

const (
	// CauseUnknown names neither device.
	CauseUnknown Cause = ""
	// CauseConverter: the converter cannot be reached.
	CauseConverter Cause = "converter"
	// CauseUnit: the converter answers, the unit behind it does not.
	CauseUnit Cause = "unit"
)

// CauseOf classifies a poll's error.
func CauseOf(err error) Cause {
	var conn *modbus.ConnectError
	if errors.As(err, &conn) {
		return CauseConverter
	}
	var exc *modbus.ExceptionError
	if errors.As(err, &exc) {
		if exc.GatewaySilent() {
			return CauseUnit
		}
		return CauseUnknown
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return CauseUnit
	}
	return CauseUnknown
}

// text is how the not-responding condition reads, in the events list and in a
// notification's subject.
func (c Cause) text() string {
	switch c {
	case CauseConverter:
		return "Not responding - converter not reachable"
	case CauseUnit:
		return "Not responding - converter OK, inverter silent"
	}
	return "Not responding"
}
