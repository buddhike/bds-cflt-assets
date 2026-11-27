// Package catalog defines the inventory population the demo produces and queries: which
// SKUs exist, which locations stock each one, and how a query picks its keys.
//
// The population is a pure function of these constants, so the simulator, the loader and
// the query generator, on any number of tasks, agree on it without sharing a file.
package catalog

import (
	"fmt"
	"math/rand/v2"
	"strconv"
)

const (
	// SKUs is the number of SKUs: SKU-0000000 .. SKU-0999999.
	SKUs = 1_000_000
	// Locations is the number of locations: LOC-0000 .. LOC-1999.
	Locations = 2000
	// Shards is how many parts the one-off load of the catalogue is split in. Control
	// leases a shard to a producer task; SKU i belongs to shard i % Shards.
	Shards = 64
	// MaxQuantity is the largest quantity a simulated position holds.
	MaxQuantity = 500

	// DefaultLocationsPerSKU is the mean number of locations that stock a SKU.
	DefaultLocationsPerSKU = 10
	// MaxLocationsPerSKU bounds LOCATIONS_PER_SKU: the catalogue must stay loadable.
	MaxLocationsPerSKU = 100
)

// Catalog is the population for one density.
type Catalog struct {
	avg int
}

// New returns the catalogue where a SKU is stocked at 1..2*avg-1 locations (mean avg).
func New(avgLocationsPerSKU int) (Catalog, error) {
	if avgLocationsPerSKU < 1 || avgLocationsPerSKU > MaxLocationsPerSKU {
		return Catalog{}, fmt.Errorf("locations per SKU must be 1..%d", MaxLocationsPerSKU)
	}
	return Catalog{avg: avgLocationsPerSKU}, nil
}

// SKU is the id of SKU number i.
func SKU(i int) string { return "SKU-" + pad(i, 7) }

// Location is the id of location number j.
func Location(j int) string { return "LOC-" + pad(j, 4) }

// Key is the Kafka record key of a position: one per sku and location pair.
func Key(sku, location string) string { return sku + "|" + location }

func pad(n, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// mix is splitmix64: a fixed, fast hash, so the catalogue never changes between builds.
func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// Count is how many locations stock SKU number i.
func (c Catalog) Count(i int) int {
	return 1 + int(mix(uint64(i))%uint64(2*c.avg-1))
}

// LocationAt is the j-th location (j < Count(i)) that stocks SKU number i. The locations
// of a SKU are distinct: they walk the ring of Locations with a step coprime to it.
func (c Catalog) LocationAt(i, j int) int {
	h := mix(uint64(i) ^ 0xabcdef12345)
	start := int(h % Locations)
	step := 2*int((h>>20)%(Locations/4)) + 1
	for step%5 == 0 {
		step += 2
	}
	return (start + j*step) % Locations
}

// Pairs is how many sku and location pairs are in the catalogue.
func (c Catalog) Pairs() int64 {
	var n int64
	for i := 0; i < SKUs; i++ {
		n += int64(c.Count(i))
	}
	return n
}

// ShardOf is the shard SKU number i belongs to.
func ShardOf(i int) int { return i % Shards }

// Sampler picks keys for queries and live events. It is not safe for concurrent use:
// give every goroutine its own.
type Sampler struct {
	c         Catalog
	rnd       *rand.Rand
	missRatio float64
	perm      [Locations]int
}

// NewSampler returns a Sampler. missRatio is the share of queries on a SKU that does not
// exist.
func NewSampler(c Catalog, missRatio float64, seed uint64) *Sampler {
	s := &Sampler{c: c, rnd: rand.New(rand.NewPCG(seed, seed^0x5851f42d4c957f2d)), missRatio: missRatio}
	for i := range s.perm {
		s.perm[i] = i
	}
	return s
}

// QuerySKU returns the SKU for a query: one in the catalogue, or, with probability
// missRatio, one that is not (SKU-1000000 and above).
func (s *Sampler) QuerySKU() (sku string, exists bool) {
	if s.rnd.Float64() < s.missRatio {
		return SKU(SKUs + s.rnd.IntN(SKUs)), false
	}
	return SKU(s.rnd.IntN(SKUs)), true
}

// LocationSet returns n distinct location ids chosen at random (n is clamped to
// 1..Locations).
func (s *Sampler) LocationSet(n int) []string {
	n = max(1, min(n, Locations))
	out := make([]string, n)
	for i := 0; i < n; i++ {
		j := i + s.rnd.IntN(Locations-i)
		s.perm[i], s.perm[j] = s.perm[j], s.perm[i]
		out[i] = Location(s.perm[i])
	}
	return out
}

// SetSize returns a random number of locations for a by_locations query, 1..max.
func (s *Sampler) SetSize(max int) int { return 1 + s.rnd.IntN(max) }

// Event returns a random catalogue pair and a new quantity for a live event.
func (s *Sampler) Event() (sku, location string, quantity int) {
	i := s.rnd.IntN(SKUs)
	j := s.rnd.IntN(s.c.Count(i))
	return SKU(i), Location(s.c.LocationAt(i, j)), s.rnd.IntN(MaxQuantity + 1)
}

// Quantity returns a random quantity.
func (s *Sampler) Quantity() int { return s.rnd.IntN(MaxQuantity + 1) }
