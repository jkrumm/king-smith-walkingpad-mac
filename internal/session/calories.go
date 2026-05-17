package session

// MET (metabolic equivalent of task) values for walking speeds, sourced from
// the Compendium of Physical Activities (Ainsworth et al., 2011 update — the
// industry-standard table used by Apple Health and most consumer fitness
// trackers). Values cover the P1's 0.5–6.0 km/h band; speeds beyond the
// hardware ceiling are clamped to the last bucket.
//
// Computed energy expenditure per second:
//
//	kcal_per_s = MET(speed_kmh) * weight_kg * (1/3600)
//
// MET below 2.0 km/h is dominated by basal-metabolic rate noise and is not
// meaningfully different from sitting; we floor at 2.0 MET there.
var metTable = []metPoint{
	{speed: 2.0, met: 2.0}, // very slow stroll
	{speed: 3.2, met: 2.8}, // slow walk
	{speed: 4.0, met: 3.0}, // moderate walk
	{speed: 4.8, met: 3.5}, // brisk walk
	{speed: 5.6, met: 4.3}, // very brisk
	{speed: 6.4, met: 5.0}, // light jog (P1 ceiling is 6.0)
}

type metPoint struct{ speed, met float64 }

// MET returns the metabolic-equivalent rate for the given belt speed. Linear
// interpolation between table entries; constant beyond the first and last bucket.
func MET(speedKmh float64) float64 {
	if speedKmh <= metTable[0].speed {
		return metTable[0].met
	}
	if speedKmh >= metTable[len(metTable)-1].speed {
		return metTable[len(metTable)-1].met
	}
	for i := 1; i < len(metTable); i++ {
		hi := metTable[i]
		if speedKmh < hi.speed {
			lo := metTable[i-1]
			t := (speedKmh - lo.speed) / (hi.speed - lo.speed)
			return lo.met + t*(hi.met-lo.met)
		}
	}
	return metTable[len(metTable)-1].met // unreachable; satisfy the compiler
}

// Kcal returns the energy expenditure for a single tick: MET × weight × dt.
// dtSeconds is the elapsed wall time across which the speed is assumed steady.
func Kcal(speedKmh, weightKg, dtSeconds float64) float64 {
	return MET(speedKmh) * weightKg * dtSeconds / 3600.0
}
