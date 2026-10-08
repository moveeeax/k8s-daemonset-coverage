// Package coverage turns a snapshot of nodes, DaemonSets and pods into a
// per-DaemonSet coverage report: which nodes are actually served, which are
// not, and why not.
package coverage

import (
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/moveeeax/k8s-daemonset-coverage/internal/predicate"
)

// Extra reasons that are not scheduling verdicts but still mean "this node is
// not served by the DaemonSet right now".
const (
	ReasonPodMissing  = "pod-missing"
	ReasonPodPending  = "pod-pending"
	ReasonPodNotReady = "pod-not-ready"
	ReasonOutOfScope  = "out-of-scope"
)

// Input is the cluster snapshot the analysis runs on.
type Input struct {
	Nodes      []corev1.Node      `json:"nodes"`
	DaemonSets []appsv1.DaemonSet `json:"daemonsets"`
	Pods       []corev1.Pod       `json:"pods"`
}

// Options tunes scope and failure thresholds.
type Options struct {
	// ExpectLabels restricts the node set a DaemonSet is supposed to cover.
	// Nodes outside it are dropped from both numerator and denominator, which
	// is how an intentionally GPU-only agent stops being noise.
	ExpectLabels []LabelMatch
}

// LabelMatch is one --expect-labels entry: "key=value" requires that exact
// value (including the empty value of node-role labels), bare "key" only
// requires the label to be present.
type LabelMatch struct {
	Key       string
	Value     string
	ValueOnly bool
}

// ParseExpectLabels parses a comma-separated --expect-labels value.
func ParseExpectLabels(raw string) ([]LabelMatch, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []LabelMatch
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, hasEq := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("expect-labels entry %q has an empty key", part)
		}
		out = append(out, LabelMatch{Key: key, Value: strings.TrimSpace(value), ValueOnly: hasEq})
	}
	return out, nil
}

func (m LabelMatch) matches(labels map[string]string) bool {
	got, present := labels[m.Key]
	if !present {
		return false
	}
	if !m.ValueOnly {
		return true
	}
	return got == m.Value
}

// NodeFinding explains one node that the DaemonSet does not serve.
type NodeFinding struct {
	Node   string `json:"node"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
	Pod    string `json:"pod,omitempty"`
}

// DaemonSetReport is the coverage verdict for a single DaemonSet.
type DaemonSetReport struct {
	Namespace    string        `json:"namespace"`
	Name         string        `json:"name"`
	ScopeNodes   int           `json:"scopeNodes"`
	CoveredNodes int           `json:"coveredNodes"`
	CoveragePct  float64       `json:"coveragePct"`
	PendingPods  int           `json:"pendingPods"`
	OutOfScope   int           `json:"outOfScopeNodes"`
	Uncovered    []NodeFinding `json:"uncovered"`
}

// Report is the whole run.
type Report struct {
	ClusterNodes int               `json:"clusterNodes"`
	ScopeNodes   int               `json:"scopeNodes"`
	DaemonSets   []DaemonSetReport `json:"daemonsets"`
}

// Analyze computes coverage for every DaemonSet in the snapshot.
func Analyze(in Input, opts Options) Report {
	scope := make([]*corev1.Node, 0, len(in.Nodes))
	outOfScope := 0
	for i := range in.Nodes {
		node := &in.Nodes[i]
		if inScope(node, opts.ExpectLabels) {
			scope = append(scope, node)
			continue
		}
		outOfScope++
	}
	sort.Slice(scope, func(i, j int) bool { return scope[i].Name < scope[j].Name })

	rep := Report{ClusterNodes: len(in.Nodes), ScopeNodes: len(scope)}
	for i := range in.DaemonSets {
		rep.DaemonSets = append(rep.DaemonSets, analyzeOne(&in.DaemonSets[i], scope, in.Pods, outOfScope))
	}
	sort.Slice(rep.DaemonSets, func(i, j int) bool {
		a, b := rep.DaemonSets[i], rep.DaemonSets[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return rep
}

func inScope(node *corev1.Node, expect []LabelMatch) bool {
	for _, m := range expect {
		if !m.matches(node.Labels) {
			return false
		}
	}
	return true
}

func analyzeOne(ds *appsv1.DaemonSet, scope []*corev1.Node, pods []corev1.Pod, outOfScope int) DaemonSetReport {
	rep := DaemonSetReport{
		Namespace:  ds.Namespace,
		Name:       ds.Name,
		ScopeNodes: len(scope),
		OutOfScope: outOfScope,
		Uncovered:  []NodeFinding{},
	}
	byNode := podsByNode(ds, pods)
	spec := ds.Spec.Template.Spec

	for _, node := range scope {
		verdict := predicate.Evaluate(node, &spec)
		if !verdict.Eligible {
			rep.Uncovered = append(rep.Uncovered, NodeFinding{
				Node:   node.Name,
				Reason: string(verdict.Reason),
				Detail: verdict.Detail,
			})
			continue
		}
		pod := byNode[node.Name]
		if pod == nil {
			rep.Uncovered = append(rep.Uncovered, NodeFinding{
				Node:   node.Name,
				Reason: ReasonPodMissing,
				Detail: "node is schedulable for this DaemonSet but has no pod",
			})
			continue
		}
		switch {
		case pod.Status.Phase == corev1.PodPending:
			rep.PendingPods++
			rep.Uncovered = append(rep.Uncovered, NodeFinding{
				Node:   node.Name,
				Reason: ReasonPodPending,
				Detail: "pod is Pending",
				Pod:    pod.Name,
			})
		case !podReady(pod):
			rep.Uncovered = append(rep.Uncovered, NodeFinding{
				Node:   node.Name,
				Reason: ReasonPodNotReady,
				Detail: fmt.Sprintf("pod phase=%s, Ready condition is not True", pod.Status.Phase),
				Pod:    pod.Name,
			})
		default:
			rep.CoveredNodes++
		}
	}
	if rep.ScopeNodes > 0 {
		rep.CoveragePct = round1(float64(rep.CoveredNodes) * 100 / float64(rep.ScopeNodes))
	} else {
		rep.CoveragePct = 100
	}
	return rep
}

// podsByNode selects the pods belonging to ds, preferring the ownerReference
// (authoritative) and falling back to the template's label selector for pods
// that an adopting controller has not stamped yet.
func podsByNode(ds *appsv1.DaemonSet, pods []corev1.Pod) map[string]*corev1.Pod {
	out := map[string]*corev1.Pod{}
	for i := range pods {
		pod := &pods[i]
		if pod.Namespace != ds.Namespace || pod.Spec.NodeName == "" {
			continue
		}
		if !ownedBy(pod, ds) && !selectorMatches(ds, pod) {
			continue
		}
		existing := out[pod.Spec.NodeName]
		if existing == nil || preferPod(pod, existing) {
			out[pod.Spec.NodeName] = pod
		}
	}
	return out
}

func ownedBy(pod *corev1.Pod, ds *appsv1.DaemonSet) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" && ref.Name == ds.Name {
			return true
		}
	}
	return false
}

func selectorMatches(ds *appsv1.DaemonSet, pod *corev1.Pod) bool {
	if ds.Spec.Selector == nil || len(ds.Spec.Selector.MatchLabels) == 0 {
		return false
	}
	for k, v := range ds.Spec.Selector.MatchLabels {
		if pod.Labels[k] != v {
			return false
		}
	}
	return true
}

// preferPod keeps the healthiest pod on a node, so a leftover terminating
// replica cannot mask a running one.
func preferPod(candidate, current *corev1.Pod) bool {
	return rank(candidate) > rank(current)
}

func rank(pod *corev1.Pod) int {
	switch {
	case podReady(pod):
		return 3
	case pod.Status.Phase == corev1.PodRunning:
		return 2
	case pod.Status.Phase == corev1.PodPending:
		return 1
	default:
		return 0
	}
}

func podReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

// Gaps reports the DaemonSets below pct coverage.
func (r Report) Gaps(pct float64) []DaemonSetReport {
	var out []DaemonSetReport
	for _, ds := range r.DaemonSets {
		if ds.CoveragePct < pct {
			out = append(out, ds)
		}
	}
	return out
}

// PendingPods totals Pending DaemonSet pods across the report.
func (r Report) PendingPods() int {
	total := 0
	for _, ds := range r.DaemonSets {
		total += ds.PendingPods
	}
	return total
}
