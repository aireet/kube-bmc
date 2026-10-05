// Command kubectl-bmc is a kubectl plugin for kube-bmc. Install it on the PATH and run
// `kubectl bmc`.
package main

import (
	"fmt"
	"os"

	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/aireet/kube-bmc/internal/kubectl"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cmd := kubectl.NewCommand(genericiooptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}, version, nil)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
