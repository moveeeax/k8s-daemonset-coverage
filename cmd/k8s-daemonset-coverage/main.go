// k8s-daemonset-coverage reports the nodes a DaemonSet is not running on, and
// the exact reason for each one.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/moveeeax/k8s-daemonset-coverage/internal/coverage"
	"github.com/moveeeax/k8s-daemonset-coverage/internal/kube"
	"github.com/moveeeax/k8s-daemonset-coverage/internal/render"
)

// version is overridden at release time with -ldflags "-X main.version=...".
var version = "0.1.0"

const usage = `k8s-daemonset-coverage — which nodes is this DaemonSet silently not on?

Usage:
  k8s-daemonset-coverage [flags]

Flags:
  -n, --namespace string      namespace to inspect (default: all namespaces)
      --kubeconfig string     explicit kubeconfig path (default: standard loading rules)
      --context string        kubeconfig context to use
      --expect-labels string  comma-separated node labels the DaemonSet is meant to cover
                              ("key=value" requires that value, bare "key" only requires the label)
      --format string         text|json (default "text")
      --fail-under float      exit 1 if any DaemonSet is below this coverage percentage
      --fail-on-pending       exit 1 if any DaemonSet pod is Pending
      --snapshot string       read a JSON cluster snapshot instead of talking to a cluster
      --timeout duration      API timeout (default 30s)
      --version               print version and exit

Exit codes: 0 clean, 1 a threshold was breached, 2 an error.
`

func main() {
	if code := run(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("k8s-daemonset-coverage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }

	var (
		namespace     string
		kubeconfig    = fs.String("kubeconfig", "", "explicit kubeconfig path")
		kubeContext   = fs.String("context", "", "kubeconfig context")
		expectLabels  = fs.String("expect-labels", "", "node labels the DaemonSet is meant to cover")
		format        = fs.String("format", "text", "output format: text|json")
		failUnder     = fs.Float64("fail-under", -1, "exit 1 below this coverage percentage")
		failOnPending = fs.Bool("fail-on-pending", false, "exit 1 if any DaemonSet pod is Pending")
		snapshot      = fs.String("snapshot", "", "read a JSON cluster snapshot instead of a cluster")
		timeout       = fs.Duration("timeout", 30*time.Second, "API timeout")
		showVersion   = fs.Bool("version", false, "print version")
	)
	fs.StringVar(&namespace, "namespace", "", "namespace to inspect")
	fs.StringVar(&namespace, "n", "", "namespace to inspect (shorthand)")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintf(stderr, "error: --format must be text or json, got %q\n", *format)
		return 2
	}

	expect, err := coverage.ParseExpectLabels(*expectLabels)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	in, err := load(*snapshot, *kubeconfig, *kubeContext, namespace, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	rep := coverage.Analyze(in, coverage.Options{ExpectLabels: expect})

	if *format == "json" {
		err = render.JSON(stdout, rep)
	} else {
		err = render.Text(stdout, rep)
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: write report: %v\n", err)
		return 2
	}

	return exitCode(rep, *failUnder, *failOnPending, stderr)
}

func exitCode(rep coverage.Report, failUnder float64, failOnPending bool, stderr io.Writer) int {
	code := 0
	if failUnder >= 0 {
		if gaps := rep.Gaps(failUnder); len(gaps) > 0 {
			for _, ds := range gaps {
				fmt.Fprintf(stderr, "fail-under: %s/%s at %.1f%% (threshold %.1f%%)\n",
					ds.Namespace, ds.Name, ds.CoveragePct, failUnder)
			}
			code = 1
		}
	}
	if failOnPending {
		if pending := rep.PendingPods(); pending > 0 {
			fmt.Fprintf(stderr, "fail-on-pending: %d DaemonSet pod(s) Pending\n", pending)
			code = 1
		}
	}
	return code
}

func load(snapshot, kubeconfig, kubeContext, namespace string, timeout time.Duration) (coverage.Input, error) {
	if snapshot != "" {
		return loadSnapshot(snapshot)
	}
	client, err := kube.NewClient(kubeconfig, kubeContext)
	if err != nil {
		return coverage.Input{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return kube.Snapshot(ctx, client, namespace)
}

func loadSnapshot(path string) (coverage.Input, error) {
	var in coverage.Input
	raw, err := os.ReadFile(path)
	if err != nil {
		return in, fmt.Errorf("read snapshot: %w", err)
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("parse snapshot %s: %w", path, err)
	}
	return in, nil
}
