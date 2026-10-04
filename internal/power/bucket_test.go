package power

import (
	"testing"

	"mikrodash/internal/power/model"
)

func values(kv map[string]float64) model.Reading { return model.Reading{Values: kv} }

func TestAMinuteKeepsAverageMinimumAndMaximum(t *testing.T) {
	var b Bucket
	base := int64(120_000) // the start of minute 2
	for i, v := range []float64{228, 230, 0, 226} {
		if done := b.Add(values(map[string]float64{"input_v": v}), 100+float64(i)*20, base+int64(i)*5000); done != nil {
			t.Fatalf("poll %d finished a minute early: %+v", i, done)
		}
	}
	b.Fail(base + 20000)
	m := b.Add(values(map[string]float64{"input_v": 229}), 100, base+60_000)
	if m == nil {
		t.Fatal("moving into the next minute did not finish the last one")
	}
	if m.Start != base || m.Polls != 5 || m.OK != 4 {
		t.Errorf("start %d polls %d ok %d, want %d 5 4", m.Start, m.Polls, m.OK, base)
	}
	if m.ReplyMs != 130 {
		t.Errorf("reply %v ms, want the mean of the answered polls, 130", m.ReplyMs)
	}
	if len(m.Stats) != 1 {
		t.Fatalf("stats %+v", m.Stats)
	}
	// The dip to 0 V survives as the minimum.
	if s := m.Stats[0]; s.Key != "input_v" || s.Avg != 171 || s.Min != 0 || s.Max != 230 {
		t.Errorf("stat %+v, want input_v avg 171 min 0 max 230", s)
	}
}

func TestAMinuteWithNoAnswersStillCountsItsPolls(t *testing.T) {
	var b Bucket
	b.Fail(60_000)
	b.Fail(65_000)
	m := b.Fail(120_000)
	if m == nil || m.Polls != 2 || m.OK != 0 || m.ReplyMs != 0 || len(m.Stats) != 0 || m.Stats == nil {
		t.Errorf("%+v, want two failed polls and an empty, non-nil stat list", m)
	}
}

func TestStatsAreSortedAndMinutesDoNotLeak(t *testing.T) {
	var b Bucket
	b.Add(values(map[string]float64{"output_v": 220, "battery_v": 13.8, "input_v": 230}), 100, 0)
	m := b.Add(values(map[string]float64{"input_v": 1}), 100, 60_000)
	if len(m.Stats) != 3 || m.Stats[0].Key != "battery_v" || m.Stats[2].Key != "output_v" {
		t.Errorf("stats %+v, want three sorted by key", m.Stats)
	}
	last := b.Flush()
	if last == nil || last.Start != 60_000 || len(last.Stats) != 1 || last.Stats[0].Avg != 1 {
		t.Errorf("flush %+v, want only the second minute's one reading", last)
	}
	if b.Flush() != nil {
		t.Error("a second flush returned a minute")
	}
}
