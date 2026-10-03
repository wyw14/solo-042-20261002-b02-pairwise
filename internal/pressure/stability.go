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

func (m *Monitor) AddVessel(vesselID string, capacity int, spread float64, minimumDuration time.Duration) error {
	window, err := NewWindow(vesselID, capacity, spread, minimumDuration)
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

// Evaluate judges soak stability using only pressure samples observed at or
// after soakStart that share the window's current calibration epoch. Samples
// that accumulated before the fill entered soaking never count toward
// completion.
func (m *Monitor) Evaluate(vesselID string, soakStart time.Time) (Result, error) {
	m.mu.RLock()
	window := m.windows[vesselID]
	m.mu.RUnlock()
	if window == nil {
		return Result{}, errors.New("pressure window not found")
	}
	return Result{
		VesselID:  vesselID,
		Epoch:     window.Epoch(),
		Stable:    window.StableSince(soakStart),
		Samples:   len(window.SamplesSince(soakStart)),
		CheckedAt: m.now().UTC(),
	}, nil
}
