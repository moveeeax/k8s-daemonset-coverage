package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/moveeeax/k8s-daemonset-coverage/internal/coverage"
)

const snapshot = "../../examples/cluster-snapshot.json"

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func reportFrom(t *testing.T, args ...string) (int, coverage.Report) {
	t.Helper()
	code, stdout, stderr := runCLI(t, append([]string{"--snapshot", snapshot, "--format", "json"}, args...)...)
	var rep coverage.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not JSON: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return code, rep
}

func dsReport(t *testing.T, rep coverage.Report, namespace, name string) coverage.DaemonSetReport {
	t.Helper()
	for _, ds := range rep.DaemonSets {
		if ds.Namespace == namespace && ds.Name == name {
			return ds
		}
	}
	t.Fatalf("no report for %s/%s", namespace, name)
	return coverage.DaemonSetReport{}
}

func TestExampleSnapshotCoverage(t *testing.T) {
	code, rep := reportFrom(t)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 without thresholds", code)
	}
	if rep.ClusterNodes != 6 || rep.ScopeNodes != 6 {
		t.Fatalf("nodes = %d scope %d, want 6/6", rep.ClusterNodes, rep.ScopeNodes)
	}

	fluent := dsReport(t, rep, "logging", "fluent-bit")
	if fluent.CoveredNodes != 2 || fluent.CoveragePct != 33.3 {
		t.Fatalf("fluent-bit = %d covered / %.1f%%, want 2 / 33.3", fluent.CoveredNodes, fluent.CoveragePct)
	}
	reasons := map[string]string{}
	for _, f := range fluent.Uncovered {
		reasons[f.Node] = f.Reason
	}
	want := map[string]string{
		"ip-10-0-2-21": coverage.ReasonPodNotReady,
		"ip-10-0-2-22": "node-unschedulable",
		"ip-10-0-3-31": "untolerated-taint",
		"ip-10-0-0-5":  "untolerated-taint",
	}
	for node, reason := range want {
		if reasons[node] != reason {
			t.Errorf("%s reason = %q, want %q", node, reasons[node], reason)
		}
	}

	exporter := dsReport(t, rep, "monitoring", "node-exporter")
	if exporter.CoveredNodes != 4 || exporter.CoveragePct != 66.7 {
		t.Fatalf("node-exporter = %d covered / %.1f%%, want 4 / 66.7", exporter.CoveredNodes, exporter.CoveragePct)
	}
	if exporter.PendingPods != 1 {
		t.Fatalf("node-exporter PendingPods = %d, want 1", exporter.PendingPods)
	}
}

func TestExpectLabelsDropsTheControlPlane(t *testing.T) {
	code, rep := reportFrom(t, "--expect-labels", "node-role.kubernetes.io/worker=")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if rep.ScopeNodes != 5 || rep.ClusterNodes != 6 {
		t.Fatalf("scope = %d of %d, want 5 of 6", rep.ScopeNodes, rep.ClusterNodes)
	}
	if got := dsReport(t, rep, "logging", "fluent-bit").CoveragePct; got != 40 {
		t.Fatalf("fluent-bit coveragePct = %v, want 40", got)
	}
	if got := dsReport(t, rep, "monitoring", "node-exporter").CoveragePct; got != 60 {
		t.Fatalf("node-exporter coveragePct = %v, want 60", got)
	}
}

func TestFailUnder(t *testing.T) {
	if code, _ := reportFrom(t, "--fail-under", "100"); code != 1 {
		t.Fatalf("--fail-under 100 exit code = %d, want 1", code)
	}
	if code, _ := reportFrom(t, "--fail-under", "0"); code != 0 {
		t.Fatalf("--fail-under 0 exit code = %d, want 0", code)
	}
}

func TestFailUnderIsSilentWhenEveryDaemonSetIsFull(t *testing.T) {
	code, stdout, _ := runCLI(t, "--snapshot", snapshot, "--format", "json",
		"--expect-labels", "topology.kubernetes.io/zone=eu-central-1a", "--fail-under", "100")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 — both DaemonSets fully cover zone a\n%s", code, stdout)
	}
}

func TestFailOnPending(t *testing.T) {
	code, _, stderr := runCLI(t, "--snapshot", snapshot, "--format", "json", "--fail-on-pending")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "fail-on-pending") {
		t.Fatalf("stderr should explain the failure, got %q", stderr)
	}
}

func TestTextOutputIsTheDefault(t *testing.T) {
	code, stdout, _ := runCLI(t, "--snapshot", snapshot)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, want := range []string{"DAEMONSET", "fluent-bit", "33.3%", "untolerated-taint", "nvidia.com/gpu"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("text output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestBadFlagsExitTwo(t *testing.T) {
	if code, _, _ := runCLI(t, "--snapshot", snapshot, "--format", "yaml"); code != 2 {
		t.Fatal("an unknown --format must exit 2")
	}
	if code, _, _ := runCLI(t, "--snapshot", "/nonexistent.json"); code != 2 {
		t.Fatal("a missing snapshot must exit 2")
	}
	if code, _, _ := runCLI(t, "--snapshot", snapshot, "--expect-labels", "=broken"); code != 2 {
		t.Fatal("a malformed --expect-labels must exit 2")
	}
	if code, _, _ := runCLI(t, "--definitely-not-a-flag"); code != 2 {
		t.Fatal("an unknown flag must exit 2")
	}
}

func TestVersion(t *testing.T) {
	code, stdout, _ := runCLI(t, "--version")
	if code != 0 || strings.TrimSpace(stdout) == "" {
		t.Fatalf("--version exit %d, stdout %q", code, stdout)
	}
}

func TestNamespaceShorthandAndLongFormShareOneValue(t *testing.T) {
	// The snapshot flag bypasses the API, so -n cannot change the result here;
	// this only guards that both spellings parse into the same variable.
	for _, flagName := range []string{"-n", "--namespace"} {
		if code, _, stderr := runCLI(t, "--snapshot", snapshot, flagName, "monitoring"); code != 0 {
			t.Fatalf("%s failed with exit %d: %s", flagName, code, stderr)
		}
	}
}
