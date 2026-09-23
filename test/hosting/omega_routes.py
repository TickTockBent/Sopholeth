"""Exercise the real Vercel dev router with a temporary site and fake metadata.

Usage: python3 test/hosting/omega_routes.py [vercel command ...]
Defaults to npx --yes vercel@59.7.0. No project link or deployment is made.
"""

import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
NO_STORE = "no-store"
IMMUTABLE = "public, max-age=31536000, immutable"
PAYLOAD = b'{"fixture":"routing only, not signed metadata"}\n'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--metadata-config", type=Path, help="exercise the publisher's metadata-only configuration")
    parser.add_argument("command", nargs=argparse.REMAINDER, help="installed Vercel CLI command")
    args = parser.parse_args()
    command = args.command or ["npx", "--yes", "vercel@59.7.0"]
    with tempfile.TemporaryDirectory(prefix="soph-omega-routes-") as temporary:
        base = Path(temporary)
        site = base / "site"
        shutil.copytree(ROOT / "sites/sopholeth.io", site)
        if args.metadata_config:
            shutil.copyfile(args.metadata_config, site / "vercel.json")
        (site / "omega/targets").mkdir(parents=True)
        files = ["timestamp.json", "1.root.json", "1.snapshot.json", "1.targets.json",
                 "targets/" + "a" * 64 + ".bootstrap.json"]
        blocked = ["1.root.json.pending", "authority.json", ".omega-publish.lock"]
        for name in files + blocked:
            (site / "omega" / name).write_bytes(PAYLOAD)
        config = json.loads((site / "vercel.json").read_text())
        # Vercel dev forces browser Cache-Control to max-age=0. Check that the
        # committed browser directives match the CDN directives exercised below.
        for route in config["routes"]:
            headers = route.get("headers", {})
            if "Vercel-CDN-Cache-Control" in headers:
                assert headers["Cache-Control"] == headers["Vercel-CDN-Cache-Control"]
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        address = f"http://127.0.0.1:{port}"

        def fetch(path):
            try:
                response = urllib.request.urlopen(address + path, timeout=3)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                return response.status, response.headers, response.read()

        with (base / "vercel.log").open("w+") as log:
            process = subprocess.Popen(
                command + ["dev", "--local", "--listen", f"127.0.0.1:{port}", "--cwd", str(site)],
                stdout=log, stderr=log, start_new_session=True,
                env={**os.environ, "VERCEL_TELEMETRY_DISABLED": "1"},
            )
            try:
                deadline = time.monotonic() + 90
                while time.monotonic() < deadline and process.poll() is None:
                    try:
                        if fetch("/omega/timestamp.json")[0] == 200:
                            break
                    except (urllib.error.URLError, TimeoutError):
                        pass
                    time.sleep(0.1)
                else:
                    log.seek(0)
                    raise RuntimeError("Vercel dev did not start:\n" + log.read())
                for name in files:
                    status, headers, body = fetch("/omega/" + name)
                    assert status == 200 and body == PAYLOAD, (name, status, body)
                    expected = NO_STORE if name == "timestamp.json" else IMMUTABLE
                    assert headers.get("Vercel-CDN-Cache-Control") == expected, (name, dict(headers))
                    assert "application/json" in headers.get("Content-Type", ""), name
                missing = ["2.root.json", "2.snapshot.json", "2.targets.json",
                           "targets/" + "b" * 64 + ".bootstrap.json", "targets/"]
                for path in ["/omega", "/omega/"] + ["/omega/" + n for n in blocked + missing]:
                    status, headers, body = fetch(path)
                    assert status == 404, (path, status)
                    assert headers.get("Vercel-CDN-Cache-Control") == NO_STORE, (path, dict(headers))
                    assert json.loads(body) == {"error": "Metadata not found"}, (path, body)
                if args.metadata_config:
                    for path in ["/", "/index.html", "/missing-doc", "/vercel.json"]:
                        status, headers, body = fetch(path)
                        assert status == 404 and headers.get("Vercel-CDN-Cache-Control") == NO_STORE
                        assert json.loads(body) == {"error": "Metadata not found"}
                else:
                    assert fetch("/")[2] == (site / "index.html").read_bytes()
                    status, _, body = fetch("/missing-doc")
                    assert status == 404 and body == (site / "404.html").read_bytes()
                print("PASS: metadata bytes, CDN cache rules, uncached JSON misses, blocked files, and docs routing")
            finally:
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
                except ProcessLookupError:
                    process.wait()


if __name__ == "__main__":
    main()
