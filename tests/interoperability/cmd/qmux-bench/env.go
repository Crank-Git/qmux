package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// environment returns the environment block, without the date that bench sets. The
// CPU and the OS come from the host that runs Docker. The kernel and
// tcp_syn_retries come from the client container, because its kernel runs the TCP
// of the HTTPS baselines (FR-bench-20a).
func (h hostFlags) environment(ctx context.Context) (Environment, error) {
	e := Environment{Go: runtime.Version()}
	var err error
	if e.CPU, err = hostCPU(ctx); err != nil {
		return Environment{}, err
	}
	if e.OS, err = hostOS(ctx); err != nil {
		return Environment{}, err
	}
	steps := []struct {
		dst *string
		run func() ([]byte, error)
	}{
		{&e.Kernel, func() ([]byte, error) { return h.client(ctx, "uname", "-r") }},
		{&e.TCPSynRetries, func() ([]byte, error) { return h.client(ctx, "cat", "/proc/sys/net/ipv4/tcp_syn_retries") }},
		{&e.Docker, func() ([]byte, error) { return output(ctx, "", h.docker, "version", "--format", "{{.Server.Version}}") }},
		{&e.Commit, func() ([]byte, error) { return output(ctx, h.root, "git", "rev-parse", "HEAD") }},
	}
	for _, s := range steps {
		b, err := s.run()
		if err != nil {
			return Environment{}, fmt.Errorf("environment: %w", err)
		}
		*s.dst = strings.TrimSpace(string(b))
	}
	return e, nil
}

// output runs name with args in dir and returns its standard output.
func output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// hostCPU returns the CPU model of the host: machdep.cpu.brand_string on macOS, and
// the first "model name" of /proc/cpuinfo on Linux.
func hostCPU(ctx context.Context) (string, error) {
	if runtime.GOOS == "darwin" {
		b, err := output(ctx, "", "sysctl", "-n", "machdep.cpu.brand_string")
		return strings.TrimSpace(string(b)), err
	}
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "", fmt.Errorf("read /proc/cpuinfo: %w", err)
	}
	if v, ok := field(b, "model name", ':'); ok {
		return v, nil
	}
	// The cpuinfo of an arm64 Linux kernel has no model name.
	return runtime.GOARCH, nil
}

// hostOS returns the name and version of the host operating system.
func hostOS(ctx context.Context) (string, error) {
	if runtime.GOOS == "darwin" {
		b, err := output(ctx, "", "sw_vers", "-productVersion")
		return "macOS " + strings.TrimSpace(string(b)), err
	}
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", fmt.Errorf("read /etc/os-release: %w", err)
	}
	if v, ok := field(b, "PRETTY_NAME", '='); ok {
		return strings.Trim(v, `"`), nil
	}
	return runtime.GOOS, nil
}

// field returns the value of the first line in b whose key is key, with sep between
// the key and the value.
func field(b []byte, key string, sep byte) (string, bool) {
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		k, v, ok := strings.Cut(s.Text(), string(sep))
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}
