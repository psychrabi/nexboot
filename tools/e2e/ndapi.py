"""Shared client for the end-to-end matrices.

Every check here talks to a real server over HTTP and reads the pool over SSH:
these matrices exist for what unit tests structurally cannot reach — ZFS
refusing an operation, LIO exporting a LUN, a partition node appearing.

Configuration comes from the environment, never from this file:

    ND_BASE       http://<host>:18080     API base URL
    ND_USER       admin                   login user
    ND_PASSWORD                           login password (required)
    ND_SSH_HOST   <host from ND_BASE>     host to read the pool on
    ND_SSH_USER   root
    ND_SSH_PASSWORD                       falls back to ND_PASSWORD
    ND_POOL       tank                    pool name
    ND_IMPORT_DIR /tank/imports           where source images live
"""
import json
import os
import shlex
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

BASE = os.environ.get("ND_BASE", "http://127.0.0.1:18080").rstrip("/")
USER = os.environ.get("ND_USER", "admin")
PASSWORD = os.environ.get("ND_PASSWORD", "")
SSH_HOST = os.environ.get("ND_SSH_HOST") or urllib.parse.urlparse(BASE).hostname
SSH_USER = os.environ.get("ND_SSH_USER", "root")
SSH_PASSWORD = os.environ.get("ND_SSH_PASSWORD") or PASSWORD
POOL = os.environ.get("ND_POOL", "tank")
IMPORT_DIR = os.environ.get("ND_IMPORT_DIR", "/tank/imports")

results = []
skipped = []
_token = None


def login():
    global _token
    if not PASSWORD:
        raise SystemExit("ND_PASSWORD is required")
    req = urllib.request.Request(BASE + "/api/login", method="POST",
                                 data=json.dumps({"username": USER, "password": PASSWORD}).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        _token = json.load(r)["token"]
    return _token


def call(method, path, body=None):
    """One API call; returns (status, parsed body or raw text)."""
    if _token is None:
        login()
    data = json.dumps(body).encode() if body is not None else None
    # IDs may contain non-ASCII names (such as Classroom Café); URL-encode them because urllib requires ASCII request lines.
    req = urllib.request.Request(BASE + urllib.parse.quote(path, safe="/?&=%"), data=data, method=method)
    req.add_header("Authorization", "Bearer " + _token)
    if data:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=90) as r:
            raw = r.read().decode()
            return r.status, (json.loads(raw) if raw.strip().startswith(("{", "[")) else raw.strip())
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode().strip()


def get_bytes(path):
    """A raw GET for endpoints that answer with a file (a bundle archive, an
    xlsx export). call() decodes as text and would choke on the first zip byte.
    """
    if _token is None:
        login()
    req = urllib.request.Request(BASE + urllib.parse.quote(path, safe="/?&=%"))
    req.add_header("Authorization", "Bearer " + _token)
    try:
        with urllib.request.urlopen(req, timeout=90) as r:
            return r.status, r.read(), dict(r.headers)
    except urllib.error.HTTPError as e:
        return e.code, e.read(), dict(e.headers)


def ssh(cmd):
    """Read the server's own state — the pool, configfs, the journal.

    Wrap commands in sudo -S for non-root SSH logins, using the same password. Some deployments prohibit direct root SSH,
    and the harness needs privileges for zfs destroy and mount, not just read-only operations.
    Without sudo, failed cleanup may be silent and leave old data in the backup pool.
    A supposedly full initial backup then becomes incremental, falsely implicating backup logic.
    """
    if SSH_USER != "root":
        cmd = f"echo {shlex.quote(SSH_PASSWORD)} | sudo -S -p '' bash -c {shlex.quote(cmd)}"
    return subprocess.run(
        ["sshpass", "-e", "ssh", "-o", "StrictHostKeyChecking=no",
         "-o", "PubkeyAuthentication=no", f"{SSH_USER}@{SSH_HOST}", cmd],
        capture_output=True, text=True,
        env={"SSHPASS": SSH_PASSWORD, "PATH": os.environ.get("PATH", "")},
    ).stdout.strip()


def configs(image):
    return call("GET", f"/api/images/{image}/configs")[1]["items"]


def reductions(config):
    return call("GET", f"/api/configs/{config}/reductions")[1]["items"]


def cfg_by_name(image, name):
    return next((c for c in configs(image) if c["Name"] == name), None)


def task_of(prefix, timeout=180):
    """Wait for the newest task of this kind to settle.

    It scans a window rather than reading the newest row: an import is followed
    immediately by its health check, so `limit=1` would report on the wrong
    task and call a successful import a failure.
    """
    for _ in range(timeout):
        _, d = call("GET", "/api/tasks/history?limit=10")
        items = d if isinstance(d, list) else d.get("items", [])
        for item in items:
            if item["ID"].startswith(prefix):
                if item["Status"] in ("success", "failed"):
                    return item
                break
        time.sleep(1)
    return {}


def mount_probe(clone, script, attempts=6, delay=3):
    """Mount a clone's Windows partition and run a probe script inside it.

    Retried: a just-created clone's partition nodes lag the clone itself by a
    moment (the same udev lag the import bake has to wait out), and a probe that
    ran too early would report an empty disk as a missing file.
    """
    # Try partitions 1–4: real Windows system partitions may be part3/4, while the mkwin fixture has only part1.
    # Skip EFI/MSR partitions that cannot mount as NTFS.
    for _ in range(attempts):
        out = ssh(
            'D=$(mktemp -d); for p in 1 2 3 4; do '
            f'mount -t ntfs3 -o ro,force /dev/zvol/{clone}-part$p $D 2>/dev/null || continue; '
            f'( cd $D && {script} ); umount $D; done; rmdir $D')
        if out.strip():
            return out
        time.sleep(delay)
    return ""


def check(cid, label, ok, detail=""):
    results.append((cid, label, ok, detail))
    print(f"  {'PASS' if ok else 'FAIL'} {cid} {label}" + (f"  — {detail}" if detail else ""))
    return ok


def skip(cid, label, why):
    """Not run, and not counted as passing. A skipped check that prints PASS is
    the matrix lying about its own coverage."""
    skipped.append((cid, label, why))
    print(f"  SKIP {cid} {label}  — {why}")


def section(title):
    print(f"\n### {title}")


def summary():
    """Print the tally and return an exit code.

    A run that checked nothing is not a pass. Reporting `0/0 passed` with exit 0 suggests
    "everything is fine" to whoever is looking at the pipeline, when what
    actually happened is that the matrix never got far enough to test anything
    — a missing image, an unreachable node, a bad precondition. Being loud here
    is the whole point of the skip/summary split.
    """
    passed = sum(1 for r in results if r[2])
    line = f"\n{passed}/{len(results)} passed"
    if skipped:
        line += f"，{len(skipped)} skipped"
    print(line)
    for cid, label, ok, detail in results:
        if not ok:
            print("  failed:", cid, label, detail)
    for cid, label, why in skipped:
        print("  skipped:", cid, label, why)
    if not results:
        print("  No checks were executed. This is not a pass; review the skipped checks above.")
        return 1
    return 0 if passed == len(results) else 1
