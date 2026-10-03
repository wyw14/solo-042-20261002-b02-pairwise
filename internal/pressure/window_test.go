package pressure

import (
	"testing"
	"time"

	"github.com/wyw14/cry-98/internal/model"
)

func sampleAt(vesselID string, epoch uint64, value float64, at time.Time) model.Sample {
	return model.Sample{
		ProbeID:          "P-P07",
		VesselID:         vesselID,
		Kind:             model.ProbePressure,
		Value:            value,
		CalibrationEpoch: epoch,
		ObservedAt:       at.UTC(),
	}
}

func TestStableSinceRejectsSamplesBeforeSoak(t *testing.T) {
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	soakStart := base.Add(10 * time.Second)

	window, err := NewWindow("V-07", 5, 1.5)
	if err != nil {
		t.Fatalf("new window: %v", err)
	}
	// A full, stable set of readings accumulated BEFORE the soak starts.
	for i := 0; i < 5; i++ {
		if !window.Add(sampleAt("V-07", 1, 100.0+float64(i)*0.1, base.Add(time.Duration(i)*time.Second))) {
			t.Fatalf("sample %d rejected", i)
		}
	}
	if !window.Stable() {
		t.Fatal("pre-soak window should be stable under the unbounded check")
	}
	if window.StableSince(soakStart, 4*time.Second) {
		t.Fatal("pre-soak samples must not verify the soak window")
	}

	// Only after enough post-soak samples spanning the minimum duration.
	for i := 0; i < 5; i++ {
		window.Add(sampleAt("V-07", 1, 100.0+float64(i)*0.1, soakStart.Add(time.Duration(i)*time.Second)))
	}
	if !window.StableSince(soakStart, 4*time.Second) {
		t.Fatal("post-soak samples spanning the minimum duration should be stable")
	}
}

func TestStableSinceEnforcesMinimumDuration(t *testing.T) {
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

	window, err := NewWindow("V-07", 3, 1.5)
	if err != nil {
		t.Fatalf("new window: %v", err)
	}
	for _, offset := range []time.Duration{0, time.Second, 2 * time.Second} {
		window.Add(sampleAt("V-07", 1, 100.0, base.Add(offset)))
	}
	if window.StableSince(base, 4*time.Second) {
		t.Fatal("samples within a 2s span must not satisfy a 4s minimum duration")
	}

	window, _ = NewWindow("V-07", 3, 1.5)
	for _, offset := range []time.Duration{0, 2 * time.Second, 4 * time.Second} {
		window.Add(sampleAt("V-07", 1, 100.0, base.Add(offset)))
	}
	if !window.StableSince(base, 4*time.Second) {
		t.Fatal("samples spanning exactly the minimum duration should be stable")
	}
}

func TestStableSinceRejectsNewCalibrationGeneration(t *testing.T) {
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

	window, err := NewWindow("V-07", 3, 1.5)
	if err != nil {
		t.Fatalf("new window: %v", err)
	}
	for i := 0; i < 3; i++ {
		window.Add(sampleAt("V-07", 1, 100.0, base.Add(time.Duration(i)*time.Second)))
	}
	// A recalibrated probe starts a new generation and clears the window.
	window.Add(sampleAt("V-07", 2, 100.0, base.Add(5*time.Second)))
	if window.StableSince(base, time.Duration(0)) {
		t.Fatal("a lone sample from a new calibration generation must not be stable")
	}
}
