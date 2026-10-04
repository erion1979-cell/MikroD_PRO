package rbac

import "testing"

func TestCanPageOnSites(t *testing.T) {
	r := build(t, seed{
		roles:     [][2]string{{"admin", "1"}, {"viewer", "0"}},
		rolePages: [][3]string{{"viewer", "backups", "read"}},
		grants: [][4]string{
			{"user", "u-site", "site", "site-1"},
			{"user", "u-router", "router", "r-A"},
			{"user", "u-global", "global", ""},
			{"user", "u-admin", "site", "site-2"},
			{"group", "g1", "site", "site-1"},
		},
		grantRoles: []string{"viewer", "viewer", "viewer", "admin", "viewer"},
		members:    [][2]string{{"g1", "u-grp"}},
	})
	cases := []struct {
		user, page, access string
		sites              []string
		want               bool
	}{
		{"u-site", "backups", "read", []string{"site-1"}, true},
		{"u-site", "backups", "read", []string{"site-9", "site-1"}, true}, // any of its sites
		{"u-site", "backups", "read", []string{"site-2"}, false},
		{"u-site", "backups", "read", nil, false}, // no site: global only
		{"u-site", "backups", "write", []string{"site-1"}, false},
		{"u-grp", "backups", "read", []string{"site-1"}, true}, // through a group
		{"u-global", "backups", "read", nil, true},
		// A grant on a router reaches nothing that only has sites, including
		// a site id that happens to be empty.
		{"u-router", "backups", "read", []string{"site-1"}, false},
		{"u-router", "backups", "read", []string{""}, false},
		{"u-admin", "backups", "write", []string{"site-2"}, true}, // builtin: every page
		{"u-admin", "backups", "write", []string{"site-1"}, false},
		{"u-admin", "no-such-page", "read", []string{"site-2"}, false},
		{"u-admin", "backups", "bogus", []string{"site-2"}, false},
		{"", "backups", "read", []string{"site-1"}, false},
		{"u-none", "backups", "read", []string{"site-1"}, false},
	}
	for _, c := range cases {
		got, err := r.CanPageOnSites(c.user, c.page, c.access, c.sites)
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		if got != c.want {
			t.Errorf("%s %s/%s on %v = %v, want %v", c.user, c.page, c.access, c.sites, got, c.want)
		}
	}
}
