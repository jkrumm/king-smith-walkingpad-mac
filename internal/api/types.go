package api

// Wire types for request/response JSON. PRD §8 is the source of truth for
// field names and shapes.

type healthResponse struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
}

type okResponse struct {
	OK bool `json:"ok"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type speedRequest struct {
	SpeedKmh float64 `json:"speed_kmh"`
}

type sampleJSON struct {
	Ts        string  `json:"ts"`
	BeltState string  `json:"belt_state,omitempty"`
	SpeedKmh  float64 `json:"speed_kmh"`
	DistanceM float64 `json:"distance_m"`
	Steps     int64   `json:"steps"`
}

type currentSessionJSON struct {
	UUID        string       `json:"uuid"`
	StartedAt   string       `json:"started_at"`
	DurationS   int64        `json:"duration_s"`
	DistanceM   float64      `json:"distance_m"`
	Steps       int64        `json:"steps"`
	Kcal        float64      `json:"kcal"`
	AvgSpeedKmh float64      `json:"avg_speed_kmh"`
	MaxSpeedKmh float64      `json:"max_speed_kmh"`
	Samples     []sampleJSON `json:"samples"`
}

type summaryJSON struct {
	DurationS int64   `json:"duration_s"`
	DistanceM float64 `json:"distance_m"`
	Steps     int64   `json:"steps"`
	Kcal      float64 `json:"kcal"`
}

type deviceJSON struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	RSSI    int    `json:"rssi"`
}

type statusResponse struct {
	Connected      bool                `json:"connected"`
	BeltState      string              `json:"belt_state,omitempty"`
	Mode           string              `json:"mode,omitempty"`
	SpeedKmh       float64             `json:"speed_kmh,omitempty"`
	ObservedAt     string              `json:"observed_at,omitempty"`
	CurrentSession *currentSessionJSON `json:"current_session"`
	Today          summaryJSON         `json:"today"`
	Device         deviceJSON          `json:"device"`
}

type sessionJSON struct {
	UUID        string  `json:"uuid"`
	StartedAt   string  `json:"started_at"`
	EndedAt     *string `json:"ended_at"`
	DurationS   int64   `json:"duration_s"`
	DistanceM   float64 `json:"distance_m"`
	Steps       int64   `json:"steps"`
	AvgSpeedKmh float64 `json:"avg_speed_kmh"`
	MaxSpeedKmh float64 `json:"max_speed_kmh"`
	Kcal        float64 `json:"kcal"`
	PauseCount  int64   `json:"pause_count"`
	SyncedAt    *string `json:"synced_at"`
}

type sessionsListResponse struct {
	Sessions []sessionJSON `json:"sessions"`
}

type sessionDetailResponse struct {
	Session sessionJSON  `json:"session"`
	Samples []sampleJSON `json:"samples"`
}

type summaryResponse struct {
	Period    string  `json:"period"`
	Sessions  int64   `json:"sessions"`
	DurationS int64   `json:"duration_s"`
	DistanceM float64 `json:"distance_m"`
	Steps     int64   `json:"steps"`
	Kcal      float64 `json:"kcal"`
}

type syncResponse struct {
	Synced int `json:"synced"`
	Failed int `json:"failed"`
}
