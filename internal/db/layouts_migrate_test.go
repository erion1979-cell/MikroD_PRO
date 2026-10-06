package db

import "testing"

// MIGRATION 36 KEEPS EVERY SAVED LAYOUT, ends in the fresh shape, and can run
// twice.
func TestMigrationThirtySixKeepsEveryLayout(t *testing.T) {
	d := openTest(t, t.TempDir())
	shape := func() string {
		t.Helper()
		var s string
		if err := d.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'user_layouts'`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	fresh := shape()

	// Wind back to the table as 35 left it, holding rows of all three kinds.
	cfgExec(t, d,
		`DROP TABLE user_layouts`,
		`CREATE TABLE "user_layouts" (
          user_id    TEXT NOT NULL,
          kind       TEXT NOT NULL CHECK (kind IN ('dashboard','topology','nav')),
          data       TEXT NOT NULL,
          updated_at INTEGER NOT NULL,
          PRIMARY KEY (user_id, kind)
        )`,
		`INSERT INTO user_layouts VALUES ('u-1','dashboard','{"cards":[1]}',1),
		   ('u-1','topology','{"r":{}}',2), ('u-2','nav','{"a":1}',3)`,
		`DELETE FROM schema_version WHERE version >= 36`)
	if err := d.SetLayout("u-1", "dashboards", map[string]any{}); err == nil {
		t.Fatal("the wound-back table accepted the new kind; this test proves nothing")
	}

	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := shape(); got != fresh {
		t.Errorf("migrated:\n%s\nfresh:\n%s", got, fresh)
	}
	for _, c := range []struct{ user, kind, want string }{
		{"u-1", "dashboard", `{"cards":[1]}`}, {"u-1", "topology", `{"r":{}}`}, {"u-2", "nav", `{"a":1}`},
	} {
		if got, err := d.Layout(c.user, c.kind); err != nil || string(got) != c.want {
			t.Errorf("%s %s after the migration: %s %v, want %s", c.user, c.kind, got, err, c.want)
		}
	}
	if err := d.SetLayout("u-1", "dashboards", map[string]any{"list": []any{}}); err != nil {
		t.Errorf("the migrated table refuses dashboards: %v", err)
	}

	cfgExec(t, d, `DELETE FROM schema_version WHERE version >= 36`)
	if _, err := d.Migrate(); err != nil {
		t.Fatalf("migration 36 failed on a database already in its shape: %v", err)
	}
	if got, _ := d.Layout("u-1", "dashboards"); string(got) != `{"list":[]}` {
		t.Errorf("a second run lost the dashboards row: %s", got)
	}
}
