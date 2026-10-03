package pressure

import (
	"testing"
	"time"

	"github.com/wyw14/cry-98/internal/model"
)

func sample(vesselID string, value float64, epoch uint64, at time.Time) model.Sample {
	return model.Sample{
		ProbeID:          "P-P07",
		VesselID:         vesselID,
		Kind:             model.ProbePressure,
		Value:            value,
		CalibrationEpoch: epoch,
		ObservedAt:       at.UTC(),
	}
}

// Stable readings accumulated before the soak must not verify it once the fill
// enters soaking with no fresh readings.
func TestStableSinceRejectsPreSoakSamples(t *testing.T) {
	window, err := NewWindow("V-07", 3, 1.0, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1000, 0)
	for i := 0; i < 3; i++ {
		if !window.Add(sample("V-07", 100.0, 1, base.Add(time.Duration(i)*time.Second))) {
			t.Fatal("sample rejected")
		}
	}
	if !window.Stable() {
		t.Fatal("precondition: window should be stable on its full contents")
	}
	soakStart := base.Add(10 * time.Second)
	if window.StableSince(soakStart) {
		t.Fatal("soak verified without any post-soak samples")
	}
}

// Enough fresh, same-epoch samples spanning the minimum duration verify it.
func TestStableSinceAcceptsFreshSameEpochSamples(t *testing.T) {
	window, err := NewWindow("V-07", 3, 1.0, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1000, 0)
	soakStart := base.Add(10 * time.Second)
	for i := 0; i < 3; i++ {
		if !window.Add(sample("V-07", 100.0, 1, soakStart.Add(time.Duration(i)*time.Second))) {
			t.Fatal("sample rejected")
		}
	}
	if !window.StableSince(soakStart) {
		t.Fatal("fresh same-epoch samples spanning the duration were rejected")
	}
}

// Enough fresh samples that arrive inside the minimum duration must not verify.
func TestStableSinceRejectsInsufficientDuration(t *testing.T) {
	window, err := NewWindow("V-07", 3, 1.0, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1000, 0)
	soakStart := base
	for i := 0; i < 3; i++ {
		if !window.Add(sample("V-07", 100.0, 1, soakStart.Add(time.Duration(i)*time.Second))) {
			t.Fatal("sample rejected")
		}
	}
	if window.StableSince(soakStart) {
		t.Fatal("soak verified without covering the minimum duration")
	}
}

// A calibration change after soak entry wipes the pre-change samples, so the
// soak must not be verified on mixed or stale epochs.
func TestStableSinceRejectsMixedCalibrationEpoch(t *testing.T) {
	window, err := NewWindow("V-07", 3, 1.0, 0)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1000, 0)
	soakStart := base
	for i := 0; i < 2; i++ {
		if !window.Add(sample("V-07", 100.0, 1, soakStart.Add(time.Duration(i)*time.Second))) {
			t.Fatal("sample rejected")
		}
	}
	// Recalibration clears the window; only a single epoch-2 sample remains.
	if !window.Add(sample("V-07", 100.0, 2, soakStart.Add(2*time.Second))) {
		t.Fatal("sample rejected")
	}
	if window.StableSince(soakStart) {
		t.Fatal("soak verified across a calibration generation change")
	}
}

func TestMonitorEvaluateUsesSoakBoundary(t *testing.T) {
	monitor := NewMonitor(func() time.Time { return time.Unix(2000, 0) })
	if err := monitor.AddVessel("V-07", 3, 1.0, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1000, 0)
	for i := 0; i < 3; i++ {
		if err := monitor.Add(sample("V-07", 100.0, 1, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	soakStart := base.Add(10 * time.Second)
	result, err := monitor.Evaluate("V-07", soakStart)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stable || result.Samples != 0 {
		t.Fatalf("pre-soak data counted: stable=%v samples=%d", result.Stable, result.Samples)
	}
}
