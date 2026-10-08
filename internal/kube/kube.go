// Package kube is the thin adapter between a Kubernetes client and the pure
// coverage engine. It only reads: nodes, daemonsets, pods.
package kube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/moveeeax/k8s-daemonset-coverage/internal/coverage"
)

// Client is the subset of kubernetes.Interface this tool needs, so tests can
// hand in a fake clientset.
type Client = kubernetes.Interface

// NewClient builds a client from an explicit kubeconfig path (empty = default
// loading rules, including in-cluster service account) and an optional context.
func NewClient(kubeconfig, kubeContext string) (Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules,
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build client: %w", err)
	}
	return cs, nil
}

// Snapshot lists everything the analysis needs. An empty namespace means all
// namespaces.
func Snapshot(ctx context.Context, c Client, namespace string) (coverage.Input, error) {
	var in coverage.Input

	nodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return in, fmt.Errorf("list nodes: %w", err)
	}
	in.Nodes = nodes.Items

	daemonSets, err := c.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return in, fmt.Errorf("list daemonsets: %w", err)
	}
	in.DaemonSets = daemonSets.Items

	pods, err := c.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return in, fmt.Errorf("list pods: %w", err)
	}
	in.Pods = pods.Items

	return in, nil
}
