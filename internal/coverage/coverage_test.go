package coverage

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func node(name string, labels map[string]string) corev1.Node {
	n := corev1.Node{}
	n.Name = name
	n.Labels = labels
	return n
}

func cordoned(name string) corev1.Node {
	n := node(name, nil)
	n.Spec.Unschedulable = true
	return n
}

func tainted(name, key, value string) corev1.Node {
	n := node(name, nil)
	n.Spec.Taints = []corev1.Taint{{Key: key, Value: value, Effect: corev1.TaintEffectNoSchedule}}
	return n
}

func ds(name string, mutate ...func(*appsv1.DaemonSet)) appsv1.DaemonSet {
	d := appsv1.DaemonSet{}
	d.Namespace = "monitoring"
	d.Name = name
	d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}
	d.Spec.Template.Labels = map[string]string{"app": name}
	for _, m := range mutate {
		m(&d)
	}
	return d
}

func pod(name, nodeName, app string, phase corev1.PodPhase, ready bool) corev1.Pod {
	p := corev1.Pod{}
	p.Namespace = "monitoring"
	p.Name = name
	p.Labels = map[string]string{"app": app}
	p.Spec.NodeName = nodeName
	p.Status.Phase = phase
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}
	p.OwnerReferences = []metav1.OwnerReference{{Kind: "DaemonSet", Name: app}}
	return p
}

func readyPodsOn(app string, nodes ...string) []corev1.Pod {
	var out []corev1.Pod
	for _, n := range nodes {
		out = append(out, pod(app+"-"+n, n, app, corev1.PodRunning, true))
	}
	return out
}

func findingFor(t *testing.T, rep DaemonSetReport, nodeName string) NodeFinding {
	t.Helper()
	for _, f := range rep.Uncovered {
		if f.Node == nodeName {
			return f
		}
	}
	t.Fatalf("no finding for node %s (uncovered: %+v)", nodeName, rep.Uncovered)
	return NodeFinding{}
}

func TestFullCoverage(t *testing.T) {
	in := Input{
		Nodes:      []corev1.Node{node("n1", nil), node("n2", nil)},
		DaemonSets: []appsv1.DaemonSet{ds("node-exporter")},
		Pods:       readyPodsOn("node-exporter", "n1", "n2"),
	}
	rep := Analyze(in, Options{})
	if rep.DaemonSets[0].CoveragePct != 100 {
		t.Fatalf("coveragePct = %v, want 100", rep.DaemonSets[0].CoveragePct)
	}
	if len(rep.DaemonSets[0].Uncovered) != 0 {
		t.Fatalf("expected no findings, got %+v", rep.DaemonSets[0].Uncovered)
	}
}

func TestUntoleratedTaintIsReportedWithTheKey(t *testing.T) {
	in := Input{
		Nodes:      []corev1.Node{node("n1", nil), tainted("gpu-1", "nvidia.com/gpu", "true")},
		DaemonSets: []appsv1.DaemonSet{ds("fluent-bit")},
		Pods:       readyPodsOn("fluent-bit", "n1"),
	}
	rep := Analyze(in, Options{}).DaemonSets[0]
	f := findingFor(t, rep, "gpu-1")
	if f.Reason != "untolerated-taint" {
		t.Fatalf("reason = %q, want untolerated-taint", f.Reason)
	}
	if !contains(f.Detail, "nvidia.com/gpu") {
		t.Fatalf("detail %q should name the taint key", f.Detail)
	}
	if rep.CoveragePct != 50 {
		t.Fatalf("coveragePct = %v, want 50", rep.CoveragePct)
	}
}

func TestNodeSelectorMatchingThreeOfFive(t *testing.T) {
	in := Input{
		Nodes: []corev1.Node{
			node("n1", map[string]string{"disk": "ssd"}),
			node("n2", map[string]string{"disk": "ssd"}),
			node("n3", map[string]string{"disk": "ssd"}),
			node("n4", map[string]string{"disk": "hdd"}),
			node("n5", nil),
		},
		DaemonSets: []appsv1.DaemonSet{ds("fast-agent", func(d *appsv1.DaemonSet) {
			d.Spec.Template.Spec.NodeSelector = map[string]string{"disk": "ssd"}
		})},
		Pods: readyPodsOn("fast-agent", "n1", "n2", "n3"),
	}
	rep := Analyze(in, Options{}).DaemonSets[0]
	if rep.CoveragePct != 60 {
		t.Fatalf("coveragePct = %v, want 60", rep.CoveragePct)
	}
	if len(rep.Uncovered) != 2 {
		t.Fatalf("want 2 uncovered nodes, got %+v", rep.Uncovered)
	}
	for _, name := range []string{"n4", "n5"} {
		if f := findingFor(t, rep, name); f.Reason != "nodeselector-mismatch" {
			t.Fatalf("%s reason = %q, want nodeselector-mismatch", name, f.Reason)
		}
	}
}

func TestCordonedNodeIsItsOwnReason(t *testing.T) {
	in := Input{
		Nodes:      []corev1.Node{node("n1", nil), cordoned("n2")},
		DaemonSets: []appsv1.DaemonSet{ds("node-exporter")},
		Pods:       readyPodsOn("node-exporter", "n1"),
	}
	rep := Analyze(in, Options{}).DaemonSets[0]
	if f := findingFor(t, rep, "n2"); f.Reason != "node-unschedulable" {
		t.Fatalf("reason = %q, want node-unschedulable", f.Reason)
	}
}

func TestPodNotReadyIsNotAschedulingFinding(t *testing.T) {
	pods := append(readyPodsOn("node-exporter", "n1"),
		pod("node-exporter-n2", "n2", "node-exporter", corev1.PodRunning, false))
	in := Input{
		Nodes:      []corev1.Node{node("n1", nil), node("n2", nil)},
		DaemonSets: []appsv1.DaemonSet{ds("node-exporter")},
		Pods:       pods,
	}
	rep := Analyze(in, Options{}).DaemonSets[0]
	f := findingFor(t, rep, "n2")
	if f.Reason != ReasonPodNotReady {
		t.Fatalf("reason = %q, want %q", f.Reason, ReasonPodNotReady)
	}
	if f.Pod != "node-exporter-n2" {
		t.Fatalf("pod = %q, want node-exporter-n2", f.Pod)
	}
	if rep.PendingPods != 0 {
		t.Fatalf("a not-ready Running pod is not Pending, got PendingPods=%d", rep.PendingPods)
	}
}

func TestPendingPodIsCounted(t *testing.T) {
	in := Input{
		Nodes:      []corev1.Node{node("n1", nil)},
		DaemonSets: []appsv1.DaemonSet{ds("node-exporter")},
		Pods:       []corev1.Pod{pod("node-exporter-n1", "n1", "node-exporter", corev1.PodPending, false)},
	}
	rep := Analyze(in, Options{})
	if rep.PendingPods() != 1 {
		t.Fatalf("PendingPods = %d, want 1", rep.PendingPods())
	}
	if f := findingFor(t, rep.DaemonSets[0], "n1"); f.Reason != ReasonPodPending {
		t.Fatalf("reason = %q, want %q", f.Reason, ReasonPodPending)
	}
}

func TestMissingPodOnEligibleNode(t *testing.T) {
	in := Input{
		Nodes:      []corev1.Node{node("n1", nil), node("n2", nil)},
		DaemonSets: []appsv1.DaemonSet{ds("node-exporter")},
		Pods:       readyPodsOn("node-exporter", "n1"),
	}
	rep := Analyze(in, Options{}).DaemonSets[0]
	if f := findingFor(t, rep, "n2"); f.Reason != ReasonPodMissing {
		t.Fatalf("reason = %q, want %q", f.Reason, ReasonPodMissing)
	}
}

func TestExpectLabelsNarrowsScope(t *testing.T) {
	in := Input{
		Nodes: []corev1.Node{
			node("w1", map[string]string{"node-role.kubernetes.io/worker": ""}),
			node("w2", map[string]string{"node-role.kubernetes.io/worker": ""}),
			node("cp1", map[string]string{"node-role.kubernetes.io/control-plane": ""}),
		},
		DaemonSets: []appsv1.DaemonSet{ds("worker-agent")},
		Pods:       readyPodsOn("worker-agent", "w1", "w2"),
	}
	expect, err := ParseExpectLabels("node-role.kubernetes.io/worker=")
	if err != nil {
		t.Fatal(err)
	}
	rep := Analyze(in, Options{ExpectLabels: expect})
	if rep.ScopeNodes != 2 || rep.ClusterNodes != 3 {
		t.Fatalf("scope = %d of %d, want 2 of 3", rep.ScopeNodes, rep.ClusterNodes)
	}
	one := rep.DaemonSets[0]
	if one.CoveragePct != 100 {
		t.Fatalf("coveragePct = %v, want 100 (control-plane node is out of scope)", one.CoveragePct)
	}
	if one.OutOfScope != 1 {
		t.Fatalf("OutOfScope = %d, want 1", one.OutOfScope)
	}
}

func TestParseExpectLabels(t *testing.T) {
	got, err := ParseExpectLabels(" pool=batch , gpu ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 matches, got %+v", got)
	}
	if got[0] != (LabelMatch{Key: "pool", Value: "batch", ValueOnly: true}) {
		t.Fatalf("pool=batch parsed as %+v", got[0])
	}
	if got[1] != (LabelMatch{Key: "gpu", ValueOnly: false}) {
		t.Fatalf("bare gpu parsed as %+v", got[1])
	}
	if _, err := ParseExpectLabels("=oops"); err == nil {
		t.Fatal("an empty key must be an error")
	}
	if got, err := ParseExpectLabels(""); err != nil || got != nil {
		t.Fatalf("empty input should yield no matches: %+v %v", got, err)
	}
}

func TestBareKeyExpectLabelMatchesAnyValue(t *testing.T) {
	expect, err := ParseExpectLabels("pool")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{
		Nodes: []corev1.Node{
			node("a", map[string]string{"pool": "batch"}),
			node("b", map[string]string{"pool": "web"}),
			node("c", nil),
		},
		DaemonSets: []appsv1.DaemonSet{ds("agent")},
		Pods:       readyPodsOn("agent", "a", "b"),
	}
	rep := Analyze(in, Options{ExpectLabels: expect})
	if rep.ScopeNodes != 2 {
		t.Fatalf("scopeNodes = %d, want 2", rep.ScopeNodes)
	}
}

func TestGapsAndThresholds(t *testing.T) {
	in := Input{
		Nodes: []corev1.Node{node("n1", nil), node("n2", nil)},
		DaemonSets: []appsv1.DaemonSet{
			ds("full"),
			ds("half"),
		},
		Pods: append(readyPodsOn("full", "n1", "n2"), readyPodsOn("half", "n1")...),
	}
	rep := Analyze(in, Options{})
	if gaps := rep.Gaps(100); len(gaps) != 1 || gaps[0].Name != "half" {
		t.Fatalf("Gaps(100) = %+v, want only half", gaps)
	}
	if gaps := rep.Gaps(50); len(gaps) != 0 {
		t.Fatalf("Gaps(50) = %+v, want none", gaps)
	}
}

func TestPodsAreMatchedByOwnerAndSelectorAcrossNamespaces(t *testing.T) {
	foreign := pod("node-exporter-n1", "n1", "node-exporter", corev1.PodRunning, true)
	foreign.Namespace = "other"
	in := Input{
		Nodes:      []corev1.Node{node("n1", nil)},
		DaemonSets: []appsv1.DaemonSet{ds("node-exporter")},
		Pods:       []corev1.Pod{foreign},
	}
	rep := Analyze(in, Options{}).DaemonSets[0]
	if f := findingFor(t, rep, "n1"); f.Reason != ReasonPodMissing {
		t.Fatalf("a pod in another namespace must not count as coverage, got %q", f.Reason)
	}
}

func TestReadyPodWinsOverTerminatingReplicaOnTheSameNode(t *testing.T) {
	stale := pod("node-exporter-old", "n1", "node-exporter", corev1.PodFailed, false)
	fresh := pod("node-exporter-new", "n1", "node-exporter", corev1.PodRunning, true)
	in := Input{
		Nodes:      []corev1.Node{node("n1", nil)},
		DaemonSets: []appsv1.DaemonSet{ds("node-exporter")},
		Pods:       []corev1.Pod{stale, fresh},
	}
	rep := Analyze(in, Options{}).DaemonSets[0]
	if rep.CoveragePct != 100 {
		t.Fatalf("coveragePct = %v, want 100 (%+v)", rep.CoveragePct, rep.Uncovered)
	}
}

func TestNoNodesInScopeReportsHundredNotNaN(t *testing.T) {
	in := Input{DaemonSets: []appsv1.DaemonSet{ds("agent")}}
	rep := Analyze(in, Options{})
	if rep.DaemonSets[0].CoveragePct != 100 {
		t.Fatalf("coveragePct = %v, want 100", rep.DaemonSets[0].CoveragePct)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
