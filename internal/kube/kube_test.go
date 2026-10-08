package kube

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moveeeax/k8s-daemonset-coverage/internal/coverage"
)

func TestSnapshotReadsNodesDaemonSetsAndPods(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}
	daemon := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "node-exporter", Namespace: "monitoring"}}
	podIn := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "node-exporter-n1", Namespace: "monitoring"}}
	podOut := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"}}

	client := fake.NewSimpleClientset(node, daemon, podIn, podOut)

	all, err := Snapshot(context.Background(), client, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Nodes) != 1 || len(all.DaemonSets) != 1 || len(all.Pods) != 2 {
		t.Fatalf("all-namespace snapshot = %d nodes, %d ds, %d pods", len(all.Nodes), len(all.DaemonSets), len(all.Pods))
	}

	scoped, err := Snapshot(context.Background(), client, "monitoring")
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Pods) != 1 || scoped.Pods[0].Name != "node-exporter-n1" {
		t.Fatalf("namespace filter did not apply: %+v", scoped.Pods)
	}
	if len(scoped.Nodes) != 1 {
		t.Fatal("nodes are cluster-scoped and must always be listed in full")
	}
}

func TestSnapshotFeedsTheAnalysisEndToEnd(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu-1"}}
	node.Spec.Taints = []corev1.Taint{{Key: "nvidia.com/gpu", Value: "true", Effect: corev1.TaintEffectNoSchedule}}
	daemon := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "fluent-bit", Namespace: "logging"}}
	daemon.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "fluent-bit"}}

	in, err := Snapshot(context.Background(), fake.NewSimpleClientset(node, daemon), "")
	if err != nil {
		t.Fatal(err)
	}
	rep := coverage.Analyze(in, coverage.Options{})
	if len(rep.DaemonSets) != 1 {
		t.Fatalf("want one DaemonSet report, got %d", len(rep.DaemonSets))
	}
	one := rep.DaemonSets[0]
	if one.CoveragePct != 0 || len(one.Uncovered) != 1 {
		t.Fatalf("expected a single uncovered node at 0%%, got %+v", one)
	}
	if one.Uncovered[0].Reason != "untolerated-taint" {
		t.Fatalf("reason = %q", one.Uncovered[0].Reason)
	}
}

func TestNewClientRejectsAMissingKubeconfig(t *testing.T) {
	if _, err := NewClient("/nonexistent/kubeconfig.yaml", ""); err == nil {
		t.Fatal("expected an error for a missing kubeconfig path")
	}
}
