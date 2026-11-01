package host

import "testing"

func TestStoreNameDistinct(t *testing.T) {
	a := storeName("@foo/bar", "1.0.0", "aaaaaaaaaaaaaaaaaaaa")
	b := storeName("foo__bar", "1.0.0", "bbbbbbbbbbbbbbbbbbbb")
	if a == b {
		t.Fatalf("collision: %s", a)
	}
}
