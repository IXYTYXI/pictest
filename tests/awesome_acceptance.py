#!/usr/bin/env python3
"""Check the platform image with a retained volume and strict runtime settings."""
import argparse
import base64
import json
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


PNG = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGNQ6SgFAAH1ASIEvfJqAAAAAElFTkSuQmCC")


def docker(*args, check=True):
    return subprocess.run(["docker", *args], check=check, capture_output=True, text=True).stdout.strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", required=True)
    args = parser.parse_args()
    prefix = "upic-awesome-test-" + uuid.uuid4().hex[:12]
    volume = prefix + "-images"
    container = prefix + "-web"
    token = secrets.token_hex(32)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request(base, path, data=None, method=None, authenticated=False):
        headers = {"Authorization": "Bearer " + token} if authenticated else {}
        if data is not None:
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
        with opener.open(req, timeout=5) as response:
            return response.read()

    with tempfile.TemporaryDirectory(prefix=prefix) as temporary:
        env = Path(temporary) / "runtime.env"
        env.write_text("\n".join([
            "UPLOAD_TOKEN=" + token, "PUBLIC_BASE_URL=https://upic.example.com",
            "LISTEN_ADDR=:8080", "DATA_DIR=/data", "MAX_UPLOAD_BYTES=524288",
            "MAX_STORAGE_BYTES=10737418240", "MAX_CONCURRENT_UPLOADS=1", "GOMEMLIMIT=384MiB",
        ]) + "\n")
        env.chmod(0o600)

        def start():
            docker("run", "--detach", "--name", container, "--read-only", "--user", "65532:65532",
                   "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--memory=512m",
                   "--memory-swap=512m", "--pids-limit=128", "--cpus=1",
                   "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777",
                   "--mount", "type=volume,src=" + volume + ",dst=/data",
                   "--env-file", str(env), "--publish", "127.0.0.1::8080", args.image, "serve")
            port = docker("port", container, "8080/tcp").rsplit(":", 1)[1]
            base = "http://127.0.0.1:" + port
            deadline = time.monotonic() + 30
            while True:
                try:
                    assert request(base, "/readyz") == b"ready\n"
                    return base
                except (OSError, urllib.error.URLError):
                    if time.monotonic() >= deadline:
                        raise RuntimeError("platform container did not become ready") from None
                    time.sleep(0.2)

        try:
            docker("volume", "create", volume)
            base = start()
            payload = json.dumps({"file": base64.b64encode(PNG).decode(), "filename": "retained.png"}).encode()
            uploaded = json.loads(request(base, "/api/upload", data=payload, authenticated=True))["data"]
            path = urllib.parse.urlsplit(uploaded["url"]).path
            assert request(base, path) == PNG
            print("PASS platform UID, read-only root, /data volume and upload", flush=True)
            docker("stop", container)
            docker("rm", container)
            base = start()
            assert request(base, path) == PNG
            listed = json.loads(request(base, "/api/images", authenticated=True))["data"]
            assert listed["total"] == 1 and listed["items"][0]["id"] == uploaded["id"]
            print("PASS replacement container retains metadata and original image bytes", flush=True)
            docker("restart", container)
            port = docker("port", container, "8080/tcp").rsplit(":", 1)[1]
            base = "http://127.0.0.1:" + port
            deadline = time.monotonic() + 30
            while True:
                try:
                    assert request(base, path) == PNG
                    break
                except (OSError, urllib.error.URLError):
                    if time.monotonic() >= deadline:
                        raise RuntimeError("image unavailable after restart") from None
                    time.sleep(0.2)
            print("PASS stopped/restarted platform container retains images", flush=True)
            request(base, "/api/images/" + uploaded["id"], method="DELETE", authenticated=True)
        finally:
            docker("rm", "--force", container, check=False)
            docker("volume", "rm", volume, check=False)


if __name__ == "__main__":
    main()
