// Package predicate re-implements the subset of the kube-scheduler node
// predicates that decides whether a DaemonSet pod is allowed to land on a
// node: taints/tolerations, nodeSelector and required nodeAffinity.
//
// It is deliberately free of any Kubernetes client code. Everything it needs
// arrives as plain API objects, which is what makes the whole engine testable
// without a cluster.
package predicate

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Reason is a stable, machine-readable explanation for a scheduling verdict.
type Reason string

const (
	// ReasonEligible means the scheduler would accept this node.
	ReasonEligible Reason = "eligible"
	// ReasonNodeUnschedulable means the node is cordoned.
	ReasonNodeUnschedulable Reason = "node-unschedulable"
	// ReasonUntoleratedTaint means a NoSchedule/NoExecute taint is not tolerated.
	ReasonUntoleratedTaint Reason = "untolerated-taint"
	// ReasonNodeSelectorMismatch means spec.nodeSelector does not match the node labels.
	ReasonNodeSelectorMismatch Reason = "nodeselector-mismatch"
	// ReasonNodeAffinityMismatch means required nodeAffinity does not match the node.
	ReasonNodeAffinityMismatch Reason = "nodeaffinity-mismatch"
)

// Result is the verdict for one (node, podSpec) pair.
type Result struct {
	Eligible bool   `json:"eligible"`
	Reason   Reason `json:"reason"`
	Detail   string `json:"detail,omitempty"`
}

// Evaluate reports whether spec could be scheduled onto node.
//
// The checks run cordon first, then label selectors, then taints. Cordon comes
// first on purpose: a cordoned node also carries the
// node.kubernetes.io/unschedulable:NoSchedule taint, which DaemonSets tolerate
// by default, so reporting it as a taint finding would be both noisy and
// misleading. "You cordoned this node" is the actionable answer.
func Evaluate(node *corev1.Node, spec *corev1.PodSpec) Result {
	if node == nil || spec == nil {
		return Result{Reason: ReasonNodeSelectorMismatch, Detail: "missing node or pod spec"}
	}
	if node.Spec.Unschedulable {
		return Result{Reason: ReasonNodeUnschedulable, Detail: "spec.unschedulable=true (cordoned)"}
	}
	if detail, ok := matchNodeSelector(node.Labels, spec.NodeSelector); !ok {
		return Result{Reason: ReasonNodeSelectorMismatch, Detail: detail}
	}
	if detail, ok := matchNodeAffinity(node, spec); !ok {
		return Result{Reason: ReasonNodeAffinityMismatch, Detail: detail}
	}
	if detail, ok := toleratesTaints(node.Spec.Taints, spec.Tolerations); !ok {
		return Result{Reason: ReasonUntoleratedTaint, Detail: detail}
	}
	return Result{Eligible: true, Reason: ReasonEligible}
}

func matchNodeSelector(labels, selector map[string]string) (string, bool) {
	keys := make([]string, 0, len(selector))
	for k := range selector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		want := selector[k]
		got, present := labels[k]
		if !present {
			return fmt.Sprintf("nodeSelector %s=%s: node has no label %s", k, want, k), false
		}
		if got != want {
			return fmt.Sprintf("nodeSelector %s=%s: node has %s=%s", k, want, k, got), false
		}
	}
	return "", true
}

// matchNodeAffinity evaluates requiredDuringSchedulingIgnoredDuringExecution.
// Terms are OR-ed, expressions inside one term are AND-ed, which is the API's
// own semantics. Preferred affinity is ignored: it never blocks scheduling.
func matchNodeAffinity(node *corev1.Node, spec *corev1.PodSpec) (string, bool) {
	if spec.Affinity == nil || spec.Affinity.NodeAffinity == nil {
		return "", true
	}
	required := spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil || len(required.NodeSelectorTerms) == 0 {
		return "", true
	}
	var misses []string
	for _, term := range required.NodeSelectorTerms {
		if detail, ok := matchTerm(node, term); ok {
			return "", true
		} else if detail != "" {
			misses = append(misses, detail)
		}
	}
	return "no nodeSelectorTerm matched: " + strings.Join(misses, "; "), false
}

func matchTerm(node *corev1.Node, term corev1.NodeSelectorTerm) (string, bool) {
	for _, expr := range term.MatchExpressions {
		if detail, ok := matchExpression(node.Labels, expr); !ok {
			return detail, false
		}
	}
	for _, expr := range term.MatchFields {
		fields := map[string]string{"metadata.name": node.Name}
		if detail, ok := matchExpression(fields, expr); !ok {
			return detail, false
		}
	}
	return "", true
}

func matchExpression(values map[string]string, expr corev1.NodeSelectorRequirement) (string, bool) {
	got, present := values[expr.Key]
	fail := func(why string) (string, bool) {
		return fmt.Sprintf("%s %s %v: %s", expr.Key, strings.ToLower(string(expr.Operator)), expr.Values, why), false
	}
	switch expr.Operator {
	case corev1.NodeSelectorOpIn:
		if !present {
			return fail("label absent")
		}
		if !contains(expr.Values, got) {
			return fail("node has " + expr.Key + "=" + got)
		}
	case corev1.NodeSelectorOpNotIn:
		if present && contains(expr.Values, got) {
			return fail("node has " + expr.Key + "=" + got)
		}
	case corev1.NodeSelectorOpExists:
		if !present {
			return fail("label absent")
		}
	case corev1.NodeSelectorOpDoesNotExist:
		if present {
			return fail("label present as " + expr.Key + "=" + got)
		}
	case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
		if !present {
			return fail("label absent")
		}
		if len(expr.Values) != 1 {
			return fail("operator needs exactly one value")
		}
		nodeNum, err := strconv.ParseInt(got, 10, 64)
		if err != nil {
			return fail("node value " + got + " is not an integer")
		}
		want, err := strconv.ParseInt(expr.Values[0], 10, 64)
		if err != nil {
			return fail("selector value " + expr.Values[0] + " is not an integer")
		}
		if expr.Operator == corev1.NodeSelectorOpGt && nodeNum <= want {
			return fail("node has " + expr.Key + "=" + got)
		}
		if expr.Operator == corev1.NodeSelectorOpLt && nodeNum >= want {
			return fail("node has " + expr.Key + "=" + got)
		}
	default:
		return fail("unsupported operator")
	}
	return "", true
}

// toleratesTaints checks every NoSchedule/NoExecute taint against the pod's
// tolerations. PreferNoSchedule is skipped: it cannot keep a pod off a node.
func toleratesTaints(taints []corev1.Taint, tolerations []corev1.Toleration) (string, bool) {
	for i := range taints {
		taint := taints[i]
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		if !taintTolerated(taint, tolerations) {
			detail := fmt.Sprintf("taint %s", taint.Key)
			if taint.Value != "" {
				detail += "=" + taint.Value
			}
			return detail + ":" + string(taint.Effect) + " is not tolerated", false
		}
	}
	return "", true
}

func taintTolerated(taint corev1.Taint, tolerations []corev1.Toleration) bool {
	for _, tol := range tolerations {
		if tol.Effect != "" && tol.Effect != taint.Effect {
			continue
		}
		op := tol.Operator
		if op == "" {
			op = corev1.TolerationOpEqual
		}
		if tol.Key == "" {
			// An empty key with Exists is the wildcard toleration.
			if op == corev1.TolerationOpExists {
				return true
			}
			continue
		}
		if tol.Key != taint.Key {
			continue
		}
		switch op {
		case corev1.TolerationOpExists:
			return true
		case corev1.TolerationOpEqual:
			if tol.Value == taint.Value {
				return true
			}
		}
	}
	return false
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
