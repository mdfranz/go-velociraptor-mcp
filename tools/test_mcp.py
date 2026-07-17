"""
End-to-end MCP test harness for raptor-mcp using pydantic-ai.

Usage:
    uv run tools/test_mcp.py [model]

Default model: google:gemini-3.5-flash
Requires: GOOGLE_API_KEY or GEMINI_API_KEY in environment.
Requires: VELOCIRAPTOR_API_CONFIG set or api_client.yaml present.
"""

import asyncio
import logging
import os
import shutil
import sys
import time
from pathlib import Path

from pydantic_ai import Agent
from pydantic_ai.mcp import MCPToolset, StdioTransport

logging.basicConfig(
    level=logging.DEBUG,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
    handlers=[
        logging.FileHandler("raptor_mcp_test.log"),
        logging.StreamHandler(),
    ],
)
for h in logging.root.handlers:
    if type(h) is logging.StreamHandler:
        h.setLevel(logging.INFO)

logger = logging.getLogger("raptor_mcp_test")

REPO_ROOT = Path(__file__).resolve().parent.parent
SERVER_BINARY = "raptor-mcp"

SYSTEM_PROMPT = """
You are a DFIR analyst using the raptor-mcp Velociraptor MCP server.

Available tools follow a strict call order:
- Use list_orgs first if the org is unknown.
- Use clients with a search filter to resolve a hostname to a client_id.
- Use list_artifacts or artifact_details to confirm artifact names and parameters.
- Use collect_artifact to start async collections, then get_collection_results to retrieve them.
- Use realtime_collect for fast blocking collections.
- run_vql executes one read-only SELECT query against the Velociraptor server.

Always provide a detailed final answer including:
1. What you found
2. Which tools you called and in what order
3. Key data from results (include sample rows)
4. Your analysis and conclusions
"""

TASKS = [
    (
        "Org and client discovery",
        """
        Start by listing all organizations in this Velociraptor deployment.
        Then list all connected clients and summarize what endpoints are available,
        including their OS, hostname, agent version, and last seen time.
        """,
    ),
    (
        "Artifact discovery",
        """
        List all artifacts whose name contains 'Linux.Network'.
        For each one, briefly describe what it collects based on its description.
        Then get the full details for Linux.Network.NetstatEnriched and list
        its available parameters.
        """,
    ),
    (
        "Process list collection",
        """
        On the client with hostname 'gitea-lin-pdx', collect Linux.Sys.Pslist
        using realtime_collect.
        Summarize the top 10 processes by RSS memory usage and identify any
        that look interesting from a security perspective.
        """,
    ),
    (
        "Network connection investigation",
        """
        On the client with hostname 'gitea-lin-pdx', collect Linux.Network.NetstatEnriched
        using realtime_collect.
        List all listening ports with their associated process names.
        Identify any ports or processes that seem unusual or worth investigating further.
        """,
    ),
    (
        "VQL: server-side queries",
        """
        Use run_vql to execute the following queries and report the results of each:
        1. List all clients: SELECT client_id, os_info.hostname AS Hostname FROM clients()
        2. List all orgs: SELECT OrgId, Name FROM orgs()
        3. Check server version: SELECT config.version.version AS version FROM scope()
        4. Find the Linux.Sys.Pslist artifact: SELECT name FROM artifact_definitions() WHERE name = 'Linux.Sys.Pslist'
        Summarize what each query returned.
        """,
    ),
    (
        "VQL: flow and source queries on gitea-lin-pdx",
        """
        Use run_vql to do the following on client C.1e91aa095935a4a2:
        1. List the 3 most recent flows: SELECT session_id, artifacts_with_results FROM flows(client_id='C.1e91aa095935a4a2') LIMIT 3
        2. From the most recent flow that has Linux.Sys.Pslist results, fetch processes
           matching sshd using: SELECT Name, Pid FROM source(client_id='C.1e91aa095935a4a2', flow_id='<flow_id>', artifact='Linux.Sys.Pslist') WHERE Name =~ 'sshd'
           substituting the actual flow_id from step 1.
        Report the flows found and the sshd process details.
        """,
    ),
    (
        "VQL: network analysis via raw VQL",
        """
        Use run_vql on client C.1e91aa095935a4a2 to investigate network connections.
        First find a recent Linux.Network.NetstatEnriched flow using:
          SELECT session_id FROM flows(client_id='C.1e91aa095935a4a2') WHERE 'Linux.Network.NetstatEnriched' IN artifacts_with_results LIMIT 1
        Then query its results to list only LISTEN status connections:
          SELECT Laddr, Lport, Status, ProcInfo.Name AS Process FROM source(client_id='C.1e91aa095935a4a2', flow_id='<flow_id>', artifact='Linux.Network.NetstatEnriched') WHERE Status = 'LISTEN'
        Summarize which services are listening and on which ports.
        """,
    ),
]


def resolve_server() -> str | None:
    found = shutil.which(SERVER_BINARY)
    if found:
        return found
    local = REPO_ROOT / SERVER_BINARY
    if local.is_file() and os.access(local, os.X_OK):
        return str(local)
    logger.error(
        f"{SERVER_BINARY} not found in PATH or {local}. "
        "Run `make build-mcp` or `go build -o raptor-mcp ./cmd/raptor-mcp` first."
    )
    return None


def resolve_model(requested: str) -> str:
    if ":" not in requested:
        if requested.startswith("gemini-"):
            return "google:" + requested
    return requested


def _total_tokens(usage) -> int:
    t = getattr(usage, "total_tokens", 0)
    return t() if callable(t) else (t or 0)


async def run(model_name: str) -> None:
    server_path = resolve_server()
    if not server_path:
        return

    if not os.getenv("GOOGLE_API_KEY") and not os.getenv("GEMINI_API_KEY"):
        logger.error("GOOGLE_API_KEY or GEMINI_API_KEY must be set.")
        return

    model = resolve_model(model_name)
    logger.info(f"Model: {model}")
    logger.info(f"Server: {server_path}")

    server_env = dict(os.environ)
    transport = StdioTransport(
        command=server_path,
        args=[],
        env=server_env,
    )

    async with MCPToolset(transport, max_retries=3) as toolset:
        agent = Agent(
            model,
            toolsets=[toolset],
            system_prompt=SYSTEM_PROMPT,
        )

        overall_input = overall_output = 0

        for title, prompt in TASKS:
            logger.info("\n" + "=" * 60)
            logger.info(f"TASK: {title}")
            logger.info("=" * 60)

            start = time.perf_counter()
            try:
                result = await agent.run(prompt)
            except Exception as e:
                logger.exception(f"Task '{title}' failed: {e}")
                continue

            logger.info("\n[Answer]\n" + result.output)

            usage = result.usage
            inp = getattr(usage, "input_tokens", 0) or 0
            out = getattr(usage, "output_tokens", 0) or 0
            tot = _total_tokens(usage)
            overall_input += inp
            overall_output += out
            elapsed = (time.perf_counter() - start) * 1000
            logger.info(
                f"[tokens] input={inp} output={out} total={tot} | elapsed={elapsed:.0f}ms"
            )

        logger.info(
            f"\n[Overall] input={overall_input} output={overall_output} "
            f"total={overall_input + overall_output}"
        )


if __name__ == "__main__":
    default_model = "gemini-3.5-flash"
    model = sys.argv[1] if len(sys.argv) > 1 else default_model
    asyncio.run(run(model))
