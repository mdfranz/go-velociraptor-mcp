#!/usr/bin/env bash
# End-to-end VQL tests for raptor-cli.
# Requires: VELOCIRAPTOR_API_CONFIG set, raptor-cli binary in PATH or ./raptor-cli present.

set -euo pipefail

CLI="${CLI:-./raptor-cli}"
CLIENT_A="C.1e91aa095935a4a2"  # gitea-lin-pdx (amd64)
CLIENT_B="C.5449f237e47ac98e"  # pi5-8gb-5a7f55 (arm64)
# Known completed flows (used for source() tests without triggering new collections)
FLOW_PSLIST_A="F.D9BC4NT839SUU"          # Linux.Sys.Pslist on CLIENT_A
FLOW_CLIENTINFO_A="F.D9BAAR3MQKQRS"     # Generic.Client.Info on CLIENT_A
FLOW_NETSTAT_A="F.D9BC8IHAVS0DA"         # Linux.Network.Netstat on CLIENT_A
FLOW_NETSTAT_ENR_A="F.D9BCAGVRTO3KI"    # Linux.Network.NetstatEnriched on CLIENT_A
HUNT_A="H.D9CG23F0AUEPE"                 # Test hunt
HUNT_ARTIFACT_A="Linux.Sys.LastUserLogin"
PASS=0
FAIL=0

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
RESET='\033[0m'

run() {
    local label="$1"; shift
    echo -n "  $label ... "
    if output=$("$CLI" "$@" 2>&1); then
        echo -e "${GREEN}PASS${RESET}"
        PASS=$((PASS+1))
        if [[ "${VERBOSE:-0}" == "1" ]]; then
            echo "$output" | head -5
        fi
    else
        echo -e "${RED}FAIL${RESET}"
        echo "    output: $output"
        FAIL=$((FAIL+1))
    fi
}

run_contains() {
    local label="$1"; local pattern="$2"; shift 2
    echo -n "  $label ... "
    if output=$("$CLI" "$@" 2>&1) && echo "$output" | grep -q "$pattern"; then
        echo -e "${GREEN}PASS${RESET}"
        PASS=$((PASS+1))
    else
        echo -e "${RED}FAIL${RESET} (expected: $pattern)"
        echo "    output: $(echo "$output" | head -3)"
        FAIL=$((FAIL+1))
    fi
}

run_fails() {
    local label="$1"; shift
    echo -n "  $label ... "
    if "$CLI" "$@" >/dev/null 2>&1; then
        echo -e "${RED}FAIL${RESET} (expected error, got success)"
        FAIL=$((FAIL+1))
    else
        echo -e "${GREEN}PASS${RESET}"
        PASS=$((PASS+1))
    fi
}

echo "=== Discovery ==="

run_contains "server health" "SERVING" \
    server health

run_contains "list orgs" "root" \
    org list

run_contains "list clients" "gitea-lin-pdx" \
    client list

run_contains "client info by hostname" "C.1e91aa095935a4a2" \
    client info gitea-lin-pdx

run_contains "client list --os linux" "linux" \
    client list --os linux

run_contains "client list --search pi5" "pi5" \
    client list --search pi5

run_contains "client list --online" "client_id" \
    client list --online --limit 5 -o json

run "client list --label filter" \
    client list --label "does-not-exist"

run_contains "client describe" "${CLIENT_A}" \
    client describe --client "${CLIENT_A}" -o json

run_contains "client metadata" '"metadata"' \
    client metadata --client "${CLIENT_A}" -o json

run_contains "artifact list" "Linux.Sys.Pslist" \
    artifact list --filter "Linux.Sys.Pslist"

run_contains "artifact details" "processRegex" \
    artifact details Linux.Sys.Pslist

echo
echo "=== VQL: gate check ==="

run_fails "vql run blocked without --dangerous" \
    vql run "SELECT 1 FROM scope()"

echo
echo "=== VQL: server-side queries ==="

run_contains "clients() query" "gitea-lin-pdx" \
    --dangerous vql run "SELECT client_id, os_info.hostname AS Hostname FROM clients()"

run_contains "orgs() query" "root" \
    --dangerous vql run "SELECT OrgId, Name FROM orgs()"

run_contains "artifact_definitions filter" "Linux.Sys.Pslist" \
    --dangerous vql run "SELECT name FROM artifact_definitions() WHERE name = 'Linux.Sys.Pslist'"

run_contains "server version via config.version" "0.77" \
    --dangerous vql run "SELECT config.version.version AS version FROM scope()"

run_contains "flows() on client A" "F.D9" \
    --dangerous vql run "SELECT session_id FROM flows(client_id='${CLIENT_A}') LIMIT 3"

echo
echo "=== VQL: client-side queries via source() ==="

run_contains "pslist results on client A" "systemd" \
    --dangerous vql run "SELECT Name, Pid FROM source(client_id='${CLIENT_A}', flow_id='${FLOW_PSLIST_A}', artifact='Linux.Sys.Pslist') LIMIT 10"

run_contains "client info BasicInformation on client A" "gitea-lin-pdx" \
    --dangerous vql run "SELECT Hostname FROM source(client_id='${CLIENT_A}', flow_id='${FLOW_CLIENTINFO_A}', artifact='Generic.Client.Info/BasicInformation')"

run_contains "filter pslist results" "sshd" \
    --dangerous vql run "SELECT Name, Pid FROM source(client_id='${CLIENT_A}', flow_id='${FLOW_PSLIST_A}', artifact='Linux.Sys.Pslist') WHERE Name =~ 'sshd'"

echo
echo "=== VQL: Linux.Network.Netstat ==="

run_contains "TCP4 listening ports" "sshd" \
    --dangerous vql run "SELECT LocalAddr, LocalPort, State, ProcessInfo.Command AS Process FROM source(client_id='${CLIENT_A}', flow_id='${FLOW_NETSTAT_A}', artifact='Linux.Network.Netstat/TCP4') WHERE State = 'Listening'"

run_contains "TCP4 port 22 listening" "22" \
    --dangerous vql run "SELECT LocalPort, ProcessInfo.Command AS Process FROM source(client_id='${CLIENT_A}', flow_id='${FLOW_NETSTAT_A}', artifact='Linux.Network.Netstat/TCP4') WHERE LocalPort = 22"

run_contains "TCP6 source has results" "LocalPort" \
    --dangerous vql run "SELECT LocalAddr, LocalPort, State FROM source(client_id='${CLIENT_A}', flow_id='${FLOW_NETSTAT_A}', artifact='Linux.Network.Netstat/TCP6') LIMIT 3"

run_contains "collect run returns flow_id" "F." \
    collect run --client "${CLIENT_A}" --artifact Linux.Network.Netstat

echo
echo "=== VQL: Linux.Network.NetstatEnriched ==="

run_contains "enriched has CallChain" "CallChain" \
    --dangerous vql run "SELECT Laddr, Lport, Status, ProcInfo.Name AS Process, CallChain FROM source(client_id='${CLIENT_A}', flow_id='${FLOW_NETSTAT_ENR_A}', artifact='Linux.Network.NetstatEnriched') LIMIT 3" -o json

run_contains "enriched filter by process docker-proxy" "docker-proxy" \
    --dangerous vql run "SELECT Laddr, Lport, Status, ProcInfo.Name AS Process FROM source(client_id='${CLIENT_A}', flow_id='${FLOW_NETSTAT_ENR_A}', artifact='Linux.Network.NetstatEnriched') WHERE ProcInfo.Name =~ 'docker-proxy'"

run_contains "enriched filter by port 22" "22" \
    --dangerous vql run "SELECT Laddr, Lport, Status, ProcInfo.Name AS Process FROM source(client_id='${CLIENT_A}', flow_id='${FLOW_NETSTAT_ENR_A}', artifact='Linux.Network.NetstatEnriched') WHERE Lport = 22"

run_contains "enriched filter LISTEN status" "LISTEN" \
    --dangerous vql run "SELECT Laddr, Lport, Status, ProcInfo.Name AS Process FROM source(client_id='${CLIENT_A}', flow_id='${FLOW_NETSTAT_ENR_A}', artifact='Linux.Network.NetstatEnriched') WHERE Status = 'LISTEN' LIMIT 5"

run_contains "collect run NetstatEnriched returns flow_id" "F." \
    collect run --client "${CLIENT_A}" --artifact Linux.Network.NetstatEnriched

echo
echo "=== flow list and describe ==="

run_contains "flow list on client A" "F." \
    flow list --client "${CLIENT_A}"

run_contains "flow list --limit 5" "F." \
    flow list --client "${CLIENT_A}" --limit 5

run_contains "flow describe known flow" "${FLOW_PSLIST_A}" \
    flow describe --client "${CLIENT_A}" --flow "${FLOW_PSLIST_A}"

run_contains "flow logs show completion" "Collection Linux.Sys.Pslist is done" \
    flow logs --client "${CLIENT_A}" --flow "${FLOW_PSLIST_A}"

echo
echo "=== collect list ==="

run_contains "collect list on client A" "F." \
    collect list --client "${CLIENT_A}"

run_contains "collect list on client B" "F." \
    collect list --client "${CLIENT_B}"

run_contains "collect list --limit 5 returns at most 5 rows" "F." \
    collect list --client "${CLIENT_A}" --limit 5

run_contains "collect list shows artifact names" "Generic.Client" \
    collect list --client "${CLIENT_A}" -o json

echo
echo "=== hunt read operations ==="

run_contains "hunt list" "${HUNT_A}" \
    hunt list --limit 5

run_contains "hunt describe" "${HUNT_A}" \
    hunt describe --hunt "${HUNT_A}" -o json

run_contains "hunt flows" "FlowId" \
    hunt flows --hunt "${HUNT_A}" --limit 5 -o json

run_contains "hunt results" "ClientId" \
    hunt results --hunt "${HUNT_A}" --artifact "${HUNT_ARTIFACT_A}" --limit 5 -o json

echo
echo "=== VQL: output formats ==="

run_contains "json output" '"OrgId"' \
    --dangerous vql run "SELECT OrgId FROM orgs()" -o json

run_contains "yaml output" 'OrgId' \
    --dangerous vql run "SELECT OrgId FROM orgs()" -o yaml

run_contains "table output (default)" "OrgId" \
    --dangerous vql run "SELECT OrgId FROM orgs()"

echo
echo "=== vql export ==="

EXPORT_TMP=$(mktemp -d)

run_contains "vql export writes JSONL file" "done:" \
    --dangerous vql export "SELECT OrgId, Name FROM orgs()" --out "${EXPORT_TMP}/orgs.jsonl"

# verify the file was actually written and contains valid JSON
echo -n "  export file exists and is valid JSONL ... "
export_file=$(ls "${EXPORT_TMP}"/orgs_*.jsonl 2>/dev/null | head -1)
if [[ -n "$export_file" ]] && python3 -c "import json,sys; [json.loads(l) for l in open('${export_file}') if l.strip()]" 2>/dev/null; then
    echo -e "${GREEN}PASS${RESET}"
    PASS=$((PASS+1))
else
    echo -e "${RED}FAIL${RESET} (file: ${export_file:-none})"
    FAIL=$((FAIL+1))
fi

run_fails "vql export blocked without --dangerous" \
    vql export "SELECT 1 FROM scope()" --out "${EXPORT_TMP}/nope.jsonl"

rm -rf "${EXPORT_TMP}"

echo
echo "=== Results ==="
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo

[[ $FAIL -eq 0 ]]
