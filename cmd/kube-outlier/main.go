// Command kube-outlier is a read-only CLI that surfaces Kubernetes resources
// probably created by hand and never cleaned up. See docs/DESIGN.md for the
// full design; this binary only wires the cobra command tree together.
package main

import "os"

func main() {
	os.Exit(Execute())
}
