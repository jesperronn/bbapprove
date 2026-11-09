package update

import "testing"

func mustV(t *testing.T, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

func TestCompare(t *testing.T) {
	// each row: a < b; the reverse must be > and a vs a must be 0
	lt := [][2]string{
		{"1.0.0-rc.2", "1.0.0-rc.10"},
		{"v1.0.0-rc.10", "v1.0.0"},
		{"1.0.0-alpha", "1.0.0-alpha.1"},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta"},
		{"1.0.0-alpha.beta", "1.0.0-beta"},
		{"1.0.0-beta.2", "1.0.0-beta.11"},
		{"1.0.0-beta.11", "1.0.0-rc.1"},
		{"1.0.0-1", "1.0.0-a"}, // numeric below alphanumeric
		{"0.9.9", "1.0.0-rc.1"},
		{"1.2.3", "1.10.0"},
		{"v1.0.0", "v1.0.1"},
		{"1.0.0-99999999999999999999", "1.0.0-100000000000000000000"}, // no overflow
	}
	for _, c := range lt {
		a, b := mustV(t, c[0]), mustV(t, c[1])
		if Compare(a, b) != -1 || Compare(b, a) != 1 || Compare(a, a) != 0 {
			t.Errorf("want %s < %s", c[0], c[1])
		}
	}
	eq := [][2]string{
		{"1.0.0-rc.10+build", "1.0.0-rc.10"},
		{"v1.0.0+a", "1.0.0+b"},
		{"v1.0.0-rc.1", "1.0.0-rc.1"},
	}
	for _, c := range eq {
		if Compare(mustV(t, c[0]), mustV(t, c[1])) != 0 {
			t.Errorf("want %s == %s", c[0], c[1])
		}
	}
}

func TestParseVersionErrors(t *testing.T) {
	for _, s := range []string{"", "dev", "1.0", "1.0.0.0", "a.b.c", "1.0.0-", "1.0.0-a..b", "1.0.0-a_b", "-1.0.0", "1.-1.0"} {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("ParseVersion(%q) should fail", s)
		}
	}
}

func TestParseCurrent(t *testing.T) {
	cases := []struct {
		in         string
		known, dev bool
		base       string
		ahead      int
		dirty      bool
	}{
		{"v1.0.0-rc.2", true, false, "v1.0.0-rc.2", 0, false},
		{"v1.0.0", true, false, "v1.0.0", 0, false},
		{"v1.0.0-rc.2-4-gabc1234", true, true, "v1.0.0-rc.2", 4, false},
		{"v1.0.0-rc.2-4-gabc1234-dirty", true, true, "v1.0.0-rc.2", 4, true},
		{"v1.2.3-17-g0123abc", true, true, "v1.2.3", 17, false},
		{"v1.0.0-dirty", true, true, "v1.0.0", 0, true},
		{"dev", false, false, "", 0, false},
		{"abc1234", false, false, "", 0, false},
		{"", false, false, "", 0, false},
	}
	for _, c := range cases {
		got := ParseCurrent(c.in)
		if got.Known != c.known || got.Dev != c.dev || got.Ahead != c.ahead || got.Dirty != c.dirty ||
			(c.known && got.Base.String() != c.base) {
			t.Errorf("ParseCurrent(%q) = %+v", c.in, got)
		}
	}
}
