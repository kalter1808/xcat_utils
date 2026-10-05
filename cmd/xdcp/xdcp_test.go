package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseXdcpArgsFlags(t *testing.T) {
	opts := &xdcpOptions{}
	rest, err := parseXdcpArgs([]string{
		"-f", "16",
		"-p",
		"-R",
		"-P",
		"-t", "10",
		"-o", "-v -a",
		"-k",
		"--sudo",
		"--nodestatus",
		"--no-separator",
		"node[1-2]",
		"/src/file",
		"/dst/dir",
	}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.fanout != 16 {
		t.Errorf("expected fanout=16, got %d", opts.fanout)
	}
	if !opts.preserve {
		t.Errorf("expected preserve=true")
	}
	if !opts.recursive {
		t.Errorf("expected recursive=true")
	}
	if !opts.pull {
		t.Errorf("expected pull=true")
	}
	if opts.timeout != 10 {
		t.Errorf("expected timeout=10, got %d", opts.timeout)
	}
	if opts.nodeOptions != "-v -a" {
		t.Errorf("expected nodeOptions='-v -a', got %q", opts.nodeOptions)
	}
	if !opts.ignoreHostKey {
		t.Errorf("expected ignoreHostKey=true")
	}
	if !opts.sudo {
		t.Errorf("expected sudo=true")
	}
	if !opts.nodestatus {
		t.Errorf("expected nodestatus=true")
	}
	if !opts.noSeparator {
		t.Errorf("expected noSeparator=true")
	}
	if len(rest) != 3 || rest[0] != "node[1-2]" || rest[1] != "/src/file" || rest[2] != "/dst/dir" {
		t.Errorf("unexpected rest: %v", rest)
	}
}

func TestParseSynclistFile(t *testing.T) {
	tmpDir := t.TempDir()
	f1 := filepath.Join(tmpDir, "f1.txt")
	f2 := filepath.Join(tmpDir, "f2.txt")
	_ = os.WriteFile(f1, stringData("data1"), 0o644)
	_ = os.WriteFile(f2, stringData("data2"), 0o644)

	synclistPath := filepath.Join(tmpDir, "synclist")
	content := "# Comment line\n\n" +
		f1 + " -> /etc/f1\n" +
		f1 + " " + f2 + " -> (node[1-2]) /etc/dir/\n" +
		filepath.Join(tmpDir, "*.txt") + " -> /tmp/dest/\n" +
		"EXECUTE:\n/tmp/some.post\n"
	if err := os.WriteFile(synclistPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write synclist: %v", err)
	}

	entries, err := parseSynclistFile(synclistPath)
	if err != nil {
		t.Fatalf("parseSynclistFile failed: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Entry 1
	if len(entries[0].sources) != 1 || entries[0].sources[0] != f1 {
		t.Errorf("entry 0 unexpected sources: %v", entries[0].sources)
	}
	if entries[0].target != "/etc/f1" {
		t.Errorf("entry 0 unexpected target: %s", entries[0].target)
	}
	if len(entries[0].nodes) != 0 {
		t.Errorf("entry 0 unexpected nodes: %v", entries[0].nodes)
	}

	// Entry 2 (with node filter)
	if len(entries[1].sources) != 2 {
		t.Errorf("entry 1 unexpected sources: %v", entries[1].sources)
	}
	if entries[1].target != "/etc/dir/" {
		t.Errorf("entry 1 unexpected target: %s", entries[1].target)
	}
	if len(entries[1].nodes) != 2 || entries[1].nodes[0] != "node1" || entries[1].nodes[1] != "node2" {
		t.Errorf("entry 1 unexpected nodes: %v", entries[1].nodes)
	}

	// Entry 3 (wildcard glob)
	if len(entries[2].sources) != 2 {
		t.Errorf("entry 2 wildcard glob should expand to 2 files, got %d: %v", len(entries[2].sources), entries[2].sources)
	}

	// Test filterSyncEntriesForNode
	n1Entries := filterSyncEntriesForNode(entries, "node1")
	if len(n1Entries) != 3 {
		t.Errorf("expected node1 to match 3 entries, got %d", len(n1Entries))
	}

	n3Entries := filterSyncEntriesForNode(entries, "node3")
	if len(n3Entries) != 2 {
		t.Errorf("expected node3 to match 2 entries, got %d", len(n3Entries))
	}
}

func stringData(s string) []byte {
	return []byte(s)
}

func TestCopyLocalToImage(t *testing.T) {
	srcDir := t.TempDir()
	imgDir := t.TempDir()

	file1 := filepath.Join(srcDir, "hosts")
	if err := os.WriteFile(file1, []byte("127.0.0.1 localhost"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	entries := []syncEntry{
		{
			sources: []string{file1},
			target:  "/etc/hosts",
		},
	}

	if err := copyLocalToImage(imgDir, entries, false); err != nil {
		t.Fatalf("copyLocalToImage failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(imgDir, "etc/hosts"))
	if err != nil {
		t.Fatalf("failed to read copied file: %v", err)
	}
	if string(got) != "127.0.0.1 localhost" {
		t.Errorf("unexpected content: %q", string(got))
	}
}

func TestParseXdcpArgsIgnoreHostKey(t *testing.T) {
	flags := []string{"-k", "--ignore-host-key", "--ignore-hostkey", "--insecure", "--no-host-key-check"}
	for _, f := range flags {
		opts := &xdcpOptions{}
		rest, err := parseXdcpArgs([]string{f, "node1", "/src", "/dst"}, opts)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", f, err)
		}
		if !opts.ignoreHostKey {
			t.Errorf("expected ignoreHostKey=true for %s", f)
		}
		if len(rest) != 3 {
			t.Errorf("expected rest len 3, got %v", rest)
		}
	}
}

func TestParseXdcpArgsNoSeparator(t *testing.T) {
	flags := []string{"--no-separator", "--no-sep", "--noseparator"}
	for _, f := range flags {
		opts := &xdcpOptions{}
		_, err := parseXdcpArgs([]string{f, "node1", "/src", "/dst"}, opts)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", f, err)
		}
		if !opts.noSeparator {
			t.Errorf("expected noSeparator=true for %s", f)
		}
	}
}

func TestResolveTimeoutXdcp(t *testing.T) {
	tests := []struct {
		cli  int
		env  string
		want int
	}{
		{-1, "", 5},
		{-1, "15", 15},
		{-1, "0", 0},
		{-1, "invalid", 5},
		{0, "", 0},
		{3, "10", 3},
	}
	for _, tc := range tests {
		got := resolveTimeout(tc.cli, tc.env)
		if got != tc.want {
			t.Errorf("resolveTimeout(%d, %q) = %d; want %d", tc.cli, tc.env, got, tc.want)
		}
	}
}

func TestBuildCopyCmd(t *testing.T) {
	r := &runner{
		cfg: &dcpConfig{
			remoteCopy:    "/usr/bin/rsync",
			isRsync:       true,
			sshPath:       "/usr/bin/ssh",
			openSSH:       true,
			ignoreHostKey: true,
			preserve:      true,
			recursive:     true,
			sudo:          true,
			user:          "root",
			targetPath:    "/tmp",
		},
	}

	cmd := r.buildCopyCmd("node1", []string{"/src/file"}, "/tmp", false)
	args := cmd.Args
	if args[0] != "/usr/bin/rsync" {
		t.Errorf("expected /usr/bin/rsync, got %s", args[0])
	}
	foundRsyncPath := false
	foundHostKey := false
	for _, a := range args {
		if a == "--rsync-path=sudo rsync" {
			foundRsyncPath = true
		}
		if len(a) > 0 && a[:2] == "-e" || len(a) > 5 && a[len(a)-5:] == "ERROR" {
			foundHostKey = true
		}
	}
	if !foundRsyncPath {
		t.Errorf("expected --rsync-path=sudo rsync in args: %v", args)
	}
	if !foundHostKey {
		t.Errorf("expected host key option in ssh wrapper: %v", args)
	}

	// Test pull mode
	rPull := &runner{
		cfg: &dcpConfig{
			remoteCopy: "/usr/bin/scp",
			isRsync:    false,
			openSSH:    true,
			preserve:   true,
			targetPath: "/tmp/pulled",
		},
	}
	cmdPull := rPull.buildCopyCmd("node2", []string{"/etc/hosts"}, "/tmp/pulled", true)
	argsPull := cmdPull.Args
	lastArg := argsPull[len(argsPull)-1]
	if lastArg != "/tmp/pulled/hosts._node2" {
		t.Errorf("expected dest /tmp/pulled/hosts._node2, got %s", lastArg)
	}
}

