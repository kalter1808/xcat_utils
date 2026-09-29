// Command pping is a standalone port of xCAT's pping (xCAT-client/bin/pping).
//
// It expands a noderange locally (no xcatd) and reports, for each node,
// "<node>: ping" or "<node>: noping" by frontending nmap (default, when
// available) or fping (-f), exactly like the original:
//
//	pping [-i|--interface interfaces] noderange
//	pping -f|--use_fping
//	pping -h|--help
//	pping -v|--version
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"xcat-ports/internal/noderange"
)

const ppingUsage = "Usage: pping [-i|--interface interfaces] noderange\n" +
	"       pping -f|--use_fping\n" +
	"       pping -h|--help\n" +
	"       pping -v|--version\n"

func main() {
	args := os.Args[1:]
	var (
		useFping bool
		help     bool
		version  bool
		noexpand bool
		iface    string
		noderangeArg string
	)

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-f" || a == "--use_fping":
			useFping = true
		case a == "-h" || a == "--help":
			help = true
		case a == "-v" || a == "--version":
			version = true
		case a == "-X" || a == "--noexpand":
			noexpand = true
		case a == "-i" || a == "--interface" || strings.HasPrefix(a, "--interface="):
			if strings.HasPrefix(a, "--interface=") {
				iface = strings.TrimPrefix(a, "--interface=")
			} else {
				i++
				if i >= len(args) {
					fmt.Print(ppingUsage)
					os.Exit(1)
				}
				iface = args[i]
			}
		default:
			if strings.HasPrefix(a, "-") && noderangeArg == "" {
				// Unknown option: original prints usage and exits 1.
				fmt.Print(ppingUsage)
				os.Exit(1)
			}
			if noderangeArg != "" {
				fmt.Print(ppingUsage)
				os.Exit(1)
			}
			noderangeArg = a
		}
	}

	if help {
		fmt.Print(ppingUsage)
		os.Exit(0)
	}
	if version {
		fmt.Println("pping (xcat-ports) 2.16.x-compatible")
		os.Exit(0)
	}
	if noderangeArg == "" {
		fmt.Print(ppingUsage)
		os.Exit(1)
	}

	var nodes []string
	if noexpand {
		// Already expanded by a caller (ppping); simple comma split.
		nodes = strings.Split(noderangeArg, ",")
	} else {
		var missed []string
		nodes, missed = noderange.Expand(noderangeArg)
		for _, m := range missed {
			fmt.Printf("Warning: Invalid nodes in noderange:%s\n", m)
		}
	}
	if len(nodes) == 0 {
		os.Exit(1)
	}

	usenmap := executableExists("/usr/bin/nmap") || executableExists("/usr/local/bin/nmap") ||
		executableInPath("nmap")
	if useFping {
		usenmap = false
	}

	var interfaces []string
	if iface != "" {
		interfaces = strings.Split(iface, ",")
	} else {
		interfaces = []string{""}
	}

	for _, interf := range interfaces {
		var targets []string
		if interf != "" {
			// Copy node list and append the interface suffix, stripping
			// the "-hf<n>" suffix first (pping lines 139-147).
			targets = make([]string, len(nodes))
			for idx, n := range nodes {
				n = regexpStripHF.ReplaceStringString(n)
				targets[idx] = n + "-" + interf
			}
		} else {
			targets = nodes
		}
		if usenmap {
			nmapPping(targets)
		} else {
			fpingPping(targets)
		}
	}
}

// nmapPping mirrors pping's nmap_pping: nmap -PE --system-dns --send-ip
// -sP --unprivileged -PA80,443,22; nodes reported up get "<node>: ping",
// everything else "<node>: noping" (sorted).
func nmapPping(nodes []string) {
	dead := map[string]bool{}
	for _, n := range nodes {
		dead[n] = true
	}
	args := []string{"-PE", "--system-dns", "--send-ip", "-sP", "--unprivileged", "-PA80,443,22"}
	args = append(args, nodes...)
	cmd := exec.Command("nmap", args...)
	stdout, _ := cmd.StdoutPipe()
	// Original discards nmap stderr (2> /dev/null).
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open nmap pipe: %v\n", err)
		os.Exit(1)
	}
	sc := bufio.NewScanner(stdout)
	var node string
	for sc.Scan() {
		line := sc.Text()
		if m := hostUpRe.FindStringSubmatch(line); m != nil {
			node = m[1]
			node = mapToRequested(node, dead)
			if node != "" {
				delete(dead, node)
				fmt.Printf("%s: ping\n", node)
			}
		} else if m := nmapReportRe.FindStringSubmatch(line); m != nil {
			node = m[1]
		} else if strings.Contains(line, "Host is up.") {
			node = mapToRequested(node, dead)
			if node != "" {
				delete(dead, node)
				fmt.Printf("%s: ping\n", node)
			}
		}
	}
	cmd.Wait()
	for _, n := range sortedKeys(dead) {
		fmt.Printf("%s: noping\n", n)
	}
}

// mapToRequested maps a resolved nmap hostname back to the requested node
// name: nmap may report FQDNs while the user asked for short names
// (pping lines 215-224: "if ($node =~ /^$_\./) { $node = $_ }").
func mapToRequested(node string, dead map[string]bool) string {
	if node == "" || dead[node] {
		return node
	}
	for d := range dead {
		if strings.HasPrefix(node, d+".") {
			return d
		}
	}
	return node
}

// fpingPping mirrors pping's fping_pping: run fping (or fping6 when the
// master resolves to IPv6), rewriting output lines the same way:
//
//	"x is unreachable" -> "x: noping"
//	"x is alive"       -> "x: ping"
//	"x address not found" -> "x: noping"
func fpingPping(nodes []string) {
	bin := "fping"
	if !executableInPath(bin) && !executableExists("/usr/bin/fping") && !executableExists("/usr/sbin/fping") {
		fmt.Println("fping is not available, please install missing package fping.")
		os.Exit(1)
	}
	args := append([]string{}, nodes...)
	cmd := exec.Command(bin, args...)
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout // original pipes 2>&1 together
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open fping pipe: %v\n", err)
		os.Exit(1)
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, "is unreachable"):
			line = strings.Replace(line, " is unreachable", ": noping", 1)
		case strings.Contains(line, "is alive"):
			line = strings.Replace(line, " is alive", ": ping", 1)
		case strings.Contains(line, "address not found"):
			line = strings.Replace(line, " address not found", ": noping", 1)
		}
		fmt.Println(line)
	}
	cmd.Wait()
}
