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
// the mains plug, and "mains_lost began" should appear within one interval.
package main

import (
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

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "powerprobe: "+format+"\n", a...)
	os.Exit(2)
}
