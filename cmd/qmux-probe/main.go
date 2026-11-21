package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

// run runs the probes with the flags in args[1:], or the subcommand in args[1], and
// returns the process exit code. A first argument that is not a flag names a
// subcommand.
func run(args []string, stdout, stderr io.Writer) int {
	name := filepath.Base(args[0])
	if len(args) < 2 || strings.HasPrefix(args[1], "-") {
		return runProbe(context.Background(), name, args[1:], stdout, stderr)
	}
	var err error
	switch args[1] {
	case "version":
		_, err = fmt.Fprintf(stdout, "%s %s\n", name, versionOf(debug.ReadBuildInfo()))
	case "help":
		return runProbe(context.Background(), name, []string{"--help"}, stdout, stderr)
	default:
		// The exit code is 64 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "Unknown command: %s\nUsage: %s --trust-anchors <file> [flags] | <version|help>\n", args[1], name)
		return exitUsage
	}
	if err != nil {
		// A caller that reads stdout must not see exit 0 with missing output.
		return 1
	}
	return 0
}

// versionOf returns the main module version from the build info.
// It returns v0.0.0-dev when the build has no version.
// A go test binary and a build outside a VCS checkout report "(devel)" or an empty version.
func versionOf(info *debug.BuildInfo, ok bool) string {
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "v0.0.0-dev"
	}
	return info.Main.Version
}
