package server

import (
	"slices"
	"testing"
)

func TestRingKeepsMostRecent(t *testing.T) {
	r := newRing[int](3)
	for i := range 5 {
		r.push(i)
	}
	if got := r.items(); !slices.Equal(got, []int{2, 3, 4}) {
		t.Fatalf("items() = %v, want [2 3 4]", got)
	}
	empty := newRing[int](2)
	if len(empty.items()) != 0 {
		t.Fatal("new ring should be empty")
	}
}
