package power

// NoticeKey is the notification-catalogue key (internal/alert/types.go) a
// condition of this kind is sent as, or "" for a kind that is shown on the page
// but not sent: the warning and error bit registers, whose meaning the
// manufacturer has not documented, so a message could say nothing useful.
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
var Kinds = []Kind{KindMainsLost, KindOutputOff, KindEvent, KindBatteryLow, KindWarningBits,
	KindErrorBits, KindNotResponding}
