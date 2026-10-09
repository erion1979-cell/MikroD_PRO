package power

// NoticeKey is the notification-catalogue key (internal/alert/types.go) a
// condition of this kind is sent as, or "" for a kind this build does not raise:
// "warning_bits" and "error_bits", which earlier builds recorded from registers
// 033/034 and an older database may still hold open. The manufacturer advises
// the event code in 035 instead (2026-10-09), so they are no longer read.
func NoticeKey(k Kind) string {
	switch k {
	case KindMainsLost:
		return "power_mains_lost"
	case KindEvent:
		return "power_event"
	case KindOutputOff:
		return "power_output_off"
	case KindBatteryLow:
		return "power_battery_low"
	case KindNotResponding:
		return "power_not_responding"
	}
	return ""
}

// Kinds is every kind of condition, for the ledger that holds NoticeKey and
// the catalogue to each other.
var Kinds = []Kind{KindMainsLost, KindOutputOff, KindEvent, KindBatteryLow, KindNotResponding}
