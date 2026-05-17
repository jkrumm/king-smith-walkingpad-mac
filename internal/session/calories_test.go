package session

import (
	"math"
	"testing"
)

func TestMET_TableValues(t *testing.T) {
	// Exact table hits must return the listed values.
	tests := []struct {
		speed, want float64
	}{
		{2.0, 2.0},
		{3.2, 2.8},
		{4.0, 3.0},
		{4.8, 3.5},
		{5.6, 4.3},
		{6.4, 5.0},
	}
	for _, tt := range tests {
		if got := MET(tt.speed); math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("MET(%g) = %g, want %g", tt.speed, got, tt.want)
		}
	}
}

func TestMET_Interpolates(t *testing.T) {
	// Midpoint of 4.0→4.8 bucket should yield midpoint of 3.0→3.5 = 3.25.
	got := MET(4.4)
	want := 3.25
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("MET(4.4) = %g, want %g", got, want)
	}
}

func TestMET_ClampsAtBounds(t *testing.T) {
	if MET(0.0) != 2.0 {
		t.Errorf("MET(0.0) = %g, want floor 2.0", MET(0.0))
	}
	if MET(10.0) != 5.0 {
		t.Errorf("MET(10.0) = %g, want ceiling 5.0", MET(10.0))
	}
}

func TestKcal(t *testing.T) {
	// 4 km/h × 80 kg × 1 hour = 3.0 MET × 80 = 240 kcal/h.
	got := Kcal(4.0, 80.0, 3600.0)
	want := 240.0
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("Kcal = %g, want %g", got, want)
	}
}
