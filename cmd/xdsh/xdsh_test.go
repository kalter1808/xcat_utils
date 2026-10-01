package main

import (
	"os"
	"testing"
)

func TestIsTruthy(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", "yes", "YES", "y", "on", "ON"}
	for _, v := range truthy {
		if !isTruthy(v) {
			t.Errorf("isTruthy(%q) = false; want true", v)
		}
	}

	falsy := []string{"0", "false", "no", "off", "", "random"}
	for _, v := range falsy {
		if isTruthy(v) {
			t.Errorf("isTruthy(%q) = true; want false", v)
		}
	}
}

func TestParseXdshArgsIgnoreHostKey(t *testing.T) {
	flags := []string{"-k", "--ignore-host-key", "--ignore-hostkey", "--insecure", "--no-host-key-check"}
	for _, f := range flags {
		opts := &xdshOptions{}
		rest, err := parseXdshArgs([]string{f, "uptime"}, opts)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", f, err)
		}
		if !opts.ignoreHostKey {
			t.Errorf("expected ignoreHostKey=true for %s", f)
		}
		if len(rest) != 1 || rest[0] != "uptime" {
			t.Errorf("expected rest=[uptime], got %v", rest)
		}
	}

	// Test boolean values with --ignore-host-key=
	opts := &xdshOptions{}
	_, err := parseXdshArgs([]string{"--ignore-host-key=false", "uptime"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.ignoreHostKey {
		t.Errorf("expected ignoreHostKey=false for --ignore-host-key=false")
	}

	opts = &xdshOptions{}
	_, err = parseXdshArgs([]string{"--ignore-host-key=true", "uptime"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.ignoreHostKey {
		t.Errorf("expected ignoreHostKey=true for --ignore-host-key=true")
	}
}

func TestXdshMainOptionBeforeNoderange(t *testing.T) {
	// Test that options can be specified before noderange
	opts := &xdshOptions{}
	args := []string{"-k", "node[1-2]", "uptime"}
	var noderangeArg string
	if len(args) > 0 && !stringsHasPrefix(args[0], "-") {
		noderangeArg = args[0]
		args = args[1:]
	}
	rest, err := parseXdshArgs(args, opts)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if noderangeArg == "" && len(rest) > 0 {
		noderangeArg = rest[0]
		rest = rest[1:]
	}
	if noderangeArg != "node[1-2]" {
		t.Errorf("expected noderangeArg='node[1-2]', got %q", noderangeArg)
	}
	if !opts.ignoreHostKey {
		t.Errorf("expected ignoreHostKey=true")
	}
	if len(rest) != 1 || rest[0] != "uptime" {
		t.Errorf("expected cmdArgs=[uptime], got %v", rest)
	}
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestXdshEnvIgnoreHostKey(t *testing.T) {
	os.Setenv("XDSH_IGNORE_HOST_KEY", "1")
	defer os.Unsetenv("XDSH_IGNORE_HOST_KEY")

	opts := &xdshOptions{}
	ignoreHostKey := opts.ignoreHostKey
	if !ignoreHostKey {
		if v := os.Getenv("XDSH_IGNORE_HOST_KEY"); v != "" {
			ignoreHostKey = isTruthy(v)
		} else if v := os.Getenv("DSH_IGNORE_HOST_KEY"); v != "" {
			ignoreHostKey = isTruthy(v)
		}
	}
	if !ignoreHostKey {
		t.Errorf("expected ignoreHostKey=true from XDSH_IGNORE_HOST_KEY")
	}
}

func TestResolveTimeout(t *testing.T) {
	tests := []struct {
		cli  int
		env  string
		want int
	}{
		{-1, "", 5},
		{-1, "10", 10},
		{-1, "0", 0},
		{-1, "invalid", 5},
		{0, "", 0},
		{0, "10", 0},
		{3, "", 3},
		{3, "10", 3},
		{15, "0", 15},
	}
	for _, tc := range tests {
		got := resolveTimeout(tc.cli, tc.env)
		if got != tc.want {
			t.Errorf("resolveTimeout(%d, %q) = %d; want %d", tc.cli, tc.env, got, tc.want)
		}
	}
}

func TestParseXdshArgsTimeout(t *testing.T) {
	opts := &xdshOptions{timeout: -1}
	_, err := parseXdshArgs([]string{"-t", "0", "uptime"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.timeout != 0 {
		t.Errorf("expected opts.timeout=0, got %d", opts.timeout)
	}

	opts = &xdshOptions{timeout: -1}
	_, err = parseXdshArgs([]string{"--timeout", "10", "uptime"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.timeout != 10 {
		t.Errorf("expected opts.timeout=10, got %d", opts.timeout)
	}
}

func TestAddFailed(t *testing.T) {
	var failed []string
	addFailed(&failed, "node1")
	addFailed(&failed, "node2")
	addFailed(&failed, "node1")
	if len(failed) != 2 {
		t.Errorf("expected 2 unique failed nodes, got %d: %v", len(failed), failed)
	}
}

func TestParseXdshArgsNoSeparator(t *testing.T) {
	flags := []string{"--no-separator", "--no-sep", "--noseparator"}
	for _, f := range flags {
		opts := &xdshOptions{}
		rest, err := parseXdshArgs([]string{f, "uptime"}, opts)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", f, err)
		}
		if !opts.noSeparator {
			t.Errorf("expected noSeparator=true for %s", f)
		}
		if len(rest) != 1 || rest[0] != "uptime" {
			t.Errorf("expected rest=[uptime], got %v", rest)
		}
	}

	opts := &xdshOptions{}
	_, err := parseXdshArgs([]string{"--no-separator=false", "uptime"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.noSeparator {
		t.Errorf("expected noSeparator=false for --no-separator=false")
	}

	opts = &xdshOptions{}
	_, err = parseXdshArgs([]string{"--no-separator=1", "uptime"}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.noSeparator {
		t.Errorf("expected noSeparator=true for --no-separator=1")
	}
}

func TestXdshEnvNoSeparator(t *testing.T) {
	os.Setenv("XDSH_NO_SEPARATOR", "1")
	defer os.Unsetenv("XDSH_NO_SEPARATOR")

	opts := &xdshOptions{}
	noSeparator := opts.noSeparator
	if !noSeparator {
		if v := os.Getenv("XDSH_NO_SEPARATOR"); v != "" {
			noSeparator = isTruthy(v)
		} else if v := os.Getenv("DSH_NO_SEPARATOR"); v != "" {
			noSeparator = isTruthy(v)
		}
	}
	if !noSeparator {
		t.Errorf("expected noSeparator=true from XDSH_NO_SEPARATOR")
	}
}


