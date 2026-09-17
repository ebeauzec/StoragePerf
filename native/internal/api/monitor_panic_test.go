package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"plumb/internal/config"
	"plumb/internal/findingstore"
	"plumb/internal/maintenance"
	"plumb/internal/notify"
)

// TestMonitorOneArrayRecoversFromPanic proves the actual production
// concern this session's audit found: RunMonitor runs as a single,
// unprotected background goroutine (plain `go app.RunMonitor(...)` in
// main.go, no recover anywhere above it) iterating every configured
// array. Before monitorOneArray's own defer/recover was added, a panic
// evaluating any ONE array — entirely plausible on real-world data this
// was never able to test against live hardware for every vendor/version
// combination — would have crashed the whole process and silently
// stopped monitoring every other array too.
//
// This drives a genuine panic through the real call path (rules.
// EvaluateArray dereferencing a nil *vm.Client, the same as a real
// initialization-order bug would produce) rather than asserting recover()
// works in the abstract — Go's panic/recover semantics aren't in
// question; whether THIS function's defer is placed correctly to catch
// what the real code path can throw is.
func TestMonitorOneArrayRecoversFromPanic(t *testing.T) {
	configDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configDir, "thresholds"), 0o755); err != nil {
		t.Fatal(err)
	}
	minimalThresholds := `
metrics:
  - id: host_latency
    category: frontend
    label: "Host Latency"
    unit: ms
    query: 'avg(host_latency{array="{array}"})'
    comparison: gt
    severity_watch: 5
    severity_critical: 15
    threshold_label: "test"
    finding:
      watch: "test watch"
      critical: "test critical"
`
	if err := os.WriteFile(filepath.Join(configDir, "thresholds", "pure_flasharray.yml"), []byte(minimalThresholds), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	store, err := findingstore.Open(dataDir)
	if err != nil {
		t.Fatalf("opening findingstore: %v", err)
	}

	app := &App{
		ConfigDir: configDir,
		DataDir:   dataDir,
		VM:        nil, // deliberately nil: rules.EvaluateArray dereferences it, producing a real nil-pointer panic
		Findings:  store,
	}

	arr := config.Array{ID: "test-array", Name: "test-array", Vendor: config.VendorPureFlashArray}
	windows := []maintenance.Window{}
	cfg := notify.Config{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// If monitorOneArray's recover() is missing or misplaced, this
		// panic propagates out of the goroutine and crashes the whole
		// test binary — there is no "caught the panic" assertion to make
		// after the fact in that failure mode, which is exactly the
		// production failure mode this test exists to rule out. A
		// completing goroutine (reaching the close(done) below via defer)
		// is the proof.
		app.monitorOneArray(arr, windows, cfg, time.Now())
	}()

	select {
	case <-done:
		// Success: monitorOneArray returned normally despite the nil-client
		// panic partway through, exactly like RunMonitor's real loop needs
		// it to for every other array to still get evaluated this tick.
	case <-time.After(10 * time.Second):
		t.Fatal("monitorOneArray did not return — panic recovery may have deadlocked or hung instead of returning")
	}
}
