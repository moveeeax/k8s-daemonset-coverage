// Package render prints a coverage report as a human table or as JSON.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/moveeeax/k8s-daemonset-coverage/internal/coverage"
)

// JSON writes the report as indented JSON.
func JSON(w io.Writer, rep coverage.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// Text writes the report as a table plus one line per uncovered node.
func Text(w io.Writer, rep coverage.Report) error {
	if len(rep.DaemonSets) == 0 {
		_, err := fmt.Fprintln(w, "no DaemonSets found")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "NAMESPACE\tDAEMONSET\tSCOPE\tCOVERED\tCOVERAGE")
	for _, ds := range rep.DaemonSets {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%.1f%%\n", ds.Namespace, ds.Name, ds.ScopeNodes, ds.CoveredNodes, ds.CoveragePct)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, ds := range rep.DaemonSets {
		if len(ds.Uncovered) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s/%s — %d uncovered node(s):\n", ds.Namespace, ds.Name, len(ds.Uncovered))
		for _, f := range ds.Uncovered {
			line := fmt.Sprintf("  %-28s %s", f.Node, f.Reason)
			if f.Detail != "" {
				line += "  (" + f.Detail + ")"
			}
			fmt.Fprintln(w, line)
		}
	}
	if rep.ScopeNodes != rep.ClusterNodes {
		fmt.Fprintf(w, "\nscope: %d of %d cluster nodes (--expect-labels)\n", rep.ScopeNodes, rep.ClusterNodes)
	}
	return nil
}
