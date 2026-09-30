package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
)

var inFlightCounter atomic.Int32

type regexpT = regexp.Regexp

func compileRe(s string) *regexp.Regexp { return regexp.MustCompile(s) }

// parseXdshArgs parses the xdsh option set (client-level flags from
// xdsh parse_args_xdsh + DSHCLI parse_and_run_dsh GetOptions).
// Returns the remaining command arguments.
func parseXdshArgs(args []string, opts *xdshOptions) ([]string, error) {
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
		case a == "-e" || a == "--execute":
			v, err := needVal(); if err != nil { return nil, err }; opts.execute = v
		case a == "-f" || a == "--fanout":
			v, err := needVal(); if err != nil { return nil, err }
			n, err2 := atoi(v); if err2 != nil || n < 0 { return nil, fmt.Errorf("invalid fanout") }; opts.fanout = n
		case a == "-h" || a == "--help":
			opts.help = true
		case a == "-l" || a == "--user":
			v, err := needVal(); if err != nil { return nil, err }; opts.user = v
		case a == "-m" || a == "--monitor":
			opts.monitor = true
		case a == "-o" || a == "--node-options":
			v, err := needVal(); if err != nil { return nil, err }; opts.nodeOptions = v
		case a == "-q" || a == "--show-config":
			opts.showConfig = true
		case a == "-r" || a == "--node-rsh":
			v, err := needVal(); if err != nil { return nil, err }
			if strings.Contains(v, "rsync") {
				fmt.Fprintf(os.Stderr, "The -r flag cannot be set to rsync.\n")
				os.Exit(1)
			}
			if fi, err := os.Stat(v); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
				fmt.Fprintf(os.Stderr, "The -r flag must be set to an existing executable remote shell.\n")
				os.Exit(1)
			}
			opts.nodeRsh = v
		case a == "-i" || a == "--rootimg":
			_, err := needVal(); if err != nil { return nil, err } // accepted, unused (no images without xCAT)
		case a == "-s" || a == "--stream":
			opts.streaming = true
		case a == "-t" || a == "--timeout":
			v, err := needVal(); if err != nil { return nil, err }
			n, err2 := atoi(v); if err2 != nil || n < 0 { return nil, fmt.Errorf("invalid timeout") }; opts.timeout = n
		case a == "-v" || a == "--verify":
			opts.verify = true
		case a == "-z" || a == "--exit-status":
			opts.exitStatus = true
		case a == "-B" || a == "--bypass":
			opts.bypass = true
		case a == "-c" || a == "--cleanup":
			_, err := needVal(); if err != nil { return nil, err }
			fmt.Fprintf(os.Stderr, "Service node cleanup is not supported by this standalone port.\n")
			os.Exit(1)
		case a == "-E" || a == "--environment":
			v, err := needVal(); if err != nil { return nil, err }; opts.environment = v
		case a == "-I" || a == "--ignore-sig" || a == "--ignoresig":
			v, err := needVal(); if err != nil { return nil, err }; opts.ignoreSig = v
			for _, sig := range strings.Split(v, ",") {
				ignoreSignal(sig)
			}
		case a == "-K" || a == "--keysetup":
			// Key setup requires the xCAT infrastructure; warn and continue.
			fmt.Fprintf(os.Stderr, "-K (ssh key setup) is not supported by this standalone port.\n")
		case a == "-L" || a == "--no-locale":
			opts.noLocale = true
		case a == "-Q" || a == "--silent":
			opts.silent = true
		case a == "-S" || a == "--syntax":
			v, err := needVal(); if err != nil { return nil, err }
			if v != "csh" && v != "ksh" {
				fmt.Fprintf(os.Stderr, "Incorrect argument %q specified on -S flag. \n", v)
				os.Exit(1)
			}
			opts.syntax = v
		case a == "-T" || a == "--trace":
			opts.trace = true
		case a == "-V" || a == "--version":
			opts.version = true
		case a == "-X" || a == "--noexpand":
			// accepted: noderanges here are always expanded locally, but a
			// pre-expanded comma list behaves the same way
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				v, _ := needVal()
				_ = v
			}
		case a == "-k" || a == "--ignore-host-key" || a == "--ignore-hostkey" || a == "--insecure" || a == "--no-host-key-check":
			if hasVal {
				opts.ignoreHostKey = isTruthy(val)
			} else {
				opts.ignoreHostKey = true
			}
		case a == "--devicetype":
			v, err := needVal(); if err != nil { return nil, err }; opts.devicetype = v
		case a == "--command-name" || a == "--commandName":
			_, err := needVal(); if err != nil { return nil, err }
		case a == "--command-description" || a == "--commandDescription":
			_, err := needVal(); if err != nil { return nil, err }
		case a == "--nodestatus":
			opts.nodestatus = true
		case a == "--sudo":
			opts.sudo = true
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
	var n int
	var neg bool
	for idx, c := range s {
		if idx == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		return -n, nil
	}
	return n, nil
}

// ignoreSignal installs SIG_IGN for the requested signal names
// (config_signals_dsh, DSHCLI.pm:2767-2804).
func ignoreSignal(name string) {
	var s syscall.Signal
	switch strings.ToUpper(name) {
	case "TERM":
		s = syscall.SIGTERM
	case "QUIT":
		s = syscall.SIGQUIT
	case "INT":
		s = syscall.SIGINT
	case "ABRT":
		s = syscall.SIGABRT
	case "ALRM":
		s = syscall.SIGALRM
	case "PIPE":
		s = syscall.SIGPIPE
	case "USR1":
		s = syscall.SIGUSR1
	case "USR2":
		s = syscall.SIGUSR2
	case "HUP":
		s = syscall.SIGHUP
	case "STOP", "CONT", "TSTP":
		return // refused, same as original
	default:
		return
	}
	signalIgnore(s)
}

// isOpenSSH runs "<shell> -V" and checks for OpenSSH
// (SSH.pm validate_ssh_version, 248-254).
func isOpenSSH(shell string) bool {
	cmd := exec.Command(shell, "-V")
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return false
	}
	return strings.Contains(string(out), "OpenSSH")
}
