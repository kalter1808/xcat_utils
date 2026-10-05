// Command xdcp is a standalone port of xCAT's xdcp (xCAT-client/bin/xdcp +
// perl-xCAT/xCAT/DSHCLI.pm), running always in "bypass" mode: no xcatd,
// no xCAT database. Noderanges are expanded locally.
//
// Capabilities:
//   - Push mode: copy local files/directories to multiple nodes in parallel
//   - Pull mode (-P): pull files from multiple nodes into a local directory,
//     appending "._<node>" to each filename/dirname
//   - Synclist mode (-F): sync files to nodes or image according to a synclist file
//   - Image mode (-i): sync files locally directly into an installation rootimg
//   - Default remote copy tool: rsync (fallback to scp, override with -r / DSH_NODE_RCP)
//   - Fanout (-f / DSH_FANOUT, default 64)
//   - Inactivity timeout (-t / DSH_TIMEOUT, default 5s, 0 = unlimited)
//   - Host key ignore (-k / --ignore-host-key / XDCP_IGNORE_HOST_KEY)
//   - Blank line separation between multi-host outputs (override with --no-separator)
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"xcat-ports/internal/noderange"
)

const xdcpUsage = " xdcp -h \n xdcp -q \n xdcp -V \n" +
	"xdcp  <noderange> [-k|--ignore-host-key] [-f fanout] [-l user_ID]\n" +
	"      [-o options] [-p] [-P] [-q] [-Q] [-r remote_copy]\n" +
	"      [-R] [-t timeout] [-T] [-v] [--sudo] [--nodestatus]\n" +
	"      [--no-separator] source_file... target_path\n" +
	"xdcp  <noderange> [-k|--ignore-host-key] [-f fanout] [-l user_ID]\n" +
	"      [-o options] [-p] [-q] [-Q] [-r remote_copy]\n" +
	"      [-t timeout] [-T] [-v] [--sudo] [--nodestatus]\n" +
	"      [--no-separator] -F <synclist_file>\n" +
	"xdcp  [-i image_path] -F <synclist_file> [-o options] [-T] [-v]\n"

const dcpVersion = "2.16.x (xcat-ports standalone)"

func main() {
	os.Exit(xdcpMain(os.Args[1:]))
}

func xdcpMain(args []string) int {
	opts := &xdcpOptions{
		timeout: -1,
	}
	var noderangeArg string

	checkInvalidExports()

	// Extract noderange if first argument is not an option
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		noderangeArg = args[0]
		args = args[1:]
	}

	rest, err := parseXdcpArgs(args, opts)
	if err != nil {
		fmt.Print(xdcpUsage)
		return 1
	}

	if opts.help {
		fmt.Print(xdcpUsage)
		return 0
	}
	if opts.version {
		fmt.Println(dcpVersion)
		return 0
	}

	// Resolve remote copy tool early for show-config
	remoteCopy, isRsync, rcpErr := resolveRemoteCopy(opts.nodeRcp, os.Getenv("DSH_NODE_RCP"))
	if opts.showConfig {
		showDshConfig(remoteCopy)
		return 0
	}

	// Image update mode (-i rootimg)
	if opts.rootimg != "" {
		if opts.file == "" {
			fmt.Fprintf(os.Stderr, "If -i option is used, then the -F option must input the file list.\n")
			return 1
		}
		if noderangeArg != "" || len(rest) > 0 {
			fmt.Fprintf(os.Stderr, "Input noderange and other flags are not valid with -i flag.\n")
			return 1
		}
		entries, err := parseSynclistFile(opts.file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		if err := copyLocalToImage(opts.rootimg, entries, opts.trace); err != nil {
			fmt.Fprintf(os.Stderr, "Error copying to image %s: %v\n", opts.rootimg, err)
			return 1
		}
		return 0
	}

	// Handle synclist file vs positional files
	var synclistEntries []syncEntry
	var sources []string
	var targetPath string

	if opts.file != "" {
		// -F mode
		if noderangeArg == "" && len(rest) > 0 {
			noderangeArg = rest[0]
			rest = rest[1:]
		}
		if noderangeArg == "" {
			fmt.Fprintf(os.Stderr, "Noderange missing in command input.\n")
			return 1
		}
		entries, err := parseSynclistFile(opts.file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		synclistEntries = entries
	} else {
		// Standard push or pull copy
		if noderangeArg == "" {
			if len(rest) < 3 {
				fmt.Fprintf(os.Stderr, "Missing noderange or file arguments, see xdcp man page for syntax.\n")
				return 1
			}
			noderangeArg = rest[0]
			rest = rest[1:]
		}
		if len(rest) < 2 {
			fmt.Fprintf(os.Stderr, "Missing file arguments, see xdcp man page for syntax.\n")
			return 1
		}
		targetPath = rest[len(rest)-1]
		sources = rest[:len(rest)-1]

		if opts.pull {
			if len(sources) != 1 {
				fmt.Fprintf(os.Stderr, "Cannot pull more than one file from targets.\n")
				return 1
			}
			fi, err := os.Stat(targetPath)
			if err != nil || !fi.IsDir() {
				fmt.Fprintf(os.Stderr, "Target path %s must be an existing directory for pull mode.\n", targetPath)
				return 1
			}
		}
	}

	if rcpErr != nil {
		fmt.Fprintf(os.Stderr, "%v\n", rcpErr)
		return 1
	}

	// Resolve user: -l, user@noderange, DSH_TO_USERID, or currentUser
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
	if fanout < 1 {
		fanout = 1
	}
	timeout := resolveTimeout(opts.timeout, os.Getenv("DSH_TIMEOUT"))

	ignoreHostKey := opts.ignoreHostKey
	if !ignoreHostKey {
		if v := os.Getenv("XDCP_IGNORE_HOST_KEY"); v != "" {
			ignoreHostKey = isTruthy(v)
		} else if v := os.Getenv("XDSH_IGNORE_HOST_KEY"); v != "" {
			ignoreHostKey = isTruthy(v)
		} else if v := os.Getenv("DSH_IGNORE_HOST_KEY"); v != "" {
			ignoreHostKey = isTruthy(v)
		}
	}

	noSeparator := opts.noSeparator
	if !noSeparator {
		if v := os.Getenv("XDCP_NO_SEPARATOR"); v != "" {
			noSeparator = isTruthy(v)
		} else if v := os.Getenv("XDSH_NO_SEPARATOR"); v != "" {
			noSeparator = isTruthy(v)
		} else if v := os.Getenv("DSH_NO_SEPARATOR"); v != "" {
			noSeparator = isTruthy(v)
		}
	}

	sshPath := sshDefaultPath()

	return executeDcp(&dcpConfig{
		nodes:         nodes,
		sources:       sources,
		targetPath:    targetPath,
		pull:          opts.pull,
		synclist:      synclistEntries,
		user:          user,
		remoteCopy:    remoteCopy,
		isRsync:       isRsync,
		sshPath:       sshPath,
		nodeOpts:      nodeOpts,
		fanout:        fanout,
		timeout:       timeout,
		silent:        opts.silent,
		monitor:       opts.monitor,
		trace:         opts.trace,
		preserve:      opts.preserve,
		recursive:     opts.recursive,
		exitStatus:    opts.exitStatus,
		nodestatus:    opts.nodestatus,
		verify:        opts.verify,
		sudo:          opts.sudo,
		openSSH:       isOpenSSH(sshPath),
		ignoreHostKey: ignoreHostKey,
		noSeparator:   noSeparator,
	})
}

type dcpConfig struct {
	nodes         []string
	sources       []string
	targetPath    string
	pull          bool
	synclist      []syncEntry
	user          string
	remoteCopy    string
	isRsync       bool
	sshPath       string
	nodeOpts      string
	fanout        int
	timeout       int
	silent        bool
	monitor       bool
	trace         bool
	preserve      bool
	recursive     bool
	exitStatus    bool
	nodestatus    bool
	verify        bool
	sudo          bool
	openSSH       bool
	ignoreHostKey bool
	noSeparator   bool
}

type targetResult struct {
	node     string
	exitCode int
	stdout   []string
	stderr   []string
}

type runner struct {
	cfg         *dcpConfig
	results     chan *targetResult
	sigCh       chan os.Signal
	interrupted bool
	mu          sync.Mutex
	children    map[string]*exec.Cmd
	hasOutput   bool
}

func executeDcp(cfg *dcpConfig) int {
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

	waiting := append([]string{}, cfg.nodes...)
	var failed []string
	sem := make(chan struct{}, cfg.fanout)
	var wg sync.WaitGroup

	for len(waiting) > 0 || inFlight(&wg) > 0 {
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
				fmt.Printf("dcp>  Remote_copy_started %s\n", node)
			}
			if cfg.trace {
				fmt.Printf("Command name: %s %s\n", cfg.remoteCopy, node)
			}
		}

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
				continue
			}
		} else {
			res = <-r.results
		}
		if res == nil {
			continue
		}

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
		fmt.Println("dcp>  Remote_copy_execution_completed")
	}
	return len(failed)
}

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
	if cfg.nodestatus || cfg.monitor || cfg.exitStatus {
		return true
	}
	return false
}

func (r *runner) report(res *targetResult, failed *[]string) {
	cfg := r.cfg

	if !cfg.noSeparator && r.hasContent(res) {
		if r.hasOutput {
			if len(res.stdout) > 0 || cfg.nodestatus || cfg.monitor || cfg.exitStatus {
				fmt.Println()
			} else {
				fmt.Fprintln(os.Stderr)
			}
		}
		r.hasOutput = true
	}

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

	if cfg.exitStatus {
		fmt.Printf("%s: Remote_copy_rc = %d\n", res.node, res.exitCode)
	}

	if res.exitCode != 0 {
		if cfg.nodestatus {
			fmt.Printf("%s: Remote_copy_failed, error_code=%d\n", res.node, res.exitCode)
		}
		if cfg.monitor {
			fmt.Printf("dcp>  Remote_copy_failed %s\n", res.node)
		}
		if !interrupted {
			addFailed(failed, res.node)
		}
		return
	}

	if cfg.nodestatus {
		fmt.Printf("%s: Remote_copy_successful\n", res.node)
	}
	if cfg.monitor {
		fmt.Printf("dcp>  Remote_copy_successful %s\n", res.node)
	}
}

func (r *runner) runTarget(node string) *targetResult {
	res := &targetResult{node: node}
	cfg := r.cfg

	if len(cfg.synclist) > 0 {
		entries := filterSyncEntriesForNode(cfg.synclist, node)
		if len(entries) == 0 {
			res.exitCode = 0
			return res
		}
		for _, entry := range entries {
			cmd := r.buildCopyCmd(node, entry.sources, entry.target, false)
			exitCode := r.executeChild(node, cmd, res)
			if exitCode != 0 {
				res.exitCode = exitCode
				return res
			}
		}
		res.exitCode = 0
		return res
	}

	cmd := r.buildCopyCmd(node, cfg.sources, cfg.targetPath, cfg.pull)
	res.exitCode = r.executeChild(node, cmd, res)
	return res
}

func (r *runner) executeChild(node string, cmd *exec.Cmd, res *targetResult) int {
	stdout, err1 := cmd.StdoutPipe()
	stderr, err2 := cmd.StderrPipe()
	if err1 != nil || err2 != nil {
		res.stderr = append(res.stderr, "cannot create pipes")
		return 255
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		res.stderr = append(res.stderr, fmt.Sprintf("cannot start %s: %v", r.cfg.remoteCopy, err))
		return 255
	}

	r.mu.Lock()
	r.children[node] = cmd
	r.mu.Unlock()

	var wg sync.WaitGroup
	var mu sync.Mutex

	scanStream := func(pipe io.Reader, isErr bool) {
		defer wg.Done()
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			if isErr {
				res.stderr = append(res.stderr, line)
			} else {
				res.stdout = append(res.stdout, line)
			}
			mu.Unlock()
		}
	}

	wg.Add(2)
	go scanStream(stdout, false)
	go scanStream(stderr, true)
	wg.Wait()

	err := cmd.Wait()

	r.mu.Lock()
	delete(r.children, node)
	r.mu.Unlock()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

func (r *runner) buildCopyCmd(node string, sources []string, target string, isPull bool) *exec.Cmd {
	cfg := r.cfg
	targetHost := node
	if cfg.user != "" && cfg.user != currentUser() {
		targetHost = cfg.user + "@" + node
	}

	var cmdArgs []string

	if cfg.isRsync {
		// Build ssh command wrapper for rsync -e
		sshArgs := []string{cfg.sshPath, "-o", "BatchMode=yes"}
		if cfg.openSSH && !strings.Contains(cfg.nodeOpts, "-X") {
			sshArgs = append(sshArgs, "-x")
		}
		if cfg.ignoreHostKey {
			sshArgs = append(sshArgs,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "GlobalKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
			)
		}
		cmdArgs = append(cmdArgs, "-e", strings.Join(sshArgs, " "))
		if cfg.preserve {
			cmdArgs = append(cmdArgs, "-p", "-t")
		}
		if cfg.recursive {
			cmdArgs = append(cmdArgs, "-r")
		}
		if cfg.sudo {
			cmdArgs = append(cmdArgs, "--rsync-path=sudo rsync")
		}
		if cfg.nodeOpts != "" {
			cmdArgs = append(cmdArgs, strings.Fields(cfg.nodeOpts)...)
		}

		if isPull {
			dest := filepath.Join(cfg.targetPath, filepath.Base(sources[0])+"._"+node)
			src := targetHost + ":" + sources[0]
			if cfg.recursive {
				src = targetHost + ":" + strings.TrimSuffix(sources[0], "/") + "/"
				dest = filepath.Join(cfg.targetPath, filepath.Base(sources[0])+"._"+node) + "/"
				_ = os.MkdirAll(dest, 0o755)
			}
			cmdArgs = append(cmdArgs, src, dest)
		} else {
			cmdArgs = append(cmdArgs, sources...)
			cmdArgs = append(cmdArgs, targetHost+":"+target)
		}
	} else {
		// scp
		if cfg.openSSH {
			cmdArgs = append(cmdArgs, "-o", "BatchMode=yes")
		}
		if cfg.ignoreHostKey {
			cmdArgs = append(cmdArgs,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "GlobalKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
			)
		}
		if cfg.preserve {
			cmdArgs = append(cmdArgs, "-p")
		}
		if cfg.recursive {
			cmdArgs = append(cmdArgs, "-r")
		}
		if cfg.nodeOpts != "" {
			cmdArgs = append(cmdArgs, strings.Fields(cfg.nodeOpts)...)
		}

		if isPull {
			dest := filepath.Join(cfg.targetPath, filepath.Base(sources[0])+"._"+node)
			src := targetHost + ":" + sources[0]
			cmdArgs = append(cmdArgs, src, dest)
		} else {
			cmdArgs = append(cmdArgs, sources...)
			cmdArgs = append(cmdArgs, targetHost+":"+target)
		}
	}

	return exec.Command(cfg.remoteCopy, cmdArgs...)
}

func resolveRemoteCopy(cliRcp, envRcp string) (path string, isRsync bool, err error) {
	if cliRcp != "" {
		path = cliRcp
	} else if envRcp != "" {
		path = envRcp
	} else {
		if p, err := exec.LookPath("rsync"); err == nil {
			path = p
		} else if _, err := os.Stat("/usr/bin/rsync"); err == nil {
			path = "/usr/bin/rsync"
		} else if p, err := exec.LookPath("scp"); err == nil {
			path = p
		} else {
			path = "/usr/bin/scp"
		}
	}

	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return "", false, fmt.Errorf("remote copy command: %s does not exist or is not executable", path)
	}

	isRsync = strings.Contains(strings.ToLower(filepath.Base(path)), "rsync")
	return path, isRsync, nil
}

func isOpenSSH(sshPath string) bool {
	out, err := exec.Command(sshPath, "-V").CombinedOutput()
	if err != nil {
		return strings.Contains(strings.ToLower(string(out)), "openssh")
	}
	return strings.Contains(strings.ToLower(string(out)), "openssh")
}

func sshDefaultPath() string {
	if p, err := exec.LookPath("ssh"); err == nil {
		return p
	}
	return "/usr/bin/ssh"
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

func showDshConfig(remoteCopy string) {
	fmt.Println("DSH_CONF_DIR: (not used by this standalone port)")
	fmt.Println("Fanout Value: 64")
	fmt.Printf("Remote Copy:  %s\n", remoteCopy)
	fmt.Println("RSH_TYPE: ssh")
}
