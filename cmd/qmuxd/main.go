// Package main implements the QMux DNS server daemon.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

var version = "v0.0.0-dev"

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if info.Main.Version == "" || info.Main.Version == "(devel)" {
		return
	}
	version = info.Main.Version
}

func main() {
	os.Exit(run(os.Args))
}

// run handles command dispatch and returns the exit code.
func run(args []string) int {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <version|help>\n", args[0])
		return 1
	}
	switch args[1] {
	case "version":
		fmt.Printf("%s %s\n", args[0], version)
		return 0
	case "help", "-h", "--help":
		fmt.Printf("Usage: %s <command>\n\nCommands:\n  version   Print version\n  help      Show this help\n", args[0])
		return 0
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", args[1])
		fmt.Fprintf(os.Stderr, "Usage: %s <version|help>\n", args[0])
		return 1
	}
}
