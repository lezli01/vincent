import json, subprocess, sys, time, threading, os
# usage: acp.py OUT.jsonl CWD MODE PROMPT [extra copilot args...]
# MODE: list (initialize+session/new, wait 15s) | prompt
# PERMREPLY env: allow|reject|cancel  for session/request_permission
out, cwd, mode, prompt = sys.argv[1:5]; extra = sys.argv[5:]
env = dict(os.environ); env.pop("COPILOT_ALLOW_ALL", None)
p = subprocess.Popen(["copilot", "--acp", "--no-auto-update", *extra], cwd=cwd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, text=True, bufsize=1)
t0 = time.time(); log = open(out, "w"); lock = threading.Lock(); pending = {}; nid = [0]
def rec(direction, msg):
    with lock:
        log.write(json.dumps({"t": round(time.time()-t0, 3), "dir": direction, "msg": msg}) + "\n"); log.flush()
def send(msg):
    rec("out", msg); p.stdin.write(json.dumps(msg) + "\n"); p.stdin.flush()
def call(method, params):
    nid[0] += 1; ev = threading.Event(); pending[nid[0]] = [ev, None]
    send({"jsonrpc": "2.0", "id": nid[0], "method": method, "params": params}); return nid[0], ev
def reader():
    for line in p.stdout:
        try: m = json.loads(line)
        except Exception: rec("raw", line); continue
        rec("in", m)
        if "id" in m and "method" not in m and m["id"] in pending:
            pending[m["id"]][1] = m; pending[m["id"]][0].set()
        elif m.get("method") == "session/request_permission":
            r = os.environ.get("PERMREPLY", "reject")
            opts = m["params"].get("options", [])
            if r == "cancel": res = {"outcome": {"outcome": "cancelled"}}
            else:
                want = "allow_once" if r == "allow" else "reject_once"
                o = next((o for o in opts if o.get("kind") == want), opts[0] if opts else {"optionId": "x"})
                res = {"outcome": {"outcome": "selected", "optionId": o["optionId"]}}
            send({"jsonrpc": "2.0", "id": m["id"], "result": res})
        elif "id" in m and "method" in m:
            send({"jsonrpc": "2.0", "id": m["id"], "error": {"code": -32601, "message": "not implemented by probe"}})
threading.Thread(target=reader, daemon=True).start()
threading.Thread(target=lambda: [rec("stderr", l) for l in p.stderr], daemon=True).start()
i, ev = call("initialize", {"protocolVersion": 1, "clientCapabilities": {"fs": {"readTextFile": False, "writeTextFile": False}, "terminal": False}, "clientInfo": {"name": "vincent-task-141-probe", "version": "0"}})
ev.wait(60)
mcp = json.loads(os.environ.get("MCPSERVERS", "[]"))
if os.environ.get("LOADSESSION"):
    i, ev = call("session/load", {"sessionId": os.environ["LOADSESSION"], "cwd": cwd, "mcpServers": mcp})
    ev.wait(60); sid = os.environ["LOADSESSION"]
else:
    i, ev = call("session/new", {"cwd": cwd, "mcpServers": mcp}); ev.wait(60)
    sid = (pending[i][1] or {}).get("result", {}).get("sessionId")
if mode == "list":
    time.sleep(float(os.environ.get("WAIT", "15")))
else:
    i, ev = call("session/prompt", {"sessionId": sid, "prompt": [{"type": "text", "text": prompt}]}); ev.wait(300)
    time.sleep(2)
rec("note", {"sessionId": sid})
p.stdin.close()
try: p.wait(10)
except subprocess.TimeoutExpired: p.terminate(); rec("note", "terminated")
rec("note", {"exit": p.returncode})
