package predicate

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func node(name string, labels map[string]string, taints ...corev1.Taint) *corev1.Node {
	n := &corev1.Node{}
	n.Name = name
	n.Labels = labels
	n.Spec.Taints = taints
	return n
}

func taint(key, value string, effect corev1.TaintEffect) corev1.Taint {
	return corev1.Taint{Key: key, Value: value, Effect: effect}
}

func TestEvaluate(t *testing.T) {
	affinity := func(exprs ...corev1.NodeSelectorRequirement) *corev1.Affinity {
		return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: exprs}},
			},
		}}
	}
	req := func(key string, op corev1.NodeSelectorOperator, values ...string) corev1.NodeSelectorRequirement {
		return corev1.NodeSelectorRequirement{Key: key, Operator: op, Values: values}
	}

	tests := []struct {
		name      string
		node      *corev1.Node
		spec      corev1.PodSpec
		eligible  bool
		reason    Reason
		detailHas string
	}{
		{
			name:     "plain node, plain pod",
			node:     node("n1", map[string]string{"kubernetes.io/os": "linux"}),
			eligible: true,
			reason:   ReasonEligible,
		},
		{
			name:      "untolerated NoSchedule taint names the key",
			node:      node("gpu-1", nil, taint("nvidia.com/gpu", "true", corev1.TaintEffectNoSchedule)),
			reason:    ReasonUntoleratedTaint,
			detailHas: "nvidia.com/gpu",
		},
		{
			name:      "untolerated NoExecute taint also blocks",
			node:      node("n1", nil, taint("node.kubernetes.io/not-ready", "", corev1.TaintEffectNoExecute)),
			reason:    ReasonUntoleratedTaint,
			detailHas: "node.kubernetes.io/not-ready",
		},
		{
			name:     "PreferNoSchedule never blocks",
			node:     node("n1", nil, taint("spot", "true", corev1.TaintEffectPreferNoSchedule)),
			eligible: true,
			reason:   ReasonEligible,
		},
		{
			name: "Equal toleration with matching value",
			node: node("gpu-1", nil, taint("nvidia.com/gpu", "true", corev1.TaintEffectNoSchedule)),
			spec: corev1.PodSpec{Tolerations: []corev1.Toleration{
				{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpEqual, Value: "true"},
			}},
			eligible: true,
			reason:   ReasonEligible,
		},
		{
			name: "Equal toleration with wrong value does not tolerate",
			node: node("gpu-1", nil, taint("nvidia.com/gpu", "true", corev1.TaintEffectNoSchedule)),
			spec: corev1.PodSpec{Tolerations: []corev1.Toleration{
				{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpEqual, Value: "false"},
			}},
			reason: ReasonUntoleratedTaint,
		},
		{
			name: "Exists toleration on the key",
			node: node("gpu-1", nil, taint("nvidia.com/gpu", "whatever", corev1.TaintEffectNoSchedule)),
			spec: corev1.PodSpec{Tolerations: []corev1.Toleration{
				{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists},
			}},
			eligible: true,
		},
		{
			name: "wildcard toleration tolerates everything",
			node: node("gpu-1", nil, taint("anything", "x", corev1.TaintEffectNoSchedule)),
			spec: corev1.PodSpec{Tolerations: []corev1.Toleration{
				{Operator: corev1.TolerationOpExists},
			}},
			eligible: true,
		},
		{
			name: "toleration scoped to the other effect does not help",
			node: node("n1", nil, taint("k", "v", corev1.TaintEffectNoSchedule)),
			spec: corev1.PodSpec{Tolerations: []corev1.Toleration{
				{Key: "k", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
			}},
			reason: ReasonUntoleratedTaint,
		},
		{
			name:      "cordoned node is reported as unschedulable, not as a taint",
			node:      cordon(node("n1", nil, taint("node.kubernetes.io/unschedulable", "", corev1.TaintEffectNoSchedule))),
			reason:    ReasonNodeUnschedulable,
			detailHas: "cordoned",
		},
		{
			name:      "nodeSelector missing label",
			node:      node("n1", map[string]string{"kubernetes.io/os": "linux"}),
			spec:      corev1.PodSpec{NodeSelector: map[string]string{"disk": "ssd"}},
			reason:    ReasonNodeSelectorMismatch,
			detailHas: "no label disk",
		},
		{
			name:      "nodeSelector wrong value prints both sides",
			node:      node("n1", map[string]string{"disk": "hdd"}),
			spec:      corev1.PodSpec{NodeSelector: map[string]string{"disk": "ssd"}},
			reason:    ReasonNodeSelectorMismatch,
			detailHas: "node has disk=hdd",
		},
		{
			name:     "nodeAffinity In matches",
			node:     node("n1", map[string]string{"topology.kubernetes.io/zone": "eu-central-1a"}),
			spec:     corev1.PodSpec{Affinity: affinity(req("topology.kubernetes.io/zone", corev1.NodeSelectorOpIn, "eu-central-1a", "eu-central-1b"))},
			eligible: true,
		},
		{
			name:      "nodeAffinity In misses",
			node:      node("n1", map[string]string{"topology.kubernetes.io/zone": "eu-central-1c"}),
			spec:      corev1.PodSpec{Affinity: affinity(req("topology.kubernetes.io/zone", corev1.NodeSelectorOpIn, "eu-central-1a"))},
			reason:    ReasonNodeAffinityMismatch,
			detailHas: "eu-central-1c",
		},
		{
			name:   "nodeAffinity NotIn excludes",
			node:   node("n1", map[string]string{"pool": "batch"}),
			spec:   corev1.PodSpec{Affinity: affinity(req("pool", corev1.NodeSelectorOpNotIn, "batch"))},
			reason: ReasonNodeAffinityMismatch,
		},
		{
			name:     "nodeAffinity NotIn passes when label absent",
			node:     node("n1", nil),
			spec:     corev1.PodSpec{Affinity: affinity(req("pool", corev1.NodeSelectorOpNotIn, "batch"))},
			eligible: true,
		},
		{
			name:     "nodeAffinity Exists",
			node:     node("n1", map[string]string{"gpu": ""}),
			spec:     corev1.PodSpec{Affinity: affinity(req("gpu", corev1.NodeSelectorOpExists))},
			eligible: true,
		},
		{
			name:   "nodeAffinity DoesNotExist fails when present",
			node:   node("n1", map[string]string{"gpu": ""}),
			spec:   corev1.PodSpec{Affinity: affinity(req("gpu", corev1.NodeSelectorOpDoesNotExist))},
			reason: ReasonNodeAffinityMismatch,
		},
		{
			name:     "nodeAffinity Gt",
			node:     node("n1", map[string]string{"cores": "16"}),
			spec:     corev1.PodSpec{Affinity: affinity(req("cores", corev1.NodeSelectorOpGt, "8"))},
			eligible: true,
		},
		{
			name:   "nodeAffinity Gt is strict",
			node:   node("n1", map[string]string{"cores": "8"}),
			spec:   corev1.PodSpec{Affinity: affinity(req("cores", corev1.NodeSelectorOpGt, "8"))},
			reason: ReasonNodeAffinityMismatch,
		},
		{
			name:     "nodeAffinity Lt",
			node:     node("n1", map[string]string{"cores": "4"}),
			spec:     corev1.PodSpec{Affinity: affinity(req("cores", corev1.NodeSelectorOpLt, "8"))},
			eligible: true,
		},
		{
			name:   "nodeAffinity Gt on a non-numeric label fails loudly",
			node:   node("n1", map[string]string{"cores": "many"}),
			spec:   corev1.PodSpec{Affinity: affinity(req("cores", corev1.NodeSelectorOpGt, "8"))},
			reason: ReasonNodeAffinityMismatch,
		},
		{
			name:     "nodeSelector is checked before taints",
			node:     node("n1", map[string]string{"disk": "hdd"}, taint("k", "v", corev1.TaintEffectNoSchedule)),
			spec:     corev1.PodSpec{NodeSelector: map[string]string{"disk": "ssd"}},
			reason:   ReasonNodeSelectorMismatch,
			eligible: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.node, &tc.spec)
			if got.Eligible != tc.eligible {
				t.Fatalf("eligible = %v, want %v (reason %q, detail %q)", got.Eligible, tc.eligible, got.Reason, got.Detail)
			}
			if tc.reason != "" && got.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.reason)
			}
			if tc.detailHas != "" && !strings.Contains(got.Detail, tc.detailHas) {
				t.Fatalf("detail %q does not contain %q", got.Detail, tc.detailHas)
			}
		})
	}
}

func TestEvaluateMultipleAffinityTermsAreOred(t *testing.T) {
	spec := corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "pool", Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}}},
			{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "pool", Operator: corev1.NodeSelectorOpIn, Values: []string{"b"}}}},
		}},
	}}}
	if got := Evaluate(node("n1", map[string]string{"pool": "b"}), &spec); !got.Eligible {
		t.Fatalf("second term should match: %+v", got)
	}
	if got := Evaluate(node("n1", map[string]string{"pool": "c"}), &spec); got.Eligible {
		t.Fatal("no term matches, node must be ineligible")
	}
}

func TestEvaluateMatchFieldsOnNodeName(t *testing.T) {
	spec := corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchFields: []corev1.NodeSelectorRequirement{{
				Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-a"},
			}}},
		}},
	}}}
	if got := Evaluate(node("node-a", nil), &spec); !got.Eligible {
		t.Fatalf("node-a should match: %+v", got)
	}
	if got := Evaluate(node("node-b", nil), &spec); got.Eligible {
		t.Fatal("node-b must not match")
	}
}

func cordon(n *corev1.Node) *corev1.Node {
	n.Spec.Unschedulable = true
	return n
}
