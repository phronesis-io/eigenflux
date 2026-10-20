"""Read-only capture bridge: item searches and verified consumption history.

Executed through SSH without installing files or changing production state.
No source text is returned unless the captured context has exclusion terms.
"""
import base64
import hashlib
import json
import os
import sys
import socket
import urllib.request


def fingerprint(item, slots):
    text = (item.get("content") or "") + "\n" + (item.get("summary") or "")
    raw = json.dumps({"ID": int(item["id"]), "Author": int(item.get("author_agent_id") or 0), "Text": text, "Slots": slots}, ensure_ascii=False, separators=(",", ":"))
    for before, after in (("&", "\\u0026"), ("<", "\\u003c"), (">", "\\u003e"), ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
        raw = raw.replace(before, after)
    return hashlib.sha256(raw.encode()).hexdigest(), text



_redis_connection = None

def redis_command(*args):
    global _redis_connection
    if _redis_connection is None:
        host, port = os.environ["REDIS_ADDR"].rsplit(":", 1)
        connection = socket.create_connection((host, int(port)), timeout=10)
        connection.settimeout(40)
        _redis_connection = (connection, connection.makefile("rb"))
        if os.environ.get("REDIS_PASSWORD"):
            redis_command("AUTH", os.environ["REDIS_PASSWORD"])
    connection, reader = _redis_connection
    values = [str(value).encode() for value in args]
    connection.sendall(b"*" + str(len(values)).encode() + b"\r\n" + b"".join(b"$" + str(len(value)).encode() + b"\r\n" + value + b"\r\n" for value in values))
    def read():
        line = reader.readline()
        kind, value = line[:1], line[1:-2]
        if kind == b"*":
            return [read() for _ in range(int(value))]
        if kind == b"$":
            count = int(value)
            if count < 0:
                return None
            data = reader.read(count)
            reader.read(2)
            return data.decode()
        if kind == b":":
            return int(value)
        if kind == b"-":
            raise ValueError("Redis response error")
        if kind != b"+":
            raise ValueError("Invalid Redis response")
        return value.decode()
    return read()


def consumption(request):
    owner = int(request["owner"])
    keys = request["keys"]
    if owner <= 0 or len(keys) != 30 or any(not k.startswith("bf:global:") for k in keys):
        raise ValueError("invalid consumption request")
    history, complete = request.get("groups", []), True
    if request["kind"] == "history":
        raw = redis_command("EVAL_RO", request["history_script"], 1, f"impr:agent:{owner}:groups", 65536)
        if raw is None:
            return {"unavailable": True}
        complete = len(raw) <= 8192
        history = sorted((int(value) for value in raw), reverse=True)[:8192]
    if len(history) > 8192 or any(int(value) <= 0 for value in history):
        raise ValueError("invalid consumption groups")
    found = redis_command("EVAL_RO", request["verification_script"], 30, *keys, *[f"{owner}:{value}" for value in history]) if history else []
    return {"history": history, "confirmed": [int(value.split(":", 1)[1]) for value in found], "complete": complete}

def main():
    for line in sys.stdin:
        try:
            request = json.loads(line)
            if request.get("kind") in ("history", "seen"):
                print(json.dumps(consumption(request)), flush=True)
                continue
            body = request["query"]
            if not (isinstance(body.get("query"), dict) or isinstance(body.get("knn"), dict)) or not 1 <= body.get("size", 0) <= 640:
                raise ValueError("invalid capture query")
            body["_source"] = ["id", "author_agent_id", "content", "summary", "lang", "retrieval_slots", "group_id"]
            req = urllib.request.Request(os.environ["ES_URL"].rstrip("/") + "/items-*/_search", data=json.dumps(body).encode())
            req.add_header("Content-Type", "application/json")
            auth = os.environ.get("ES_USERNAME", "") + ":" + os.environ.get("ES_PASSWORD", "")
            req.add_header("Authorization", "Basic " + base64.b64encode(auth.encode()).decode())
            with urllib.request.urlopen(req, timeout=45) as response:
                data = json.load(response)
            if data.get("timed_out") or data.get("_shards", {}).get("failed", 0):
                raise ValueError("partial search")
            out = []
            for hit in data["hits"]["hits"]:
                source = hit["_source"]
                original = source.get("retrieval_slots") or {}
                slots = {k: original[k] for k in ("provider_region", "lang") if original.get(k)}
                if source.get("lang"):
                    slots["lang"] = [source["lang"]]
                digest, text = fingerprint(source, slots)
                out.append({"id": str(source["id"]), "author": str(source.get("author_agent_id") or 0), "group": source.get("group_id", 0), "score": hit["_score"], "hash": digest, "slots": slots, "text": text if request.get("exclusion_text") else ""})
            print(json.dumps({"hits": out}, ensure_ascii=False), flush=True)
        except Exception as exc:
            print(json.dumps({"error": type(exc).__name__}), flush=True)

if __name__ == "__main__":
    main()
