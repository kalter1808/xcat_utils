// Command xdsh is a standalone port of xCAT's xdsh (xCAT-client/bin/xdsh +
// perl-xCAT/xCAT/DSHCLI.pm), running always in "bypass" mode: no xcatd,
// no xCAT database. Noderanges are expanded locally.
//
// Semantics replicated from DSHCLI.pm/DSHCore.pm/SSH.pm:
//   - default remote shell: /usr/bin/ssh with "-o BatchMode=yes -x"
//     appended for OpenSSH (DSH_NODE_RSH / -r to override)
//   - remote command string: "export NODE=<node>; <pre-command><command>"
//     + "; export DSH_TARGET_RC=$?; echo \":DSH_TARGET_RC=${DSH_TARGET_RC}:\""
//     (the :DSH_TARGET_RC: marker line is stripped from the output)
//   - default fanout 64 (-f / DSH_FANOUT)
//   - output labeled "node: " on every line; per-node buffering by default
//     (stdout of the node, then its stderr); -s streams as data arrives
//   - exit code = number of failed targets
//   - -Q silences output; -z appends "Remote_command_rc = N" per node
//   - -t timeout (seconds) kills children when no output arrives
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"xcat-ports/internal/noderange"
)

const xdshUsage = " xdsh -h \n xdsh -q \n xdsh -V \n" +
	"xdsh  <noderange> [-k|--ignore-host-key] [-K] [-l logonuserid]\n" +
	"      [-B bypass ] [-c] [-e] [-E environment_file]\n" +
	"      [--devicetype type_of_device] [-f fanout]\n" +
	"      [-l user_ID] [-L]  [-m] [-o options][-q] [-Q] [-r remote_shell]\n" +
	"      [-i image path] [-s] [-S ksh | csh] [-t timeout]\n" +
	"      [-T] [-X environment variables] [-v] [-z] [--no-separator]\n" +
	"      <command_list>\n"

const dshVersion = "2.16.x (xcat-ports standalone)"

func main() {
	os.Exit(xdshMain(os.Args[1:]))
}

type xdshOptions struct {
	execute       string
	fanout        int
	help          bool
	user          string
	monitor       bool
	nodeOptions   string
	showConfig    bool
	nodeRsh       string
	streaming     bool
	timeout       int // seconds; default 5; 0 = unlimited
	verify        bool
	exitStatus    bool
	bypass        bool
	environment   string
	ignoreSig     string
	noLocale      bool
	silent        bool
	syntax        string
	trace         bool
	version       bool
	devicetype    string
	commandName   string
	sudo          bool
	nodestatus    bool
	ignoreEnv     string
	rootsEnv      []string
	ignoreHostKey bool
	noSeparator   bool
}

func xdshMain(args []string) int {
	opts := &xdshOptions{
		timeout: -1,
	}
	var noderangeArg string
	var cmdArgs []string

	checkInvalidExports()

	// The original requires the noderange as the first argument unless the
	// command starts with an option (then noderange must be absent).
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		noderangeArg = args[0]
		args = args[1:]
	}

	rest, err := parseXdshArgs(args, opts)
	if err != nil {
		fmt.Print(xdshUsage)
		return 1
	}
	cmdArgs = rest

	if opts.help {
		fmt.Print(xdshUsage)
		return 0
	}
	if opts.version {
		fmt.Println(dshVersion)
		return 0
	}
	if opts.showConfig {
		showDshConfig()
		return 0
	}

	if noderangeArg == "" && len(cmdArgs) > 0 {
		noderangeArg = cmdArgs[0]
		cmdArgs = cmdArgs[1:]
	}

	if noderangeArg == "" {
		fmt.Fprintf(os.Stderr, "Node range not specified, see xdsh man page for syntax.\n")
		return 1
	}
	if len(cmdArgs) == 0 && opts.execute == "" {
		fmt.Fprintf(os.Stderr, "No command specified, see xdsh man page for syntax.\n")
		return 1
	}

	// -B always on in this standalone port (there is no xcatd to bypass);
	// accept the flag for compatibility.
	_ = opts.bypass

	// Resolve user: -l or current user (client sets DSH_TO_USERID).
	// A "user@noderange" prefix on the noderange is supported like ssh/scp
	// target syntax (the plain xCAT xdsh uses -l only). Since '@' is also
	// the noderange intersection operator, disambiguate: a username-like
	// prefix (letters/digits/_/-/., no range syntax) before a single '@'
	// is treated as a user; anything else stays noderange intersection.
	user := opts.user
	nrExpr := noderangeArg
	if at := strings.LastIndex(nrExpr, "@"); at > 0 && isUserName(nrExpr[:at]) {
		if user == "" {
			user = nrExpr[:at]
		}
		nrExpr = nrExpr[at+1:]
	}
	if user == "" {
		if env := os.Getenv("DSH_TO_USERID"); env != "" {
			user = env
		} else {
			user = currentUser()
		}
	}

	nodes, missed := noderange.Expand(nrExpr)
	if len(missed) > 0 {
		fmt.Printf("Invalid nodes in noderange:%s\n", strings.Join(missed, ","))
	}
	if len(nodes) == 0 {
		return 1
	}

	command := strings.TrimSpace(strings.Join(cmdArgs, " "))

	// Environment variable fallback chains (config_dsh, DSHCLI.pm:2439-2442,
	// 2501, 2535, 2670-2672). The original hardcodes /usr/bin/ssh (RHEL);
	// we resolve "ssh" via PATH first and fall back to /usr/bin/ssh.
	remoteShell := opts.nodeRsh
	if remoteShell == "" {
		remoteShell = firstNonEmpty(os.Getenv("DSH_NODE_RSH"), os.Getenv("DSH_REMOTE_CMD"), sshDefaultPath())
	}
	nodeOpts := opts.nodeOptions
	if nodeOpts == "" {
		nodeOpts = firstNonEmpty(os.Getenv("DSH_NODE_OPTS"), os.Getenv("DSH_REMOTE_OPTS"))
	}
	fanout := opts.fanout
	if fanout == 0 {
		if f := os.Getenv("DSH_FANOUT"); f != "" {
			if n, err := strconv.Atoi(f); err == nil {
				fanout = n
			}
		}
	}
	if fanout == 0 {
		fanout = 64
	}
	timeout := resolveTimeout(opts.timeout, os.Getenv("DSH_TIMEOUT"))
	if fanout < 1 {
		fanout = 1
	}

	ignoreHostKey := opts.ignoreHostKey
	if !ignoreHostKey {
		if v := os.Getenv("XDSH_IGNORE_HOST_KEY"); v != "" {
			ignoreHostKey = isTruthy(v)
		} else if v := os.Getenv("DSH_IGNORE_HOST_KEY"); v != "" {
			ignoreHostKey = isTruthy(v)
		}
	}

	noSeparator := opts.noSeparator
	if !noSeparator {
		if v := os.Getenv("XDSH_NO_SEPARATOR"); v != "" {
			noSeparator = isTruthy(v)
		} else if v := os.Getenv("DSH_NO_SEPARATOR"); v != "" {
			noSeparator = isTruthy(v)
		}
	}

	preCommand := buildPreCommand(opts)
	postCommand := buildPostCommand(opts)

	return executeDsh(&dshConfig{
		nodes:         nodes,
		command:       command,
		user:          user,
		remoteShell:   remoteShell,
		nodeOpts:      nodeOpts,
		fanout:        fanout,
		timeout:       timeout,
		streaming:     opts.streaming,
		silent:        opts.silent,
		monitor:       opts.monitor,
		trace:         opts.trace,
		exitStatus:    opts.exitStatus,
		nodestatus:    opts.nodestatus,
		verify:        opts.verify,
		sudo:          opts.sudo,
		execute:       opts.execute,
		preCommand:    preCommand,
		postCommand:   postCommand,
		openSSH:       isOpenSSH(remoteShell),
		ignoreHostKey: ignoreHostKey,
		noSeparator:   noSeparator,
	})
}

func resolveTimeout(cliTimeout int, envTimeout string) int {
	if cliTimeout >= 0 {
		return cliTimeout
	}
	if envTimeout != "" {
		if n, err := strconv.Atoi(envTimeout); err == nil && n >= 0 {
			return n
		}
	}
	return 5
}

func addFailed(failed *[]string, node string) {
	for _, n := range *failed {
		if n == node {
			return
		}
	}
	*failed = append(*failed, node)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	out, err := exec.Command("id", "-un").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

// isUserName reports whether s looks like a login name (user@noderange
// syntax) rather than noderange atoms: no commas, brackets, parens,
// slashes or '@' (which would mean intersection/range syntax).
func isUserName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

// sshDefaultPath mirrors the original's /usr/bin/ssh default while staying
// portable: prefer the ssh found in PATH (NixOS has no /usr/bin), fall back
// to the hardcoded location (SSH.pm: SSH_CMD).
func sshDefaultPath() string {
	if p, err := exec.LookPath("ssh"); err == nil {
		return p
	}
	return "/usr/bin/ssh"
}

// scpDefaultPath is the same for /usr/bin/scp (SSH.pm: SCP_CMD).
func scpDefaultPath() string {
	if p, err := exec.LookPath("scp"); err == nil {
		return p
	}
	return "/usr/bin/scp"
}

// checkInvalidExports warns about unsupported dsh environment variables,
// mirroring xdsh's check_invalid_exports (xdsh lines 779-783 etc).
func checkInvalidExports() {
	for _, v := range []string{
		"DSH_CONTEXT", "DSH_LIST", "DSH_NODE_LIST", "WCOLL",
		"DSH_DEVICE_LIST", "DSH_DEVICE_OPTS", "DSH_DEVICE_RCP", "DSH_DEVICE_RSH",
		"DSH_NODEGROUP_PATH", "RSYNC_RSH", "DSH_REPORT",
	} {
		if os.Getenv(v) != "" {
			fmt.Printf("%s is set but is not supported. It will be ignored.\n", v)
		}
	}
}

func showDshConfig() {
	fmt.Println("DSH_CONF_DIR: (not used by this standalone port)")
	fmt.Println("Fanout Value: 64")
	fmt.Println("Remote Shell: /usr/bin/ssh")
	fmt.Println("Remote Copy:  /usr/bin/scp")
	fmt.Println("DSH_PATH: (default)")
	fmt.Println("DISPLAYfu: (default)")
	fmt.Println("RSH_TYPE: ssh")
}

// buildPreCommand mirrors config_dsh (DSHCLI.pm:2549-2593): a PATH export
// (from DSH_PATH) plus locale exports from /usr/bin/locale, unless -L.
func buildPreCommand(opts *xdshOptions) string {
	var sb strings.Builder
	if p := os.Getenv("DSH_PATH"); p != "" {
		sb.WriteString("export PATH=" + p + ";")
	}
	if opts.noLocale {
		return sb.String()
	}
	out, err := exec.Command("/usr/bin/locale").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			sb.WriteString(line)
			sb.WriteString(" ")
		}
	}
	sb.WriteString("PERL_BADLANG=0 ; ")
	return sb.String()
}

// buildPostCommand mirrors config_dsh (DSHCLI.pm:2604-2622): capture the
// remote command's return code through the :DSH_TARGET_RC: marker.
func buildPostCommand(opts *xdshOptions) string {
	pc := "; export DSH_TARGET_RC=$?; echo \":DSH_TARGET_RC=${DSH_TARGET_RC}:\""
	if opts.exitStatus {
		pc += " ; echo \"Remote_command_rc = $DSH_TARGET_RC\""
	}
	return pc
}

// dshConfig is the fully resolved dsh configuration (options hash in DSHCLI).
type dshConfig struct {
	nodes         []string
	command       string
	user          string
	remoteShell   string
	nodeOpts      string
	fanout        int
	timeout       int
	streaming     bool
	silent        bool
	monitor       bool
	trace         bool
	exitStatus    bool
	nodestatus    bool
	verify        bool
	sudo          bool
	execute       string // -e script path
	preCommand    string
	postCommand   string
	openSSH       bool
	ignoreHostKey bool
	noSeparator   bool
}

// targetResult captures a finished target.
type targetResult struct {
	node     string
	exitCode int    // ssh process exit code
	targetRC int    // remote command rc (from :DSH_TARGET_RC:)
	rcSeen   bool   // whether the marker was received
	stdout   []string
	stderr   []string
}

type runner struct {
	cfg         *dshConfig
	results     chan *targetResult
	sigCh       chan os.Signal
	interrupted bool
	mu          sync.Mutex
	children    map[string]*exec.Cmd
	hasOutput   bool
}

// executeDsh mirrors fork_fanout_dsh + _execute_dsh: spawn up to fanout
// ssh processes, multiplex their stdout/stderr, buffer per node, classify
// success/failure, return the number of failed targets.
func executeDsh(cfg *dshConfig) int {
	r := &runner{
		cfg:      cfg,
		results:  make(chan *targetResult, len(cfg.nodes)),
		children: map[string]*exec.Cmd{},
	}
	signal.Ignore(syscall.SIGPIPE)
	r.sigCh = make(chan os.Signal, 1)
	signal.Notify(r.sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer signal.Stop(r.sigCh)
	go func() {
		for range r.sigCh {
			fmt.Fprintf(os.Stderr, "Caught SIGINT - terminating the child processes.\n")
			r.mu.Lock()
			for _, cmd := range r.children {
				if cmd.Process != nil {
					cmd.Process.Signal(syscall.SIGINT)
				}
			}
			r.interrupted = true
			r.mu.Unlock()
		}
	}()

	// Spawn in waves of fanout; collect results as they complete. Like the
	// original select loop, each round processes every finished target of
	// the round together, printed in sorted order (_execute_dsh:565).
	waiting := append([]string{}, cfg.nodes...)
	var failed []string
	sem := make(chan struct{}, cfg.fanout)
	var wg sync.WaitGroup

	for len(waiting) > 0 || inFlight(&wg) > 0 {
		// Fill the fanout window.
		for len(waiting) > 0 && inFlight(&wg) < cfg.fanout {
			node := waiting[0]
			waiting = waiting[1:]
			wg.Add(1)
			sem <- struct{}{}
			go func(node string) {
				defer wg.Done()
				defer func() { <-sem }()
				inFlightCounter.Add(1)
				defer inFlightCounter.Add(-1)
				res := r.runTarget(node)
				r.results <- res
			}(node)
			if cfg.monitor {
				fmt.Printf("dsh>  Remote_command_started %s\n", node)
			}
			if cfg.trace {
				fmt.Printf("Command name: %s %s\n", cfg.remoteShell, node)
			}
		}

		// Wait for one result (with optional timeout on inactivity).
		var res *targetResult
		if cfg.timeout > 0 {
			select {
			case res = <-r.results:
			case <-time.After(time.Duration(cfg.timeout) * time.Second):
				fmt.Fprintf(os.Stderr, " Timed out waiting for response from child processes for the following nodes. Terminating the child processes. \n")
				r.mu.Lock()
				active := make([]string, 0, len(r.children))
				for n := range r.children {
					active = append(active, n)
				}
				for _, cmd := range r.children {
					r.signalChild(cmd)
				}
				r.mu.Unlock()
				sort.Strings(active)
				fmt.Fprintf(os.Stderr, " %s\n", strings.Join(active, " "))
				for _, n := range active {
					addFailed(&failed, n)
				}
				// The original does NOT cancel still-waiting targets on a
				// timeout (the `last` is commented out at _execute_dsh:507),
				// so waiting nodes keep being started.
				continue
			}
		} else {
			res = <-r.results
		}
		if res == nil {
			continue
		}
		// Non-blocking drain of everything finished in this round, then
		// report in sorted node order.
		batch := []*targetResult{res}
		for {
			select {
			case res2 := <-r.results:
				batch = append(batch, res2)
			default:
				goto report
			}
		}
	report:
		sort.Slice(batch, func(i, j int) bool { return batch[i].node < batch[j].node })
		for _, res := range batch {
			r.report(res, &failed)
		}
	}
	// Reap any leftover children (they were signaled on the timeout path).
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		r.killChildren()
		<-done
	}
	for {
		select {
		case res := <-r.results:
			r.report(res, &failed)
		default:
			goto reaped
		}
	}
reaped:

	if cfg.monitor {
		fmt.Println("dsh>  Remote_command_execution_completed")
	}
	return len(failed)
}

// signalChild sends SIGINT to the child's process group (each child gets
// its own group via Setpgid, so ssh and its remote-side helpers all get
// the signal, mirroring "Terminating the child processes").
func (r *runner) signalChild(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	cmd.Process.Signal(syscall.SIGINT)
}

func (r *runner) killChildren() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cmd := range r.children {
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			cmd.Process.Kill()
		}
	}
}

func inFlight(wg *sync.WaitGroup) int {
	return int(inFlightCounter.Load())
}

func (r *runner) hasContent(res *targetResult) bool {
	cfg := r.cfg
	if !cfg.silent && (len(res.stdout) > 0 || len(res.stderr) > 0) {
		return true
	}
	if cfg.nodestatus {
		return true
	}
	if cfg.monitor {
		return true
	}
	if !res.rcSeen && !strings.HasSuffix(cfg.command, "&") {
		return true
	}
	return false
}

func (r *runner) report(res *targetResult, failed *[]string) {
	cfg := r.cfg

	if !cfg.noSeparator && !cfg.streaming && r.hasContent(res) {
		if r.hasOutput {
			if len(res.stdout) > 0 || cfg.nodestatus || cfg.monitor {
				fmt.Println()
			} else {
				fmt.Fprintln(os.Stderr)
			}
		}
		r.hasOutput = true
	}

	// Default (buffered) mode: print the node's stdout then its stderr,
	// each line labeled "node: " (buffer_output + _execute_dsh print loop).
	if !cfg.silent {
		for _, l := range res.stdout {
			fmt.Printf("%s: %s\n", res.node, l)
		}
		for _, l := range res.stderr {
			fmt.Fprintf(os.Stderr, "%s: %s\n", res.node, l)
		}
	}

	r.mu.Lock()
	interrupted := r.interrupted
	r.mu.Unlock()

	if res.exitCode != 0 {
		if cfg.nodestatus {
			fmt.Printf("%s: Remote_command_failed, error_code=%d\n", res.node, res.exitCode)
		}
		if cfg.monitor {
			fmt.Printf("dsh>  Remote_command_failed %s\n", res.node)
		}
		if !interrupted {
			addFailed(failed, res.node)
		}
		return
	}
	if res.rcSeen && res.targetRC != 0 {
		if cfg.nodestatus {
			fmt.Printf("%s: Remote_command_failed, error_code=%d\n", res.node, res.targetRC)
		}
		if cfg.monitor {
			fmt.Printf("dsh>  Remote_command_failed %s\n", res.node)
		}
		addFailed(failed, res.node)
		return
	}
	if !res.rcSeen && !strings.HasSuffix(cfg.command, "&") {
		if cfg.nodestatus {
			fmt.Printf("%s: Remote_command_failed, error_code=???\n", res.node)
		}
		fmt.Fprintf(os.Stderr, " A return code for the command run on the host %s was not received.\n", res.node)
		if cfg.monitor {
			fmt.Printf("dsh>  Remote_command_failed %s\n", res.node)
		}
		addFailed(failed, res.node)
		return
	}
	if cfg.nodestatus {
		fmt.Printf("%s: Remote_command_successful\n", res.node)
	}
	if cfg.monitor {
		fmt.Printf("dsh>  Remote_command_successful %s\n", res.node)
	}
}

// runTarget spawns ssh for one node and collects labeled output
// (fork_fanout_dsh + fork_output_for_commands + pipe_handler_buffer).
func (r *runner) runTarget(node string) *targetResult {
	cfg := r.cfg

	// Command string built like fork_fanout_dsh (DSHCLI.pm:1099-1167):
	// export NODE=<node>; <pre><command><post>
	cmdStr := "export NODE=" + node + "; " + cfg.preCommand
	if cfg.execute != "" {
		tmp := "/tmp/xdsh-" + strconv.Itoa(os.Getpid()) + ".dsh"
		// Copy script to the node first (scp), then run it.
		if err := r.copyScriptTo(node, cfg.execute, tmp); err != nil {
			return &targetResult{node: node, exitCode: 1, rcSeen: false,
				stderr: []string{fmt.Sprintf("cannot copy %s to %s: %v", cfg.execute, node, err)}}
		}
		sudo := ""
		if cfg.sudo {
			sudo = "sudo "
		}
		cmdStr += sudo + " " + tmp + cfg.postCommand + ";rm " + tmp
	} else {
		sudo := ""
		if cfg.sudo {
			sudo = "sudo "
		}
		cmdStr += sudo + cfg.command + cfg.postCommand
	}

	// ssh argv (SSH.pm remote_shell_command, 59-86):
	// ssh [user options] [-o BatchMode=yes] [-x] [user@]node '<command>'
	args := []string{}
	if cfg.nodeOpts != "" {
		args = append(args, strings.Fields(cfg.nodeOpts)...)
	}
	if cfg.openSSH {
		args = append(args, "-o", "BatchMode=yes")
		if !strings.Contains(cfg.nodeOpts, "-X") {
			args = append(args, "-x")
		}
	}
	if cfg.ignoreHostKey {
		args = append(args,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "GlobalKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
		)
	}
	target := node
	if cfg.user != "" && cfg.user != currentUser() {
		target = cfg.user + "@" + node
	}
	args = append(args, target, cmdStr)

	cmd := exec.Command(cfg.remoteShell, args...)
	stdout, err1 := cmd.StdoutPipe()
	stderr, err2 := cmd.StderrPipe()
	if err1 != nil || err2 != nil {
		return &targetResult{node: node, exitCode: 255, rcSeen: false,
			stderr: []string{"cannot create pipes"}}
	}
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	r.mu.Lock()
	r.children[node] = cmd
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.children, node)
		r.mu.Unlock()
	}()

	if err := cmd.Start(); err != nil {
		return &targetResult{node: node, exitCode: 255, rcSeen: false,
			stderr: []string{fmt.Sprintf("%s could not execute this command %s - %s , %v", node, cfg.remoteShell, cfg.command, err)}}
	}

	res := &targetResult{node: node}
	var wg sync.WaitGroup
	var mu sync.Mutex // guards res.stdout/res.stderr/rc fields

	handle := func(rd io.Reader, isErr bool) {
		defer wg.Done()
		sc := bufio.NewScanner(rd)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var pending string // partial line buffer (pipe_handler_buffer ${node}_tmp)
		for sc.Scan() {
			line := pending + sc.Text()
			pending = ""
			// Strip the :DSH_TARGET_RC=N: marker line (DSHCore.pm:373-382).
			if strings.Contains(line, ":DSH_TARGET_RC=") {
				if m := targetRCRe.FindStringSubmatch(line); m != nil {
					rc, _ := strconv.Atoi(m[1])
					mu.Lock()
					res.targetRC = rc
					res.rcSeen = true
					mu.Unlock()
					continue
				}
			}
			if cfg.streaming && !cfg.silent {
				if isErr {
					fmt.Fprintf(os.Stderr, "%s: %s\n", node, line)
				} else {
					fmt.Printf("%s: %s\n", node, line)
				}
			} else {
				mu.Lock()
				if isErr {
					res.stderr = append(res.stderr, line)
				} else {
					res.stdout = append(res.stdout, line)
				}
				mu.Unlock()
			}
		}
		if sc.Err() != nil && pending != "" {
			mu.Lock()
			if isErr {
				res.stderr = append(res.stderr, pending)
			} else {
				res.stdout = append(res.stdout, pending)
			}
			mu.Unlock()
		}
	}
	wg.Add(2)
	go handle(stdout, false)
	go handle(stderr, true)
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.exitCode = ee.ExitCode()
		} else {
			res.exitCode = 255
		}
	}
	return res
}

var targetRCRe = regexpMustCompile(`:DSH_TARGET_RC=(\d+):`)

// copyScriptTo scp's a script to the node (-e support, fork_no_output for
// the env/exec file copies in DSHCLI.pm:1290-1300).
func (r *runner) copyScriptTo(node, src, dst string) error {
	target := node
	if r.cfg.user != "" && r.cfg.user != currentUser() {
		target = r.cfg.user + "@" + node
	}
	args := []string{}
	if r.cfg.nodeOpts != "" {
		args = append(args, strings.Fields(r.cfg.nodeOpts)...)
	}
	if r.cfg.openSSH {
		args = append(args, "-B")
	}
	if r.cfg.ignoreHostKey {
		args = append(args,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "GlobalKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
		)
	}
	args = append(args, src, target+":"+dst)
	return exec.Command(scpDefaultPath(), args...).Run()
}

func regexpMustCompile(s string) *regexpT { return compileRe(s) }
