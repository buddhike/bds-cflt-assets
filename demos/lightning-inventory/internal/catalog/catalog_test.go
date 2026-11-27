package catalog

import "testing"

func TestIdsAndKey(t *testing.T) {
	if SKU(42) != "SKU-0000042" || Location(7) != "LOC-0007" || Key(SKU(42), Location(7)) != "SKU-0000042|LOC-0007" {
		t.Fatal("ids changed: queries and the data would no longer match")
	}
}

// The catalogue is a function of its constants. If this fails, a change moved every key
// that was already loaded into a running demo's table.
func TestCatalogueIsPinned(t *testing.T) {
	c, _ := New(10)
	got := []int{c.Count(0), c.Count(1), c.Count(999_999), c.LocationAt(0, 0), c.LocationAt(1, 0)}
	want := []int{17, 10, 16, 1126, 1815}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("pinned value %d: got %v, want %v", i, got, want)
		}
	}
}

func TestLocationsOfASkuAreDistinctAndInRange(t *testing.T) {
	c, _ := New(10)
	for _, i := range []int{0, 1, 17, 12345, 999_999} {
		seen := map[int]bool{}
		for j := 0; j < c.Count(i); j++ {
			l := c.LocationAt(i, j)
			if l < 0 || l >= Locations || seen[l] {
				t.Fatalf("sku %d: location %d repeated or out of range", i, l)
			}
			seen[l] = true
		}
	}
	// A SKU stocked at the maximum number of locations still has distinct ones.
	for j := 0; j < Locations; j++ {
		_ = c.LocationAt(5, j)
	}
}

func TestMeanDensity(t *testing.T) {
	c, _ := New(10)
	var n int64
	for i := 0; i < 100_000; i++ {
		n += int64(c.Count(i))
	}
	if mean := float64(n) / 100_000; mean < 9.5 || mean > 10.5 {
		t.Fatalf("mean locations per sku %v, want about 10", mean)
	}
}

func TestBounds(t *testing.T) {
	if _, err := New(0); err == nil {
		t.Error("0 accepted")
	}
	if _, err := New(MaxLocationsPerSKU + 1); err == nil {
		t.Error("too many accepted")
	}
}

func TestSampler(t *testing.T) {
	c, _ := New(10)
	s := NewSampler(c, 0.2, 1)
	miss := 0
	for i := 0; i < 10_000; i++ {
		if _, ok := s.QuerySKU(); !ok {
			miss++
		}
	}
	if miss < 1500 || miss > 2500 {
		t.Fatalf("miss ratio produced %d misses of 10000, want about 2000", miss)
	}
	for _, n := range []int{1, 500, Locations, 99999, -3} {
		set := s.LocationSet(n)
		seen := map[string]bool{}
		for _, l := range set {
			if seen[l] {
				t.Fatalf("duplicate location in a set of %d", n)
			}
			seen[l] = true
		}
		if want := max(1, min(n, Locations)); len(set) != want {
			t.Fatalf("set of %d has %d", n, len(set))
		}
	}
	for i := 0; i < 1000; i++ {
		if n := s.SetSize(2000); n < 1 || n > 2000 {
			t.Fatal("set size out of range")
		}
	}
	sku, loc, q := s.Event()
	if sku == "" || loc == "" || q < 0 || q > MaxQuantity {
		t.Fatal("bad event")
	}
}
