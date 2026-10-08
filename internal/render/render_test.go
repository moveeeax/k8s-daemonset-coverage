package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/moveeeax/k8s-daemonset-coverage/internal/coverage"
)

func sample() coverage.Report {
	return coverage.Report{
		ClusterNodes: 3,
		ScopeNodes:   2,
		DaemonSets: []coverage.DaemonSetReport{{
			Namespace:    "monitoring",
			Name:         "node-exporter",
			ScopeNodes:   2,
			CoveredNodes: 1,
			CoveragePct:  50,
			Uncovered: []coverage.NodeFinding{{
				Node:   "gpu-1",
				Reason: "untolerated-taint",
				Detail: "taint nvidia.com/gpu=true:NoSchedule is not tolerated",
			}},
		}},
	}
}

func TestTextMentionsCoverageAndReason(t *testing.T) {
	var buf bytes.Buffer
	if err := Text(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"node-exporter", "50.0%", "gpu-1", "untolerated-taint", "nvidia.com/gpu", "2 of 3 cluster nodes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestTextWithoutDaemonSets(t *testing.T) {
	var buf bytes.Buffer
	if err := Text(&buf, coverage.Report{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no DaemonSets found") {
		t.Fatalf("unexpected output: %q", buf.String())
	}
}

func TestJSONRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	var back coverage.Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if back.DaemonSets[0].CoveragePct != 50 {
		t.Fatalf("coveragePct survived as %v", back.DaemonSets[0].CoveragePct)
	}
	if back.DaemonSets[0].Uncovered[0].Node != "gpu-1" {
		t.Fatalf("uncovered node survived as %q", back.DaemonSets[0].Uncovered[0].Node)
	}
	if !strings.Contains(buf.String(), `"coveragePct"`) {
		t.Fatal("JSON keys must stay stable for jq pipelines")
	}
}
