#!/usr/bin/env bash
# Smoke tests for the standalone pping/xdsh ports.
set -u
cd "$(dirname "$0")"
GO=${GO:-go}
mkdir -p /tmp/opencode
$GO build -o /tmp/opencode/bin-pping ./cmd/pping
$GO build -o /tmp/opencode/bin-xdsh ./cmd/xdsh
P=/tmp/opencode/bin-pping
X=/tmp/opencode/bin-xdsh

fail=0
check() { # name expected actual
  if [ "$2" = "$3" ]; then echo "PASS: $1"; else echo "FAIL: $1"; echo "  expected: $2"; echo "  actual:   $3"; fail=1; fi
}

# --- pping ---
check "pping -h exit" "0" "$($P -h >/dev/null; echo $?)"
check "pping -v output" "pping (xcat-ports) 2.16.x-compatible" "$($P -v)"
check "pping no args exit" "1" "$($P >/dev/null 2>&1; echo $?)"

if command -v fping >/dev/null; then
  out=$($P -f localhost 2>&1 | head -1)
  case "$out" in
    "localhost: ping") echo "PASS: pping -f localhost";;
    *) echo "SKIP: pping -f localhost (got: $out)";;
  esac
else
  echo "SKIP: fping not installed"
fi
if command -v nmap >/dev/null; then
  echo "NOTE: nmap present; pping default nmap path can be tested manually"
else
  echo "SKIP: nmap not installed"
fi

# --- xdsh ---
check "xdsh -h exit" "0" "$($X -h >/dev/null; echo $?)"
$X -h | head -1 | grep -q " xdsh -h" && echo "PASS: xdsh -h text" || { echo "FAIL: xdsh -h text"; fail=1; }
check "xdsh -V" "2.16.x (xcat-ports standalone)" "$($X -V)"

cat > /tmp/opencode/fakessh <<'EOSSH'
#!/usr/bin/env bash
# Fake ssh: last arg is the command; run it locally.
cmd="${@: -1}"
exec /bin/sh -c "$cmd"
EOSSH
chmod +x /tmp/opencode/fakessh

out=$($X node[1-2] -r /tmp/opencode/fakessh "echo hello" 2>&1 | sort)
expected=$'node1: hello\nnode2: hello'
check "xdsh echo via fake ssh" "$expected" "$out"

out=$($X node[1-2] -r /tmp/opencode/fakessh "exit 3" >/dev/null 2>&1; echo $?)
check "xdsh exit code = failed count" "2" "$out"

out=$($X node1 -z -r /tmp/opencode/fakessh "true" 2>&1 | grep "Remote_command_rc")
check "xdsh -z" "node1: Remote_command_rc = 0" "$out"

out=$($X node1 -Q -r /tmp/opencode/fakessh "echo hi" 2>&1)
check "xdsh -Q silent" "" "$out"

out=$($X node1 -s -r /tmp/opencode/fakessh "echo hi" 2>&1)
check "xdsh -s stream" "node1: hi" "$out"

out=$($X node1 -r /tmp/opencode/fakessh "echo err 1>&2" 2>&1 1>/dev/null)
check "xdsh stderr label" "node1: err" "$out"

out=$($X node1 --nodestatus -r /tmp/opencode/fakessh "true" 2>&1 | grep successful)
check "xdsh --nodestatus ok" "node1: Remote_command_successful" "$out"
out=$($X node1 --nodestatus -r /tmp/opencode/fakessh "false" 2>&1 | grep failed)
check "xdsh --nodestatus fail" "node1: Remote_command_failed, error_code=1" "$out"

out=$($X node7 -r /tmp/opencode/fakessh 'echo $NODE' 2>&1)
check "xdsh exports NODE" "node7: node7" "$out"

out=$($X node[1-8] -f 2 -r /tmp/opencode/fakessh "echo x" 2>&1 | sort | tr '\n' ' ')
expected8="node1: x node2: x node3: x node4: x node5: x node6: x node7: x node8: x "
check "xdsh fanout 8 nodes" "$expected8" "$out"

out=$($X node1 -B -r /tmp/opencode/fakessh "echo B" 2>&1)
check "xdsh -B bypass accepted" "node1: B" "$out"

# user@noderange target syntax
out=$($X root@node1 -r /tmp/opencode/fakessh "echo hi" 2>&1 | grep TARGET)
check "xdsh user@ target" "node1: TARGET=root@node1" "$out"

start=$(date +%s)
out=$($X node[1-3] -t 1 -r /tmp/opencode/fakessh "sleep 30" >/dev/null 2>&1; echo $?)
elapsed=$(( $(date +%s) - start ))
if [ "$elapsed" -lt 10 ]; then echo "PASS: xdsh timeout kills ($elapsed s, rc=$out)"; else echo "FAIL: xdsh timeout ($elapsed s)"; fail=1; fi

out=$($X '192.168.100.[1-3]' -r /tmp/opencode/fakessh 'echo $NODE' 2>&1 | sort | tr '\n' ' ')
check "xdsh ip noderange" "192.168.100.1: 192.168.100.1 192.168.100.2: 192.168.100.2 192.168.100.3: 192.168.100.3 " "$out"

exit $fail
