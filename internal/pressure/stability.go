package pressure

import (
	"errors"
	"sync"
	"time"

	"github.com/wyw14/cry-98/internal/model"
)

type Result struct {
	VesselID  string    `json:"vessel_id"`
	Epoch     uint64    `json:"epoch"`
	Stable    bool      `json:"stable"`
	Samples   int       `json:"samples"`
	CheckedAt time.Time `json:"checked_at"`
}

type Monitor struct {
	mu      sync.RWMutex
	windows map[string]*Window
	now     func() time.Time
}

func NewMonitor(now func() time.Time) *Monitor {
	if now == nil {
		now = time.Now
	}
	return &Monitor{windows: make(map[string]*Window), now: now}
}

func (m *Monitor) AddVessel(vesselID string, capacity int, spread float64) error {
	window, err := NewWindow(vesselID, capacity, spread)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.windows[vesselID] = window
	m.mu.Unlock()
	return nil
}

func (m *Monitor) Reset(vesselID string, epoch uint64) error {
	m.mu.RLock()
	window := m.windows[vesselID]
	m.mu.RUnlock()
	if window == nil {
		return errors.New("pressure window not found")
	}
	window.Reset(epoch)
	return nil
}

func (m *Monitor) Add(sample model.Sample) error {
	m.mu.RLock()
	window := m.windows[sample.VesselID]
	m.mu.RUnlock()
	if window == nil {
		return errors.New("pressure window not found")
	}
	if !window.Add(sample) {
		return errors.New("sample was rejected by pressure window")
	}
	return nil
}

func (m *Monitor) Evaluate(vesselID string) (Result, error) {
	m.mu.RLock()
	window := m.windows[vesselID]
	m.mu.RUnlock()
	if window == nil {
		return Result{}, errors.New("pressure window not found")
	}
	return Result{
		VesselID:  vesselID,
		Epoch:     window.Epoch(),
		Stable:    window.Stable(),
		Samples:   len(window.Samples()),
		CheckedAt: m.now().UTC(),
	}, nil
}

// EvaluateSince assesses the soak window using only samples observed at or
// after since (the soak start), all from the same calibration generation, that
// span at least minDuration. It does not accept samples accumulated before the
// soak began.
func (m *Monitor) EvaluateSince(vesselID string, since time.Time, minDuration time.Duration) (Result, error) {
	m.mu.RLock()
	window := m.windows[vesselID]
	m.mu.RUnlock()
	if window == nil {
		return Result{}, errors.New("pressure window not found")
	}
	samples := window.Samples()
	fresh := 0
	for _, sample := range samples {
		if sample.CalibrationEpoch == window.Epoch() && !sample.ObservedAt.Before(since) {
			fresh++
		}
	}
	return Result{
		VesselID:  vesselID,
		Epoch:     window.Epoch(),
		Stable:    window.StableSince(since, minDuration),
		Samples:   fresh,
		CheckedAt: m.now().UTC(),
	}, nil
}
