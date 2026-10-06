package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type dashboardsReply struct {
	OK         bool       `json:"ok"`
	Dashboards Dashboards `json:"dashboards"`
}

func TestDashboardsNeedASessionAndTheDashboardPage(t *testing.T) {
	h, token := signedInServer(t, "a-password-for-dashboards")
	for _, c := range []struct{ method, body string }{{"GET", ""}, {"POST", `{"list":[]}`}} {
		if rec := layoutReq(t, h, c.method, "/api/dashboards", c.body, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with no cookie: %d, want 401", c.method, rec.Code)
		}
		if rec := layoutReq(t, h, c.method, "/api/dashboards", c.body, token); rec.Code != http.StatusForbidden {
			t.Errorf("%s with no grant: %d, want 403", c.method, rec.Code)
		}
	}
}

func TestDashboardsRoundTripTidied(t *testing.T) {
	h, token, _ := grantedServer(t, "a-password-for-dashboards")
	get := func() Dashboards {
		t.Helper()
		rec := layoutReq(t, h, "GET", "/api/dashboards", "", token)
		var r dashboardsReply
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &r) != nil {
			t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
		}
		return r.Dashboards
	}
	// Nothing stored: the first dashboard's default name and no others.
	if d := get(); d.MainName != "Overview" || d.List == nil || len(d.List) != 0 {
		t.Fatalf("empty: %+v", d)
	}
	rec := layoutReq(t, h, "POST", "/api/dashboards", `{"mainName":"  Main  ","list":[
	  {"id":"ho","name":" Head Office ","cards":[
	    {"uid":"card-traffic-x1","type":"card-traffic","router":"r-B","iface":"ether1","x":1,"y":1,"w":24,"h":5},
	    {"uid":"dc-card-power","type":"dc-card-power","router":"","x":1,"y":6,"w":8,"h":5}]},
	  {"id":"spare","name":"Spare"}]}`, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	d := get()
	if d.MainName != "Main" || len(d.List) != 2 || d.List[0].Name != "Head Office" ||
		len(d.List[0].Cards) != 2 || d.List[0].Cards[1].Type != "dc-card-power" ||
		d.List[0].Cards[0].Router != "r-B" || d.List[0].Cards[0].Iface != "ether1" {
		t.Errorf("after save: %+v", d)
	}
	if d.List[1].Cards == nil {
		t.Error("a dashboard with no cards reads back null, not []")
	}
	// The first dashboard's own layout is a different row, and untouched.
	if rec := layoutReq(t, h, "GET", "/api/dashboard-layout", "", token); strings.TrimSpace(rec.Body.String()) != "null" {
		t.Errorf("dashboard-layout after saving dashboards: %s, want null", rec.Body.String())
	}
}

func TestDashboardsAreRefusedWhenMalformed(t *testing.T) {
	card := func(over string) string {
		return `{"uid":"c1","type":"card-traffic","router":"","x":1,"y":1,"w":4,"h":4` + over + `}`
	}
	// The name is JSON-encoded, so a control character reaches the decoder
	// as one rather than as an escape JSON does not have.
	dash := func(id, name, cards string) string {
		n, _ := json.Marshal(name)
		return fmt.Sprintf(`{"id":%q,"name":%s,"cards":[%s]}`, id, n, cards)
	}
	many := make([]string, 20)
	for i := range many {
		many[i] = dash(fmt.Sprintf("d%d", i), "D", "")
	}
	// Nine devices named by cards; nine cards following the selection are not.
	devices, followers := make([]string, 9), make([]string, 9)
	for i := range devices {
		devices[i] = fmt.Sprintf(`{"uid":"c%d","type":"card-system","router":"r-%d","x":1,"y":%d,"w":1,"h":1}`, i, i, i+1)
		followers[i] = fmt.Sprintf(`{"uid":"c%d","type":"card-system","router":"","x":1,"y":%d,"w":1,"h":1}`, i, i+1)
	}
	if _, msg := cleanDashboards(mustDashboards(t, `{"list":[`+dash("a", "x", strings.Join(devices[:8], ","))+
		`,`+dash("b", "y", strings.Join(followers, ","))+`]}`)); msg != "" {
		t.Errorf("8 devices, and 9 cards following the selection, were refused: %s", msg)
	}
	cases := map[string]string{
		"9 devices":               dash("a", "x", strings.Join(devices, ",")),
		"a malformed id":          dash("Head Office", "x", ""),
		"a repeated id":           dash("a", "x", "") + "," + dash("a", "y", ""),
		"no name":                 dash("a", "   ", ""),
		"a control character":     dash("a", "x\x07", ""),
		"a long name":             dash("a", strings.Repeat("n", 41), ""),
		"an unknown card type":    dash("a", "x", card(`,"type":"card-nope"`)),
		"an original on a device": dash("a", "x", card(`,"type":"dc-card-power","router":"r-A"`)),
		"a malformed device":      dash("a", "x", card(`,"router":"r A"`)),
		"an interface on System":  dash("a", "x", card(`,"type":"card-system","iface":"ether1"`)),
		"a long interface":        dash("a", "x", card(`,"iface":"`+strings.Repeat("e", 65)+`"`)),
		"a card off the right":    dash("a", "x", card(`,"x":22,"w":4`)),
		"a card at row zero":      dash("a", "x", card(`,"y":0`)),
		"a repeated card uid":     dash("a", "x", card("")+","+card("")),
		"a malformed card uid":    dash("a", "x", card(`,"uid":"Card 1"`)),
		"a card at column 0":      dash("a", "x", card(`,"x":0`)),
		"a card 0 wide":           dash("a", "x", card(`,"w":0`)),
		"a card 0 high":           dash("a", "x", card(`,"h":0`)),
		"a card 101 high":         dash("a", "x", card(`,"h":101`)),
		"a card at row 501":       dash("a", "x", card(`,"y":501`)),
		"101 cards":               dash("a", "x", hundredAndOne()),
		"21 dashboards":           strings.Join(many, ","),
	}
	for what, list := range cases {
		if _, msg := cleanDashboards(mustDashboards(t, `{"list":[`+list+`]}`)); msg == "" {
			t.Errorf("%s was accepted", what)
		}
	}
	// 19 others plus the first is the most there can be, and the route stores it.
	h, token, _ := grantedServer(t, "a-password-for-dashboards")
	if rec := layoutReq(t, h, "POST", "/api/dashboards", `{"list":[`+strings.Join(many[:19], ",")+`]}`, token); rec.Code != http.StatusOK {
		t.Errorf("20 dashboards in all: %d %s", rec.Code, rec.Body.String())
	}
	if rec := layoutReq(t, h, "POST", "/api/dashboards", `{"list":[`+cases["an original on a device"]+`]}`, token); rec.Code != http.StatusBadRequest {
		t.Errorf("a refused list through the route: %d, want 400", rec.Code)
	}
}

func mustDashboards(t *testing.T, s string) Dashboards {
	t.Helper()
	var d Dashboards
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return d
}

func hundredAndOne() string {
	out := make([]string, 101)
	for i := range out {
		out[i] = fmt.Sprintf(`{"uid":"c%d","type":"card-traffic","router":"","x":1,"y":%d,"w":1,"h":1}`, i, i+1)
	}
	return strings.Join(out, ",")
}
