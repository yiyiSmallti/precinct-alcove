package pkg

import "testing"

func TestSatisfiesPrerelease(t *testing.T) {
	cases := []struct {
		v, r string
		want bool
	}{
		{"0.2.1-rc.5", ">=0.2.1-rc.5 <0.3.0", true},
		{"0.2.1-rc.7", ">=0.2.1-rc.5 <0.3.0", true},
		{"0.2.2-rc.1", ">=0.2.1-rc.5 <0.3.0", false}, // other tuple
		{"1.5.0-beta.1", "1.0.0 - 2.0.0", false},     // hyphen range, not a prerelease
		{"1.5.0", "1.0.0 - 2.0.0", true},
		{"1.0.0-rc.1", "^2.0.0 || >=1.0.0-rc.0 <1.1.0", true},
		{"1.0.0-rc.1", ">=1.0.0-rc.0 <1.1.0 || ^0.9.0", true},
		{"1.0.0-rc.1", "^0.9.0 || 0.9.0 - 2.0.0", false},
	}
	for _, c := range cases {
		got, err := Satisfies(c.v, c.r)
		if err != nil || got != c.want {
			t.Errorf("Satisfies(%q, %q) = %v, %v; want %v", c.v, c.r, got, err, c.want)
		}
	}
}
