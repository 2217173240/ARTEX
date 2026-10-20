import json
import os
import sys

keys = ["ARTEX_PG_DSN", "ANTHROPIC_API_KEY", "ARTEX_LLM_BASE_URL", "ARTEX_LLM_PROXY"]
ok = all(not os.getenv(key) for key in keys)
ok = ok and os.getenv("OPENAI_API_KEY", "") == sys.argv[1]
ok = ok and os.getenv("ARTEX_LLM_CALL_TIMEOUT") == "37"
ok = ok and os.getenv("REQUESTS_CA_BUNDLE") == "inherited-cert.pem"
ok = ok and os.getenv("TOOL_ENV_PRESERVED") == "runtime-canary"
ok = ok and bool(os.getenv("PATH")) and bool(os.getenv("HOME"))

for line in sys.stdin:
    req = json.loads(line)
    if "id" not in req:
        continue
    if not ok:
        reply = {
            "jsonrpc": "2.0", "id": req["id"],
            "error": {"code": -32000, "message": "environment check failed"},
        }
    else:
        if req["method"] == "initialize":
            result = {
                "protocolVersion": "2024-11-05", "capabilities": {"tools": {}},
                "serverInfo": {"name": "env-local", "version": "1"},
            }
        elif req["method"] == "tools/list":
            result = {"tools": [{"name": "environment_ok", "inputSchema": {"type": "object"}}]}
        else:
            raise ValueError("Unexpected protocol method: " + req["method"])
        reply = {"jsonrpc": "2.0", "id": req["id"], "result": result}
    print(json.dumps(reply), flush=True)
