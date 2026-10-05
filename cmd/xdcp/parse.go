package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

var inFlightCounter atomic.Int32

// xdcpOptions captures command-line flags for xdcp.
type xdcpOptions struct {
	fanout        int
	file          string // -F synclist file
	help          bool
	user          string // -l user
	monitor       bool   // -m
	nodeOptions   string // -o
	showConfig    bool   // -q
	preserve      bool   // -p
	pull          bool   // -P
	nodeRcp       string // -r, -c
	rootimg       string // -i
	rsyncSN       bool   // -s
	timeout       int    // -t (seconds; default 5; 0 = unlimited)
	verify        bool   // -v
	exitStatus    bool   // -z
	bypass        bool   // -B
	silent        bool   // -Q
	recursive     bool   // -R
	trace         bool   // -T
	version       bool   // -V
	devicetype    string
	sudo          bool
	nodestatus    bool
	ignoreEnv     string
	ignoreHostKey bool // -k
	noSeparator   bool // --no-separator
}

// parseXdcpArgs parses the xdcp options set.
// Returns the remaining positional arguments.
func parseXdcpArgs(args []string, opts *xdcpOptions) ([]string, error) {
	var rest []string
	i := 0
	takeValue := func(name string) (string, error) {
		i++
		if i >= len(args) {
			return "", fmt.Errorf("option %s requires a value", name)
		}
		return args[i], nil
	}
	for ; i < len(args); i++ {
		a := args[i]
		eq := strings.Index(a, "=")
		var val string
		hasVal := false
		if strings.HasPrefix(a, "--") && eq >= 0 {
			val = a[eq+1:]
			hasVal = true
			a = a[:eq]
		}
		needVal := func() (string, error) {
			if hasVal {
				return val, nil
			}
			return takeValue(a)
		}
		switch {
		case a == "-B" || a == "--bypass":
			opts.bypass = true
		case a == "-c" || a == "-r" || a == "--node-rcp":
			v, err := needVal()
			if err != nil {
				return nil, err
			}
			opts.nodeRcp = v
		case a == "-f" || a == "--fanout":
			v, err := needVal()
			if err != nil {
				return nil, err
			}
			n, err2 := atoi(v)
			if err2 != nil || n < 0 {
				return nil, fmt.Errorf("invalid fanout")
			}
			opts.fanout = n
		case a == "-F" || a == "--File":
			v, err := needVal()
			if err != nil {
				return nil, err
			}
			opts.file = v
		case a == "-h" || a == "--help":
			opts.help = true
		case a == "-i" || a == "--rootimg":
			v, err := needVal()
			if err != nil {
				return nil, err
			}
			opts.rootimg = v
		case a == "-l" || a == "--user":
			v, err := needVal()
			if err != nil {
				return nil, err
			}
			opts.user = v
		case a == "-m" || a == "--monitor":
			opts.monitor = true
		case a == "-o" || a == "--node-options":
			v, err := needVal()
			if err != nil {
				return nil, err
			}
			opts.nodeOptions = v
		case a == "-p" || a == "--preserve":
			opts.preserve = true
		case a == "-P" || a == "--pull":
			opts.pull = true
		case a == "-q" || a == "--show-config":
			opts.showConfig = true
		case a == "-Q" || a == "--silent":
			opts.silent = true
		case a == "-R" || a == "--recursive":
			opts.recursive = true
		case a == "-s":
			opts.rsyncSN = true
		case a == "-t" || a == "--timeout":
			v, err := needVal()
			if err != nil {
				return nil, err
			}
			n, err2 := atoi(v)
			if err2 != nil || n < 0 {
				return nil, fmt.Errorf("invalid timeout")
			}
			opts.timeout = n
		case a == "-T" || a == "--trace":
			opts.trace = true
		case a == "-v" || a == "--verify":
			opts.verify = true
		case a == "-V" || a == "--version":
			opts.version = true
		case a == "-z" || a == "--exit-status":
			opts.exitStatus = true
		case a == "-k" || a == "--ignore-host-key" || a == "--ignore-hostkey" || a == "--insecure" || a == "--no-host-key-check":
			if hasVal {
				opts.ignoreHostKey = isTruthy(val)
			} else {
				opts.ignoreHostKey = true
			}
		case a == "--devicetype":
			v, err := needVal()
			if err != nil {
				return nil, err
			}
			opts.devicetype = v
		case a == "--sudo":
			opts.sudo = true
		case a == "--nodestatus":
			opts.nodestatus = true
		case a == "--no-separator" || a == "--no-sep" || a == "--noseparator":
			if hasVal {
				opts.noSeparator = isTruthy(val)
			} else {
				opts.noSeparator = true
			}
		case a == "-X" || a == "--noexpand":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				v, _ := needVal()
				_ = v
			}
		case strings.HasPrefix(a, "-"):
			return nil, fmt.Errorf("unknown option %s", a)
		default:
			rest = append(rest, a)
		}
	}
	return rest, nil
}

func isTruthy(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "1" || s == "true" || s == "yes" || s == "y" || s == "on"
}

func atoi(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
