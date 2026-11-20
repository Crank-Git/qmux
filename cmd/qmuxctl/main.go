package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
)

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

// run dispatches the subcommand in args[1] and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	name := filepath.Base(args[0])
	if len(args) < 2 {
		// The exit code is 1 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "Usage: %s <query|discover|version|help>\n", name)
		return 1
	}
	var err error
	switch args[1] {
	case "query":
		return runQuery(context.Background(), name, args[2:], stdout, stderr)
	case "discover":
		return runDiscover(context.Background(), name, args[2:], stdout, stderr)
	case "version":
		_, err = fmt.Fprintf(stdout, "%s %s\n", name, versionOf(debug.ReadBuildInfo()))
	case "help", "-h", "--help":
		_, err = fmt.Fprintf(stdout, "Usage: %s <command>\n\nCommands:\n  query     Send one query and print the result\n  discover  Print the manifest of a server\n  version   Print version\n  help      Show this help\n", name)
	default:
		// The exit code is 1 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "Unknown command: %s\nUsage: %s <query|discover|version|help>\n", args[1], name)
		return 1
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
