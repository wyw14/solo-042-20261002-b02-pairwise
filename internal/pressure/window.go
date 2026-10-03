package pressure

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-98/internal/model"
)

type WindowSample struct {
	Value            float64   `json:"value"`
	CalibrationEpoch uint64    `json:"calibration_epoch"`
	ObservedAt       time.Time `json:"observed_at"`
}

type Window struct {
	mu              sync.RWMutex
	vesselID        string
	epoch           uint64
	capacity        int
	spread          float64
	minimumDuration time.Duration
	samples         []WindowSample
}

func NewWindow(vesselID string, capacity int, spread float64, minimumDuration time.Duration) (*Window, error) {
	if vesselID == "" || capacity < 2 || spread < 0 || minimumDuration < 0 {
		return nil, errors.New("invalid pressure window")
	}
	return &Window{
		vesselID:        vesselID,
		capacity:        capacity,
		spread:          spread,
		minimumDuration: minimumDuration,
	}, nil
}

func (w *Window) Reset(epoch uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.epoch = epoch
	w.samples = nil
}

func (w *Window) Add(sample model.Sample) bool {
	if sample.Kind != model.ProbePressure || sample.VesselID != w.vesselID {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.epoch == 0 {
		w.epoch = sample.CalibrationEpoch
	}
	if sample.CalibrationEpoch != w.epoch {
		w.epoch = sample.CalibrationEpoch
		w.samples = nil
	}
	w.samples = append(w.samples, WindowSample{
		Value:            sample.Value,
		CalibrationEpoch: sample.CalibrationEpoch,
		ObservedAt:       sample.ObservedAt,
	})
	if len(w.samples) > w.capacity {
		w.samples = w.samples[len(w.samples)-w.capacity:]
	}
	return true
}

// qualifying returns the samples that are admissible for a soak that began at
// soakStart: only samples observed at or after soak entry and belonging to the
// window's current calibration epoch. Samples are returned oldest-first.
func (w *Window) qualifying(soakStart time.Time) []WindowSample {
	eligible := make([]WindowSample, 0, len(w.samples))
	for _, sample := range w.samples {
		if !sample.ObservedAt.Before(soakStart) && sample.CalibrationEpoch == w.epoch {
			eligible = append(eligible, sample)
		}
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ObservedAt.Before(eligible[j].ObservedAt) })
	return eligible
}

// Stable reports whether the window as a whole currently holds a full set of
// same-epoch samples within the allowed spread. It does not account for the
// soak boundary or the minimum duration; use StableSince for soak verification.
func (w *Window) Stable() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.isStable(w.samples)
}

// StableSince reports whether the samples gathered at or after soakStart meet
// every stability requirement: enough same-calibration-epoch samples, a spread
// within the limit, and coverage of at least the policy's minimum duration.
func (w *Window) StableSince(soakStart time.Time) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	eligible := w.qualifying(soakStart)
	return w.isStable(eligible)
}

func (w *Window) isStable(samples []WindowSample) bool {
	if len(samples) < w.capacity {
		return false
	}
	minimum, maximum := samples[0].Value, samples[0].Value
	for _, sample := range samples[1:] {
		minimum = minFloat(minimum, sample.Value)
		maximum = maxFloat(maximum, sample.Value)
		if sample.CalibrationEpoch != w.epoch {
			return false
		}
	}
	if maximum-minimum > w.spread {
		return false
	}
	if w.minimumDuration > 0 {
		span := samples[len(samples)-1].ObservedAt.Sub(samples[0].ObservedAt)
		if span < w.minimumDuration {
			return false
		}
	}
	return true
}

func (w *Window) Samples() []WindowSample {
	w.mu.RLock()
	defer w.mu.RUnlock()
	result := append([]WindowSample(nil), w.samples...)
	sort.Slice(result, func(i, j int) bool { return result[i].ObservedAt.Before(result[j].ObservedAt) })
	return result
}

// SamplesSince returns the same-epoch samples observed at or after soakStart,
// oldest-first.
func (w *Window) SamplesSince(soakStart time.Time) []WindowSample {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.qualifying(soakStart)
}

func (w *Window) Epoch() uint64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.epoch
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
