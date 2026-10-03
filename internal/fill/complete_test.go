package fill

import (
	"context"
	"errors"
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
	mu      sync.Mutex
	actions []model.ValveAction
	now     func() time.Time
}

func (t *recordingTransport) Execute(_ context.Context, command model.ValveCommand) (model.ValveReceipt, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.actions = append(t.actions, command.Action)
	return model.ValveReceipt{CommandID: command.ID, Executed: true, ReceivedAt: t.now().UTC()}, nil
}

func (t *recordingTransport) count(action model.ValveAction) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, a := range t.actions {
		if a == action {
			n++
		}
	}
	return n
}

type memoryAppender struct{}

func (memoryAppender) Append(event model.Event) (model.Event, error) { return event, nil }

func pressureSample(value float64, epoch uint64, at time.Time) model.Sample {
	return model.Sample{
		ProbeID:          "P-P07",
		VesselID:         "V-07",
		Kind:             model.ProbePressure,
		Value:            value,
		CalibrationEpoch: epoch,
		ObservedAt:       at.UTC(),
	}
}

func newCompletionHarness(t *testing.T, now func() time.Time) (*Coordinator, *CompletionService, *pressure.Monitor, *recordingTransport, pressure.Policy, *vessel.StateService, *circuit.Manager) {
	t.Helper()

	vesselRegistry := vessel.NewRegistry()
	vesselModel, err := model.NewVessel("V-07", "Cryogenic vessel 07", "north", now())
	if err != nil {
		t.Fatalf("new vessel: %v", err)
	}
	if err := vesselRegistry.Register(vesselModel); err != nil {
		t.Fatalf("register vessel: %v", err)
	}
	vesselState := vessel.NewStateService(vesselRegistry, now)

	circuitRegistry := circuit.NewRegistry(
		circuit.Circuit{ID: "C-A", Zone: "north", ValveID: "VALVE-A", Enabled: true},
	)
	circuitManager := circuit.NewManager("C-A")

	transport := &recordingTransport{now: now}
	controller, err := valve.NewController(transport, now)
	if err != nil {
		t.Fatalf("new controller: %v", err)
	}
	commandService := valve.NewCommandService(controller, now)
	routeService := valve.NewRouteService(commandService)

	coordinator := NewCoordinator(circuitManager, circuitRegistry, routeService, vesselState, now)

	monitor := pressure.NewMonitor(now)
	policy := pressure.Policy{RequiredSamples: 5, MaximumSpread: 1.5, MinimumDuration: 4 * time.Second}
	if err := monitor.AddVessel("V-07", policy.RequiredSamples, policy.MaximumSpread); err != nil {
		t.Fatalf("add vessel: %v", err)
	}
	completion := NewCompletionService(coordinator, monitor, policy, memoryAppender{}, now)

	return coordinator, completion, monitor, transport, policy, vesselState, circuitManager
}

func TestCompleteRejectsStableSamplesAccumulatedBeforeSoak(t *testing.T) {
	clock := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }

	coordinator, completion, monitor, transport, _, vesselState, circuitManager := newCompletionHarness(t, now)
	ctx := context.Background()

	session, err := coordinator.StartManual(ctx, "V-07", 10.0)
	if err != nil {
		t.Fatalf("start fill: %v", err)
	}

	// Stable pressure readings accumulated while filling, BEFORE the soak.
	fillStart := clock
	for i := 0; i < 5; i++ {
		if err := monitor.Add(pressureSample(100.0+float64(i)*0.1, 1, fillStart.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("add pre-soak sample: %v", err)
		}
	}

	clock = fillStart.Add(30 * time.Second)
	soaking, err := coordinator.BeginSoak(session.ID)
	if err != nil {
		t.Fatalf("begin soak: %v", err)
	}
	if soaking.SoakStartedAt.IsZero() {
		t.Fatal("soak start time was not recorded")
	}

	// No new readings arrive during the soak: completion must be refused and
	// must neither close the valve nor release the circuit / finish the vessel.
	if _, err := completion.Complete(ctx, session.ID); !errors.Is(err, ErrSoakNotVerified) {
		t.Fatalf("expected ErrSoakNotVerified, got %v", err)
	}
	if got := transport.count(model.ValveClose); got != 0 {
		t.Fatalf("valve must not be closed without post-soak samples, close count=%d", got)
	}
	current, ok := coordinator.Get(session.ID)
	if !ok || current.State != model.FillSoaking {
		t.Fatalf("session must remain soaking, got %q (ok=%v)", current.State, ok)
	}
	if _, reserved := circuitManager.Get("C-A"); !reserved {
		t.Fatal("circuit must remain reserved after a refused completion")
	}
	if state, _ := vesselState.Registry().Get("V-07"); state.Status != model.VesselFilling {
		t.Fatalf("vessel must remain filling, got %q", state.Status)
	}
}

func TestCompleteAcceptsFreshSameGenerationSamplesSpanningDuration(t *testing.T) {
	clock := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }

	coordinator, completion, monitor, transport, _, vesselState, circuitManager := newCompletionHarness(t, now)
	ctx := context.Background()

	session, err := coordinator.StartManual(ctx, "V-07", 10.0)
	if err != nil {
		t.Fatalf("start fill: %v", err)
	}
	clock = clock.Add(30 * time.Second)
	soaking, err := coordinator.BeginSoak(session.ID)
	if err != nil {
		t.Fatalf("begin soak: %v", err)
	}

	// Fresh, same-generation samples spanning the minimum soak duration.
	for i := 0; i < 5; i++ {
		if err := monitor.Add(pressureSample(100.0+float64(i)*0.1, 1, soaking.SoakStartedAt.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("add soak sample: %v", err)
		}
	}

	completed, err := completion.Complete(ctx, session.ID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed.State != model.FillCompleted {
		t.Fatalf("expected completed state, got %q", completed.State)
	}
	if got := transport.count(model.ValveClose); got != 1 {
		t.Fatalf("expected exactly one valve close, got %d", got)
	}
	if _, reserved := circuitManager.Get("C-A"); reserved {
		t.Fatal("circuit should be released after completion")
	}
	if state, _ := vesselState.Registry().Get("V-07"); state.Status != model.VesselReady {
		t.Fatalf("vessel should return to ready, got %q", state.Status)
	}
}

func TestCompleteRejectsFreshSamplesFromNewCalibrationGeneration(t *testing.T) {
	clock := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }

	coordinator, completion, monitor, transport, _, _, _ := newCompletionHarness(t, now)
	ctx := context.Background()

	session, err := coordinator.StartManual(ctx, "V-07", 10.0)
	if err != nil {
		t.Fatalf("start fill: %v", err)
	}
	// Stable pre-soak readings from the original calibration.
	for i := 0; i < 5; i++ {
		if err := monitor.Add(pressureSample(100.0+float64(i)*0.1, 1, clock.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("add pre-soak sample: %v", err)
		}
	}
	clock = clock.Add(30 * time.Second)
	soaking, err := coordinator.BeginSoak(session.ID)
	if err != nil {
		t.Fatalf("begin soak: %v", err)
	}

	// Recalibration during the soak resets the window; the pre-soak epoch-1
	// readings must not count, and a few new-generation readings are insufficient.
	if err := monitor.Reset("V-07", 2); err != nil {
		t.Fatalf("reset calibration: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := monitor.Add(pressureSample(100.0, 2, soaking.SoakStartedAt.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("add recalibrated sample: %v", err)
		}
	}
	if _, err := completion.Complete(ctx, session.ID); !errors.Is(err, ErrSoakNotVerified) {
		t.Fatalf("expected ErrSoakNotVerified, got %v", err)
	}
	if got := transport.count(model.ValveClose); got != 0 {
		t.Fatalf("valve must not close on insufficient new-generation samples, close count=%d", got)
	}
}
