// Command powerprobe polls one Power/UPS unit and prints what MikroDash would
// decode from it: the live check that no unit test can stand in for.
//
// ── READ-ONLY ───────────────────────────────────────────────────────────────
//
// It uses internal/power/modbus, which cannot send anything but function 03 or
// 04, and the same model definition and event tracker the server uses. So what
// it prints is what the Power/UPS page will show, and a disagreement with the
// unit's own display is a bug in the definition, found before anyone relies on
// it.
//
// The converter in front of the unit must run as a Modbus TCP gateway; its make
// does not matter (docs/inverter/converters.md). For a Megatec UPS
// (-model powerguard/megatec) it runs in transparent mode instead, and the probe
// sends only the two read commands, Q1 and F (internal/power/megatec).
//
//	docker run --rm --network host -v "$PWD":/src -w /src golang:1.27-alpine \
//	  go run ./cmd/powerprobe -host 192.168.20.83
//
// Add -count 0 to keep polling until Ctrl-C and watch events as they happen: pull
// the mains plug, and "Mains lost began" should appear within one interval.
//
// ── STARTED WITH NO OPTIONS, IT ASKS ────────────────────────────────────────
//
// On Windows the natural way to run it is a double-click, which passes no
// options. So with none it asks for the converter's address, port and slave id,
// polls until the window is closed, and waits for Enter before closing on an
// error, so the message can be read rather than flashing past.
//
//	GOOS=windows GOARCH=amd64 go build -o powerprobe.exe ./cmd/powerprobe
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/power"
	"mikrodash/internal/power/megatec"
	"mikrodash/internal/power/modbus"
	"mikrodash/internal/power/model"
)

func main() {
	host := flag.String("host", "", "the converter's IP address (required)")
	port := flag.Int("port", 502, "the converter's Modbus TCP port")
	slave := flag.Int("slave", 1, "the unit's Modbus slave id, 1-247")
	modelID := flag.String("model", "powerguard/modbus-v1.1", "the model definition, producer/model")
	count := flag.Int("count", 1, "polls to make; 0 polls until Ctrl-C")
	interval := flag.Duration("interval", 5*time.Second, "time between polls (not under 1s)")
	timeout := flag.Duration("timeout", 2*time.Second, "reply timeout")
	raw := flag.Bool("raw", false, "also print every register (or Megatec line) as read")
	list := flag.Bool("models", false, "list the model definitions this build has, and exit")
	flag.Parse()
	if flag.NFlag() == 0 {
		interactive = true
		in := bufio.NewReader(os.Stdin)
		fmt.Println("MikroDash Power/UPS probe - reads one unit, never writes to it.")
		fmt.Println()
		if askInt(in, "Protocol: 1 = Modbus inverter (gateway mode), 2 = Megatec UPS (transparent mode)", 1) == 2 {
			*modelID = "powerguard/megatec"
		}
		*host = ask(in, "Converter IP address", "")
		*port = askInt(in, "Port", 502)
		if *modelID != "powerguard/megatec" {
			*slave = askInt(in, "Slave ID", 1)
		}
		*count = 0
		fmt.Println("\nPolling every 5 seconds. Close this window (or press Ctrl-C) to stop.")
		fmt.Println()
	}

	all, err := model.All()
	if err != nil {
		fail("a model definition is broken: %v", err)
	}
	if *list {
		for _, m := range all {
			fmt.Printf("%-28s %s %s (%s)\n", m.ID(), m.ProducerName, m.ModelName, m.Kind)
		}
		return
	}
	if *host == "" {
		fail("-host is required: the converter's IP address")
	}
	if *slave < 1 || *slave > 247 {
		fail("-slave must be 1-247")
	}
	if *interval < time.Second {
		fail("-interval must be at least 1s: a unit polled faster answers \"busy\"")
	}
	mdl := model.ByID(*modelID)
	if mdl == nil {
		fail("no model %q; -models lists them", *modelID)
	}

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	var read reader
	if mdl.Protocol == "megatec" {
		client := megatec.New(addr, *timeout)
		defer client.Close()
		read = megatecReader(client, mdl)
		fmt.Printf("Polling %s as %s %s (Megatec). Read-only.\n\n", addr, mdl.ProducerName, mdl.ModelName)
	} else {
		client := modbus.New(addr, *timeout)
		defer client.Close()
		read = modbusReader(client, mdl, byte(*slave))
		fmt.Printf("Polling %s, slave %d, as %s %s. Read-only.\n\n", addr, *slave, mdl.ProducerName, mdl.ModelName)
	}
	tracker := power.NewTracker()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	for n := 0; *count == 0 || n < *count; n++ {
		if n > 0 {
			select {
			case <-stop:
				return
			case <-time.After(*interval):
			}
		}
		poll(read, tracker, *raw, addr, mdl)
	}
}

// reader takes one reading, with what it read as text lines for -raw. A
// failure to get a reply is a plain error; one to make sense of it is
// errUndecodable.
type reader func() (model.Reading, []string, error)

type errUndecodable struct{ error }

func modbusReader(c *modbus.Client, mdl *model.Model, slave byte) reader {
	return func() (model.Reading, []string, error) {
		regs := make([][]uint16, 0, len(mdl.Reads))
		for _, r := range mdl.Reads {
			got, err := c.Read(slave, r.Function, r.Start, r.Count)
			if err != nil {
				return model.Reading{}, nil, err
			}
			regs = append(regs, got)
		}
		var lines []string
		for i, rd := range mdl.Reads {
			lines = append(lines, fmt.Sprintf("raw fn%02d from %d: %v", rd.Function, rd.Start, regs[i]))
		}
		r, err := mdl.Decode(regs)
		if err != nil {
			return r, lines, errUndecodable{err}
		}
		return r, lines, nil
	}
}

// megatecReader asks Q1 each poll, and F until the UPS answers it or says it
// cannot, as the server does.
func megatecReader(c *megatec.Client, mdl *model.Model) reader {
	var rating *megatec.Rating
	ratingDone := false
	return func() (model.Reading, []string, error) {
		line, err := c.Query(megatec.QueryStatus)
		if err != nil {
			return model.Reading{}, nil, err
		}
		lines := []string{"Q1 -> " + line}
		st, err := megatec.ParseStatus(line)
		if err != nil {
			return model.Reading{}, lines, errUndecodable{err}
		}
		if !ratingDone {
			f, err := c.Query(megatec.QueryRating)
			switch {
			case err == nil:
				lines = append(lines, "F  -> "+f)
				if r, err := megatec.ParseRating(f); err == nil {
					rating = &r
				} else {
					lines = append(lines, "F  not understood: "+err.Error())
				}
				ratingDone = true
			case errors.Is(err, megatec.ErrUnsupported):
				lines = append(lines, "F  not supported by this UPS: no battery size")
				ratingDone = true
			default:
				lines = append(lines, "F  no reply: "+err.Error())
			}
		}
		lines = append(lines, fmt.Sprintf("status bits %08b", st.Bits))
		return mdl.FromMegatec(st, rating), lines, nil
	}
}

func poll(read reader, tr *power.Tracker, raw bool, addr string, mdl *model.Model) {
	began := time.Now()
	stamp := began.Format("15:04:05")
	r, lines, err := read()
	reply := time.Since(began)
	var bad errUndecodable
	switch {
	case errors.As(err, &bad):
		fmt.Printf("%s  UNDECODABLE: %v\n", stamp, bad.error)
		for _, l := range lines {
			fmt.Printf("    %s\n", l)
		}
		report(tr.Failure(began.UnixMilli(), power.CauseUnknown))
		return
	case err != nil:
		why := power.CauseOf(err)
		fmt.Printf("%s  NO READING - %s\n", stamp, whyNoReading(why, addr, mdl))
		fmt.Printf("    (%v)\n", err)
		report(tr.Failure(began.UnixMilli(), why))
		return
	}

	fmt.Printf("%s  mode %-8s reply %d ms  event %02d %s\n", stamp, r.Mode, reply.Milliseconds(),
		r.EventCode, r.EventText)
	for _, ms := range model.Measures {
		if v, ok := r.Values[ms.Key]; ok {
			fmt.Printf("    %-22s %8.1f %s\n", ms.Label, v, ms.Unit)
		}
	}
	if r.ApparentVA != nil {
		fmt.Printf("    %-22s %8.0f VA (calculated: output V x A)\n", "Apparent power", *r.ApparentVA)
	}
	var flags []string
	for name, on := range r.Flags {
		mark := "-"
		if on {
			mark = "+"
		}
		flags = append(flags, mark+name)
	}
	sort.Strings(flags)
	fmt.Printf("    flags  %s\n", strings.Join(flags, " "))
	if r.Version != "" {
		fmt.Printf("    %-22s %s\n", "Firmware", r.Version)
	}
	if raw {
		for _, l := range lines {
			fmt.Printf("    %s\n", l)
		}
	}
	report(tr.Success(r, began.UnixMilli()))
	fmt.Println()
}

// whyNoReading says which device a failed poll stopped at, the converter or
// the unit behind it, and what to check there: the same split the Power/UPS
// page makes (internal/power/cause.go).
func whyNoReading(why power.Cause, addr string, mdl *model.Model) string {
	unit := "inverter"
	if mdl.Kind == "ups" {
		unit = "UPS"
	}
	switch why {
	case power.CauseConverter:
		return "CONVERTER NOT REACHABLE at " + addr + ".\n" +
			"    It is off, unplugged, on another network, or the IP address or port is wrong."
	case power.CauseUnit:
		hint := "the RS485 wires (A/B), the slave ID, 9600 8N1 and that the converter is in Modbus gateway mode."
		if mdl.Protocol == "megatec" {
			hint = "the RS232 cable (try swapping TX and RX), 2400 8N1 and that the converter is in transparent mode."
		}
		return "CONVERTER OK, " + strings.ToUpper(unit) + " SILENT: the converter at " + addr +
			" answers, but the " + unit + " behind it does not.\n" +
			"    The " + unit + " is off, or check " + hint
	}
	return "no usable reply from " + addr + "."
}

func report(cs []power.Change) {
	for _, c := range cs {
		switch {
		case c.Began && c.Initial:
			fmt.Printf("    >> %s (already so when polling began)\n", c.Text)
		case c.Began:
			fmt.Printf("    >> %s began\n", c.Text)
		default:
			fmt.Printf("    >> %s ended after %s\n", c.Text, time.Duration(c.At-c.Since)*time.Millisecond)
		}
	}
}

// interactive is set when the probe was started with no options and asked its
// questions: it then waits before closing on an error.
var interactive bool

func ask(in *bufio.Reader, q, def string) string {
	for {
		if def != "" {
			fmt.Printf("%s [%s]: ", q, def)
		} else {
			fmt.Printf("%s: ", q)
		}
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			line = def
		}
		if line != "" {
			return line
		}
		if err != nil {
			fail("no answer to %q", q)
		}
	}
}

func askInt(in *bufio.Reader, q string, def int) int {
	for {
		v, err := strconv.Atoi(ask(in, q, strconv.Itoa(def)))
		if err == nil {
			return v
		}
		fmt.Println("  a number, please")
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "powerprobe: "+format+"\n", a...)
	if interactive {
		fmt.Fprint(os.Stderr, "\nPress Enter to close.")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	os.Exit(2)
}
