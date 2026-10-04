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
// does not matter (docs/inverter/converters.md).
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
	raw := flag.Bool("raw", false, "also print every register as read")
	list := flag.Bool("models", false, "list the model definitions this build has, and exit")
	flag.Parse()
	if flag.NFlag() == 0 {
		interactive = true
		in := bufio.NewReader(os.Stdin)
		fmt.Println("MikroDash Power/UPS probe - reads one unit, never writes to it.")
		fmt.Println("The converter must be in Modbus TCP gateway mode.")
		fmt.Println()
		*host = ask(in, "Converter IP address", "")
		*port = askInt(in, "Port", 502)
		*slave = askInt(in, "Slave ID", 1)
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
	client := modbus.New(addr, *timeout)
	defer client.Close()
	tracker := power.NewTracker()
	fmt.Printf("Polling %s, slave %d, as %s %s. Read-only.\n\n", addr, *slave, mdl.ProducerName, mdl.ModelName)

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
		poll(client, mdl, byte(*slave), tracker, *raw)
	}
}

func poll(c *modbus.Client, mdl *model.Model, slave byte, tr *power.Tracker, raw bool) {
	began := time.Now()
	stamp := began.Format("15:04:05")
	regs := make([][]uint16, 0, len(mdl.Reads))
	for _, r := range mdl.Reads {
		got, err := c.Read(slave, r.Function, r.Start, r.Count)
		if err != nil {
			fmt.Printf("%s  NO READING: %v\n", stamp, err)
			report(tr.Failure(began.UnixMilli()))
			return
		}
		regs = append(regs, got)
	}
	reply := time.Since(began)
	r, err := mdl.Decode(regs)
	if err != nil {
		fmt.Printf("%s  UNDECODABLE: %v\n", stamp, err)
		report(tr.Failure(began.UnixMilli()))
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
	for _, k := range model.RawKeys {
		if v, ok := r.Raw[k]; ok {
			fmt.Printf("    %-22s %8d\n", k, v)
		}
	}
	if raw {
		for i, rd := range mdl.Reads {
			fmt.Printf("    raw fn%02d from %d: %v\n", rd.Function, rd.Start, regs[i])
		}
	}
	report(tr.Success(r, began.UnixMilli()))
	fmt.Println()
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
