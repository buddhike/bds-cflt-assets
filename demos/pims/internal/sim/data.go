package sim

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/brianvoe/gofakeit/v7"
)

// provider is a clinician who takes appointments in a department.
type provider struct {
	Name       string
	Department string
}

// providers staff the simulated clinic. Each holds one appointment per
// slot, which bounds daily capacity the way a real roster would.
var providers = []provider{
	{"Dr Amelia Hart", "General Practice"},
	{"Dr Rajesh Iyer", "General Practice"},
	{"Dr Sofia Moreno", "General Practice"},
	{"Dr Liam O'Connor", "Paediatrics"},
	{"Dr Mei Chen", "Paediatrics"},
	{"Dr Kwame Mensah", "Cardiology"},
	{"Dr Hannah Schultz", "Dermatology"},
	{"Dr Yusuf Rahman", "Orthopaedics"},
	{"Nurse Grace Kim", "Immunisation"},
}

// emergencyProvider sees walk-in emergency patients.
var emergencyProvider = provider{"Dr Elena Petrova", "Emergency"}

var appointmentReasons = map[string][]string{
	"General Practice": {"Annual check-up", "Persistent cough", "Blood pressure review", "Medication review", "Fatigue", "Back pain", "Follow-up consultation"},
	"Paediatrics":      {"Child wellness check", "Ear infection", "Fever", "Growth assessment", "Asthma review"},
	"Cardiology":       {"Chest pain assessment", "ECG follow-up", "Palpitations", "Hypertension management"},
	"Dermatology":      {"Skin rash", "Mole check", "Eczema flare-up", "Acne treatment"},
	"Orthopaedics":     {"Knee pain", "Post-fracture review", "Shoulder injury", "Sports injury"},
	"Immunisation":     {"Influenza vaccination", "Travel vaccination", "Tetanus booster"},
}

var walkInReasons = []string{"Minor laceration", "Sprained ankle", "Sore throat", "Urinary tract infection", "Allergic reaction"}

var emergencyReasons = []string{"Severe abdominal pain", "Shortness of breath", "Head injury", "High fever", "Chest pain"}

// diagnoses are ICD-10 codes recorded when a visit completes.
var diagnoses = []string{"J06.9", "I10", "E11.9", "M54.5", "J45.909", "L30.9", "S93.401A", "R50.9", "Z00.00", "Z23", "N39.0", "K21.9"}

// generator produces realistic, reproducible patient data.
type generator struct {
	rng  *rand.Rand
	fake *gofakeit.Faker
}

func newGenerator(seed uint64) *generator {
	return &generator{
		rng:  rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		fake: gofakeit.New(seed),
	}
}

func (g *generator) pick(xs []string) string { return xs[g.rng.IntN(len(xs))] }

func (g *generator) chance(p float64) bool { return g.rng.Float64() < p }

// patient generates a new patient registered at time now.
func (g *generator) patient(now time.Time) Patient {
	sex := "F"
	switch r := g.rng.Float64(); {
	case r < 0.49:
		sex = "M"
	case r < 0.51:
		sex = "X"
	}
	first := g.fake.FirstName()
	last := g.fake.LastName()
	addr := g.fake.Address()
	return Patient{
		MRN:         fmt.Sprintf("M%09d", g.rng.Int64N(1_000_000_000)),
		FirstName:   first,
		LastName:    last,
		DateOfBirth: g.dateOfBirth(now),
		Sex:         sex,
		Phone:       g.fake.Phone(),
		Email:       g.email(first, last),
		Address:     addr.Street,
		City:        addr.City,
		State:       addr.State,
		Postcode:    addr.Zip,
	}
}

// dateOfBirth draws ages from a rough population pyramid: more adults
// than children or the very old.
func (g *generator) dateOfBirth(now time.Time) time.Time {
	var age int
	switch r := g.rng.Float64(); {
	case r < 0.18:
		age = g.rng.IntN(18)
	case r < 0.83:
		age = 18 + g.rng.IntN(47)
	default:
		age = 65 + g.rng.IntN(31)
	}
	dob := now.AddDate(-age, 0, -g.rng.IntN(365))
	return time.Date(dob.Year(), dob.Month(), dob.Day(), 0, 0, 0, 0, time.UTC)
}

func (g *generator) email(first, last string) string {
	if g.chance(0.12) {
		return "" // not everyone gives an email address
	}
	return fmt.Sprintf("%s.%s%d@%s", lower(first), lower(last), g.rng.IntN(100), g.pick([]string{"example.com", "example.org", "example.net"}))
}

func (g *generator) contactChange() ContactChange {
	addr := g.fake.Address()
	return ContactChange{Phone: g.fake.Phone(), Address: addr.Street, City: addr.City, State: addr.State, Postcode: addr.Zip}
}

func (g *generator) appointmentDuration(department string) time.Duration {
	switch department {
	case "Immunisation":
		return 15 * time.Minute
	case "Cardiology", "Orthopaedics":
		return 30 * time.Minute
	}
	if g.chance(0.25) {
		return 30 * time.Minute
	}
	return 15 * time.Minute
}

func lower(s string) string {
	b := []byte(s)
	out := b[:0]
	for _, c := range b {
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+'a'-'A')
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		}
	}
	return string(out)
}
