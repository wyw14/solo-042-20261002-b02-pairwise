package fill

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wyw14/cry-98/internal/circuit"
	"github.com/wyw14/cry-98/internal/model"
	"github.com/wyw14/cry-98/internal/pressure"
	"github.com/wyw14/cry-98/internal/valve"
	"github.com/wyw14/cry-98/internal/vessel"
)

type recordingTransport struct {
	mu     sync.Mutex
	opens  int
	closes int
	now    func() time.Time
}

func (t2 *recordingTransport) Execute(_ context.Context, command model.ValveCommand) (model.ValveReceipt, error) {
	t2.mu.Lock()
	defer t2.mu.Unlock()
	switch command.Action {
	case model.ValveOpen:
		t2.opens++
	case model.ValveClose:
		t2.closes++
	}
	return model.ValveReceipt{CommandID: command.ID, Executed: true, ReceivedAt: t2.now().UTC()}, nil
}

func (t2 *recordingTransport) closeCount() int {
	t2.mu.Lock()
	defer t2.mu.Unlock()
	return t2.closes
}

type fakeAppender struct{}

func (fakeAppender) Append(event model.Event) (model.Event, error) { return event, nil }

type soakRig struct {
	coordinator *Coordinator
	completion  *CompletionService
	monitor     *pressure.Monitor
	transport   *recordingTransport
	clock       *time.Time
}

func newSoakRig(t *testing.T) soakRig {
	t.Helper()
	clock := time.Unix(1000, 0)
	now := func() time.Time { return clock }

	vesselRegistry := vessel.NewRegistry()
	v, err := model.NewVessel("V-07", "Cryogenic vessel 07", "north", clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := vesselRegistry.Register(v); err != nil {
		t.Fatal(err)
	}
	vesselState := vessel.NewStateService(vesselRegistry, now)

	circuitList := circuit.NewRegistry(circuit.Circuit{ID: "C-A", Zone: "north", ValveID: "VALVE-A", Enabled: true})
	circuitManager := circuit.NewManager("C-A")

	transport := &recordingTransport{now: now}
	controller, err := valve.NewController(transport, now)
	if err != nil {
		t.Fatal(err)
	}
	commands := valve.NewCommandService(controller, now)
	routes := valve.NewRouteService(commands)

	coordinator := NewCoordinator(circuitManager, circuitList, routes, vesselState, now)

	monitor := pressure.NewMonitor(now)
	policy := pressure.DefaultPolicy()
	if err := monitor.AddVessel("V-07", policy.RequiredSamples, policy.MaximumSpread, policy.MinimumDuration); err != nil {
		t.Fatal(err)
	}
	completion := NewCompletionService(coordinator, monitor, fakeAppender{}, now)

	return soakRig{coordinator: coordinator, completion: completion, monitor: monitor, transport: transport, clock: &clock}
}

func pressureSample(at time.Time) model.Sample {
	return model.Sample{
		ProbeID:          "P-P07",
		VesselID:         "V-07",
		Kind:             model.ProbePressure,
		Value:            100.0,
		CalibrationEpoch: 1,
		ObservedAt:       at.UTC(),
	}
}

// The reported defect: a stable window accumulated before refill, with no new
// readings after entering soaking, must not close the valve or complete.
func TestCompleteRejectsSoakWithoutFreshSamples(t *testing.T) {
	rig := newSoakRig(t)
	ctx := context.Background()

	session, err := rig.coordinator.StartManual(ctx, "V-07", 2.1)
	if err != nil {
		t.Fatal(err)
	}

	// Stable pressure history gathered during filling, long before soak.
	for i := 0; i < 5; i++ {
		*rig.clock = time.Unix(1000, 0).Add(time.Duration(i) * time.Second)
		if err := rig.monitor.Add(pressureSample(*rig.clock)); err != nil {
			t.Fatal(err)
		}
	}

	// Enter soaking; no further pressure readings arrive.
	*rig.clock = time.Unix(2000, 0)
	if _, err := rig.coordinator.BeginSoak(session.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := rig.completion.Complete(ctx, session.ID); err == nil {
		t.Fatal("completion succeeded without post-soak pressure samples")
	}

	if rig.transport.closeCount() != 0 {
		t.Fatalf("valve closed despite insufficient soak evidence: closes=%d", rig.transport.closeCount())
	}
	current, ok := rig.coordinator.Get(session.ID)
	if !ok {
		t.Fatal("session disappeared")
	}
	if current.State != model.FillSoaking {
		t.Fatalf("session left soaking state=%s", current.State)
	}

	// Fresh, same-epoch samples spanning the minimum duration now arrive.
	soakStart := *rig.clock
	for i := 0; i < 5; i++ {
		at := soakStart.Add(time.Duration(i) * time.Second)
		if err := rig.monitor.Add(pressureSample(at)); err != nil {
			t.Fatal(err)
		}
	}
	*rig.clock = soakStart.Add(5 * time.Second)

	completed, err := rig.completion.Complete(ctx, session.ID)
	if err != nil {
		t.Fatalf("completion failed once fresh soak samples arrived: %v", err)
	}
	if completed.State != model.FillCompleted {
		t.Fatalf("state=%s", completed.State)
	}
	if rig.transport.closeCount() != 1 {
		t.Fatalf("expected exactly one valve close, got %d", rig.transport.closeCount())
	}
}
