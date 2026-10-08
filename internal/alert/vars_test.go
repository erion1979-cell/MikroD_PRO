package alert

import "testing"

// EVERY VARIABLE THE SETTINGS PAGE OFFERS IS FILLED by the alert it belongs
// to. They were listed for years and filled by nothing, so a template using
// {{comment}} rendered "Commented: " with nothing after it.
func TestEachAlertFillsItsTemplateVariables(t *testing.T) {
	r := Router{ID: "r1", AlertsEnabled: true}
	e := NewEvaluator(&memStore{})
	one := func(t *testing.T, got []Fired) map[string]string {
		t.Helper()
		if len(got) != 1 {
			t.Fatalf("fired %d alerts, want 1: %+v", len(got), got)
		}
		return got[0].Vars
	}
	want := func(t *testing.T, got map[string]string, kv ...string) {
		t.Helper()
		for i := 0; i < len(kv); i += 2 {
			if got[kv[i]] != kv[i+1] {
				t.Errorf("{{%s}} = %q, want %q (all: %v)", kv[i], got[kv[i]], kv[i+1], got)
			}
		}
	}

	t.Run("interface", func(t *testing.T) {
		up := Interface{Name: "ether6", Comment: "Rack01 uplink", Running: true}
		e.IfstatusUpdate(r, []Interface{up})
		down := up
		down.Running = false
		want(t, one(t, e.IfstatusUpdate(r, []Interface{down})), "ifaceName", "ether6", "status", "down", "comment", "Rack01 uplink")
		want(t, one(t, e.IfstatusUpdate(r, []Interface{up})), "status", "up", "comment", "Rack01 uplink")
	})
	t.Run("netwatch", func(t *testing.T) {
		h := NetwatchHost{ID: "*1", Host: "198.51.100.7", Name: "camera", Status: "up", Comment: "Gate"}
		e.NetwatchUpdate(r, []NetwatchHost{h})
		h.Status = "down"
		want(t, one(t, e.NetwatchUpdate(r, []NetwatchHost{h})), "netwatchName", "camera", "host", "198.51.100.7",
			"status", "down", "comment", "Gate")
	})
	t.Run("vpn", func(t *testing.T) {
		e.VPNUpdate(r, []VPNTunnel{{Name: "branch", State: "active", Comment: "Office B"}})
		want(t, one(t, e.VPNUpdate(r, []VPNTunnel{{Name: "branch", State: "stale", Comment: "Office B"}})),
			"vpnPeer", "branch", "status", "stale", "comment", "Office B")
	})
	t.Run("bgp", func(t *testing.T) {
		zero := 0.0
		p := BGPPeer{Key: "k", Name: "isp", RemoteAddr: "198.51.100.1", Description: "Transit A",
			State: "established", Prefixes: &zero, HoldTime: &zero, Keepalive: &zero}
		e.RoutingUpdate(r, []BGPPeer{p})
		p.State = "idle"
		want(t, one(t, e.RoutingUpdate(r, []BGPPeer{p})), "bgpPeer", "isp", "host", "198.51.100.1",
			"status", "idle", "comment", "Transit A")
	})
	t.Run("cpu and ping", func(t *testing.T) {
		cpu := 91.5
		want(t, one(t, e.SystemCPUOnly(r, &cpu)), "cpuLoad", "91.5")
		target, loss, rtt := "1.1.1.1", 40.0, 23.4
		want(t, one(t, e.PingUpdate(r, &target, &loss, &rtt)), "pingTarget", "1.1.1.1", "pingLoss", "40", "pingRtt", "23.4")
	})
	t.Run("router", func(t *testing.T) {
		want(t, one(t, e.RouterStatus(r, false)), "status", "offline")
	})
}
