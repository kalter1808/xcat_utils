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
