package power

import (
	"sort"

	"mikrodash/internal/power/model"
)

// One history row per unit per minute, holding the average, minimum and maximum
// of each measure. Polling every 5 s gives twelve readings a minute; storing all
// of them would make the database twelve times bigger and the charts no better,
// while keeping the minimum and maximum keeps a short dip visible.
//
// Pure, like track.go: the caller supplies the time.

// Stat is one measure over one minute.
type Stat struct {
	Key string
	Avg float64
	Min float64
	Max float64
}

// Minute is one finished minute of one unit.
type Minute struct {
	// Start is the minute's first millisecond, Unix time.
	Start int64
	// Polls is every poll attempted in the minute, OK the ones that answered,
	// which together give the success rate.
	Polls int
	OK    int
	// ReplyMs is the mean reply time of the polls that answered; 0 when none did.
	ReplyMs float64
	// Stats has one entry per measure seen, sorted by key. Empty when no poll
	// in the minute answered.
	Stats []Stat
}

// Bucket accumulates one unit's polls into minutes.
type Bucket struct {
	start   int64
	active  bool
	polls   int
	ok      int
	replyMs float64
	acc     map[string]*accum
}

type accum struct {
	sum, min, max float64
	n             int
}

func minuteOf(ms int64) int64 { return ms - ms%60000 }

// Add records a reading taken at now that took replyMs to arrive. It returns
// the previous minute when now has moved past it.
func (b *Bucket) Add(r model.Reading, replyMs float64, now int64) *Minute {
	done := b.roll(now)
	b.polls++
	b.ok++
	b.replyMs += replyMs
	for k, v := range r.Values {
		a := b.acc[k]
		if a == nil {
			b.acc[k] = &accum{sum: v, min: v, max: v, n: 1}
			continue
		}
		a.sum += v
		a.n++
		a.min = min(a.min, v)
		a.max = max(a.max, v)
	}
	return done
}

// Fail records a poll at now that got no reply.
func (b *Bucket) Fail(now int64) *Minute {
	done := b.roll(now)
	b.polls++
	return done
}

// Flush returns the minute in progress, if any, and empties the bucket. For
// shutdown, or when a unit is removed or reconfigured.
func (b *Bucket) Flush() *Minute {
	if !b.active {
		return nil
	}
	m := b.finish()
	b.active = false
	return m
}

// roll starts the minute holding now, returning the one it replaces.
func (b *Bucket) roll(now int64) *Minute {
	start := minuteOf(now)
	if b.active && start == b.start {
		return nil
	}
	var done *Minute
	if b.active {
		done = b.finish()
	}
	*b = Bucket{start: start, active: true, acc: map[string]*accum{}}
	return done
}

func (b *Bucket) finish() *Minute {
	m := &Minute{Start: b.start, Polls: b.polls, OK: b.ok, Stats: []Stat{}}
	if b.ok > 0 {
		m.ReplyMs = b.replyMs / float64(b.ok)
	}
	for k, a := range b.acc {
		m.Stats = append(m.Stats, Stat{Key: k, Avg: a.sum / float64(a.n), Min: a.min, Max: a.max})
	}
	sort.Slice(m.Stats, func(i, j int) bool { return m.Stats[i].Key < m.Stats[j].Key })
	return m
}
