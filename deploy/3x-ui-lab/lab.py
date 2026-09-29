#!/usr/bin/env python3
"""Bootstrap and verify an isolated, disposable 3x-ui SHADOW laboratory.

This controller is Linux/root-only. It never discovers or opens production
containers, credentials, databases, or configurations. All mutations are scoped
to the new Compose project recorded in this directory's artifacts/state.json.
"""

import argparse
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parent
STATE = ROOT / "artifacts/state.json"
PROJECT_PATTERN = r"chiral-3x-ui-lab-[a-z0-9][a-z0-9-]{0,35}"
VOLUMES = {"core-data", "agent-data", "three-x-ui-data", "three-x-ui-bin", "three-x-ui-cert"}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def save(path, content):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    if not isinstance(content, bytes):
        content = content.encode()
    path.write_bytes(content)
    path.chmod(0o600)


def save_json(path, value):
    save(path, json.dumps(value, indent=2) + "\n")


def run(args, data=None, timeout=60, env=None):
    result = subprocess.run(args, input=data, capture_output=True, timeout=timeout, env=env)
    # Command arguments, stderr, and stdout may contain bootstrap secrets.
    # Callers retain only explicitly selected, non-secret evidence.
    require(result.returncode == 0, f"{args[0]} failed with exit code {result.returncode}")
    return result.stdout


def require_fresh_project(project):
    containers = run(["docker", "container", "ls", "--all", "--filter",
                      "label=com.docker.compose.project=" + project, "--format", "{{.Names}}"])
    require(not containers.strip(), "Project already owns Docker containers")
    # Compose may reuse an unlabelled volume with the generated project name.
    # Reject name collisions as well as resources carrying our project label.
    existing = set(run(["docker", "volume", "ls", "--format", "{{.Name}}"])
                   .decode().splitlines())
    require(not existing.intersection(project + "_" + name for name in VOLUMES),
            "A laboratory volume name already exists")
    labelled = run(["docker", "volume", "ls", "--filter",
                    "label=com.docker.compose.project=" + project, "--format", "{{.Name}}"])
    require(not labelled.strip(), "Project already owns Docker volumes")


def validate_interfaces(interfaces):
    require(any(i["ifname"] == "lo" and "UP" in i.get("flags", []) for i in interfaces),
            "Laboratory loopback is not up")
    for interface in interfaces:
        if interface["ifname"] == "lo":
            continue
        # Linux can create the inactive fallback SIT tunnel in every namespace.
        # With no underlay interface, address, route, or NET_ADMIN capability it
        # cannot carry traffic; any other non-loopback interface is rejected.
        require(interface["ifname"] == "sit0" and interface.get("link_type") == "sit"
                and interface.get("operstate") == "DOWN" and "UP" not in interface.get("flags", [])
                and not interface.get("addr_info"), "Laboratory has a usable non-loopback interface")


class Lab:
    def __init__(self, state):
        self.state = state
        self.project = state["project"]
        require(re.fullmatch(PROJECT_PATTERN, self.project), "Invalid laboratory project name")
        self.base = ["docker", "compose", "--project-directory", str(ROOT),
                     "--env-file", str(ROOT / ".env"), "--project-name", self.project,
                     "--file", str(ROOT / "docker-compose.yml")]

    def compose(self, *args, data=None, timeout=60):
        env = {k: v for k, v in os.environ.items() if not k.startswith("CHIRAL_LAB_")}
        return run(self.base + list(args), data=data, timeout=timeout, env=env)

    def persist(self):
        save_json(STATE, self.state)

    def validate_compose(self):
        config = json.loads(self.compose("config", "--format", "json"))
        require(config["name"] == self.project, "Unexpected Compose project")
        require(set(config["services"]) == {"3x-ui", "core", "agent"}, "Unexpected services")
        allowed_volumes = VOLUMES
        require(set(config["volumes"]) == allowed_volumes, "Unexpected volumes")
        for name, volume in config["volumes"].items():
            require(not volume.get("external"), "External volumes are forbidden")
            require(volume.get("name") == self.project + "_" + name, "Volume is not project-scoped")
        for name, service in config["services"].items():
            expected = "none" if name == "3x-ui" else "service:3x-ui"
            require(service.get("network_mode") == expected, "Invalid laboratory network mode")
            for key in ["ports", "networks", "devices", "cap_add", "privileged", "pid", "ipc", "volumes_from"]:
                require(not service.get(key), "Forbidden service option: " + key)
            require(service.get("cap_drop") == ["ALL"], "All capabilities must be dropped")
            require("no-new-privileges:true" in service.get("security_opt", []), "Missing privilege guard")
            require(0 < int(service.get("mem_limit", 0)) <= 384 * 1024 * 1024, "Missing memory limit")
            require(0 < float(service.get("cpus", 0)) <= 0.5, "Missing CPU limit")
            require(service.get("pids_limit") == 128, "Missing process limit")
            for mount in service.get("volumes", []):
                if mount["type"] == "volume":
                    require(mount["source"] in allowed_volumes, "Foreign volume")
                else:
                    require(name == "agent" and mount["type"] == "bind", "Unexpected host mount")
                    require(Path(mount["source"]).resolve() == ROOT / "secrets/3x-ui.token", "Foreign token mount")
                    require(mount.get("read_only") and not mount.get("bind", {}).get("create_host_path", False), "Unsafe token mount")

    def inspect(self, service):
        container = self.compose("ps", "--all", "--quiet", service).decode().strip()
        require(bool(container) and "\n" not in container, "Expected one laboratory container")
        info = json.loads(run(["docker", "inspect", container]))[0]
        labels = info["Config"]["Labels"]
        require(labels.get("com.docker.compose.project") == self.project, "Foreign container")
        require(labels.get("com.docker.compose.service") == service, "Unexpected service identity")
        require(labels.get("io.chiral.lab") == "3x-ui-shadow", "Missing laboratory label")
        return info

    def pid(self):
        info = self.inspect("3x-ui")
        require(info["State"]["Running"], "Laboratory 3x-ui is not running")
        return info["State"]["Pid"]

    def request(self, path, method="GET", payload=None, provider="core"):
        require(path.startswith("/") and "\n" not in path, "Invalid API path")
        if provider == "core":
            origin = "http://127.0.0.1:26080"
            token = self.state["core_token"]
        else:
            require(provider == "3x-ui", "Unknown provider")
            origin = "http://127.0.0.1:2053/" + self.state["base_path"]
            token = (ROOT / "secrets/bootstrap.token").read_text().strip()
        opts = "url = " + json.dumps(origin + path) + "\n"
        opts += "request = " + json.dumps(method) + "\n"
        opts += "header = " + json.dumps("Authorization: Bearer " + token) + "\n"
        if payload is not None:
            opts += 'header = "Content-Type: application/json"\n'
            opts += "data = " + json.dumps(json.dumps(payload)) + "\n"
        output = run(["nsenter", "-t", str(self.pid()), "-n", "curl", "--noproxy", "*",
                      "--silent", "--show-error", "--max-time", "8", "--config", "-",
                      "--write-out", "\n%{http_code}"], data=opts.encode(), timeout=15)
        body, status = output.rsplit(b"\n", 1)
        require(200 <= int(status) < 300, f"Laboratory API {path} returned HTTP {status.decode()}")
        if path == "/healthz":
            require(body == b"ok", "Invalid Core health response")
            return "ok"
        result = json.loads(body) if body else None
        if provider == "3x-ui":
            require(isinstance(result, dict) and result.get("success") is True,
                    "3x-ui rejected laboratory operation: " + path)
            return result.get("obj")
        return result

    def wait(self, check, description, timeout=50):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                value = check()
                if value:
                    return value
            except (RuntimeError, subprocess.TimeoutExpired):
                pass
            time.sleep(1)
        raise RuntimeError("Timed out waiting for " + description)

    def node(self):
        result = self.request("/api/nodes")
        nodes = result if isinstance(result, list) else result["nodes"]
        return next((n for n in nodes if n["id"] == self.state["node_id"]), None)

    def ready(self):
        n = self.node()
        return n if n and n.get("online") and n.get("runtime_mode") == "SHADOW" and n.get("runtime_health") == "READY" else None

    def verify_isolation(self):
        self.validate_compose()
        netns = None
        identities = {}
        for name in ["3x-ui", "core", "agent"]:
            info = self.inspect(name)
            require(info["State"]["Running"] and not info["State"].get("OOMKilled"), "Unhealthy laboratory container")
            require(not info["HostConfig"].get("PortBindings"), "Published host ports are forbidden")
            require(info["HostConfig"]["CapDrop"] == ["ALL"], "Unexpected effective capabilities")
            require(not info["HostConfig"].get("Privileged"), "Privileged laboratory container")
            expected_mode = "none" if name == "3x-ui" else "container:" + identities["3x-ui"]["id"]
            require(info["HostConfig"]["NetworkMode"] == expected_mode, "Unexpected network namespace owner")
            current = os.readlink(f'/proc/{info["State"]["Pid"]}/ns/net')
            require(current != os.readlink("/proc/1/ns/net"), "Laboratory is using host networking")
            require(netns is None or netns == current, "Laboratory services do not share the isolated namespace")
            netns = current
            for mount in info["Mounts"]:
                if mount["Type"] == "volume":
                    require(mount["Name"].startswith(self.project + "_"), "Foreign mounted volume")
                else:
                    require(name == "agent" and Path(mount["Source"]).resolve() == ROOT / "secrets/3x-ui.token"
                            and not mount["RW"], "Foreign mounted host directory")
            identities[name] = {"id": info["Id"], "image": info["Image"], "started_at": info["State"]["StartedAt"]}
        prefix = ["nsenter", "-t", str(self.pid()), "-n", "ip", "-j"]
        validate_interfaces(json.loads(run(prefix + ["address", "show"])))
        for family in ["-4", "-6"]:
            routes = json.loads(run(prefix + [family, "route", "show", "table", "main"]))
            require(not routes, "Laboratory has a non-local route")
        return identities

    def verify(self):
        identities = self.verify_isolation()
        self.request("/healthz")
        node = self.wait(self.ready, "SHADOW READY")
        require(node.get("runtime_provider") == "3x-ui", "Wrong runtime provider")
        require(len(node.get("runtime_capabilities", [])) == 16, "Incomplete runtime capabilities")
        config_before = self.request("/panel/api/server/getConfigJson", provider="3x-ui")
        test_binary = ROOT / "bin/threexui-contract.test"
        require(test_binary.is_file(), "Build bin/threexui-contract.test before verification")
        env = {**os.environ,
               "CHIRAL_3XUI_TEST_URL": "http://127.0.0.1:2053/" + self.state["base_path"] + "/",
               "CHIRAL_3XUI_TEST_TOKEN_FILE": str(ROOT / "secrets/3x-ui.token")}
        output = run(["nsenter", "-t", str(self.pid()), "-n", str(test_binary),
                      "-test.run", "^TestLive3XUIReadOnlyContract$", "-test.v", "-test.count=1"], env=env)
        require(b"--- PASS: TestLive3XUIReadOnlyContract" in output and b"SKIP" not in output,
                "Live contract test did not positively pass")
        save(ROOT / "artifacts/contract-test.log", output)
        print(output.decode(), flush=True)
        token_id = self.state["observer_token_id"]
        # Only the test harness writes the disposable token state. The Agent
        # observer continues to use exclusively read-only API requests.
        endpoint = f"/panel/api/setting/apiTokens/setEnabled/{token_id}"
        try:
            self.request(endpoint, "POST", {"enabled": False, "expectedScope": "admin"}, "3x-ui")
            def denied():
                n = self.node()
                return n if n and n.get("online") and n.get("runtime_mode") == "SHADOW" and n.get("runtime_health") == "INCOMPATIBLE" else None
            self.wait(denied, "explicit failure after observer token revocation")
            print("Observer token revocation: SHADOW / INCOMPATIBLE", flush=True)
        finally:
            self.request(endpoint, "POST", {"enabled": True, "expectedScope": "admin"}, "3x-ui")
        node = self.wait(self.ready, "SHADOW recovery after token restoration")
        config_after = self.request("/panel/api/server/getConfigJson", provider="3x-ui")
        require(config_before == config_after, "Observer changed 3x-ui configuration")
        self.verify_isolation()
        report = {"project": self.project, "services": identities, "network": "isolated-loopback-only",
                  "runtime": {k: v for k, v in node.items() if k.startswith("runtime_")},
                  "live_contract_test": "PASS", "revocation_and_recovery": "PASS",
                  "provider_config_unchanged": True, "last_verified_at": int(time.time())}
        save_json(ROOT / "artifacts/verification.json", report)
        print(json.dumps(report, indent=2), flush=True)


def initialize(args):
    require(re.fullmatch(PROJECT_PATTERN, args.project), "Use a unique chiral-3x-ui-lab-* project name")
    require(not STATE.exists() and not (ROOT / ".env").exists(), "This directory is already initialized")
    for image in [args.three_x_ui_image, args.core_base_image, args.agent_base_image]:
        require(re.fullmatch(r"[A-Za-z0-9./_-]+@sha256:[0-9a-f]{64}", image), "Image must be an explicit repository digest")
        run(["docker", "image", "inspect", image])
    require_fresh_project(args.project)
    for binary in ["chiral-core", "chiral-agent", "threexui-contract.test"]:
        require((ROOT / "bin" / binary).is_file(), "Missing cross-compiled binary: " + binary)
    state = {"project": args.project, "base_path": secrets.token_hex(16),
             "core_token": secrets.token_hex(32), "created_at": int(time.time())}
    lab = Lab(state)
    for directory in ["secrets", "artifacts"]:
        (ROOT / directory).mkdir(mode=0o700, exist_ok=True)
        (ROOT / directory).chmod(0o700)
    values = {"CHIRAL_LAB_3XUI_IMAGE": args.three_x_ui_image,
              "CHIRAL_LAB_CORE_BASE_IMAGE": args.core_base_image,
              "CHIRAL_LAB_AGENT_BASE_IMAGE": args.agent_base_image,
              "CHIRAL_LAB_CORE_IMAGE": args.project + "-core:local",
              "CHIRAL_LAB_AGENT_IMAGE": args.project + "-agent:local",
              "CHIRAL_LAB_3XUI_BASE_PATH": state["base_path"]}
    save(ROOT / ".env", "".join(k + "=" + v + "\n" for k, v in values.items()))
    save(ROOT / "secrets/core.env", "CHIRAL_ADMIN_TOKEN=" + state["core_token"] + "\n"
         + "CHIRAL_ADMIN_PASSWORD=" + secrets.token_hex(32) + "\n"
         + "CHIRAL_SECRET_KEY=" + secrets.token_hex(32) + "\n")
    save(ROOT / "secrets/agent.env", "")
    save(ROOT / "secrets/3x-ui.token", "")
    lab.persist()
    lab.validate_compose()
    print("Building isolated runtime images from local binaries", flush=True)
    lab.compose("build", "--pull=false", "core", "agent", timeout=120)
    password = secrets.token_hex(32)
    save(ROOT / "secrets/3x-ui-admin-password", password + "\n")
    script = ('IFS= read -r lab_password; exec /app/x-ui setting -username chiral-lab '
              '-password "$lab_password" -port 2053 -listenIP 127.0.0.1 '
              '-webBasePath /' + state["base_path"] + '/ -tokenName chiral-lab-bootstrap -getApiToken')
    bootstrap = lab.compose("run", "--rm", "--no-deps", "-T", "--entrypoint", "/bin/sh", "3x-ui",
                            "-c", script, data=(password + "\n").encode())
    match = re.search(rb"apiToken:\s*([A-Za-z0-9_-]+)", bootstrap)
    require(match is not None, "Upstream CLI did not return a bootstrap token")
    save(ROOT / "secrets/bootstrap.token", match.group(1) + b"\n")
    lab.compose("up", "-d", "--no-build", "--pull", "never", "3x-ui", "core")
    lab.wait(lambda: lab.request("/healthz"), "laboratory Core")
    lab.wait(lambda: lab.request("/panel/api/server/status", provider="3x-ui"), "laboratory 3x-ui")
    settings = lab.request("/panel/api/setting/all", "POST", {}, "3x-ui")
    settings.update(subEnable=False, subJsonEnable=False, subClashEnable=False)
    lab.request("/panel/api/setting/update", "POST", settings, "3x-ui")
    saved = lab.request("/panel/api/setting/all", "POST", {}, "3x-ui")
    require(all(saved.get(k) is False for k in ["subEnable", "subJsonEnable", "subClashEnable"]), "3x-ui subscriptions were not disabled")
    token = lab.request("/panel/api/setting/apiTokens/create", "POST",
                        {"name": "chiral-lab-observer", "scope": "admin", "expiresAt": 0}, "3x-ui")
    require(token.get("token") and token.get("scope") == "admin", "Missing observer token")
    save(ROOT / "secrets/3x-ui.token", token["token"] + "\n")
    state["observer_token_id"] = token["id"]
    node = lab.request("/api/nodes", "POST", {"name": "isolated-3x-ui-contract"})
    state["node_id"] = node["node"]["id"]
    save(ROOT / "secrets/agent.env", "JOIN_TOKEN=" + node["join_token"] + "\n")
    lab.persist()
    lab.compose("up", "-d", "--no-build", "--pull", "never", "agent")
    lab.wait(lab.ready, "initial SHADOW READY")
    applied = lab.request("/api/nodes/" + state["node_id"] + "/config/apply", "POST")
    state["direct_config_version"] = applied["version"]
    def acknowledged():
        versions = lab.request("/api/nodes/" + state["node_id"] + "/config/versions")["versions"]
        return any(v["version"] == applied["version"] and v["applied"] == 1 for v in versions)
    lab.wait(acknowledged, "direct-Xray configuration acknowledgement")
    state["initialized"] = True
    lab.persist()
    print("Isolated Core and Agent ready; direct-Xray configuration acknowledged", flush=True)
    lab.verify()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    init = sub.add_parser("init", help="Create a fresh isolated laboratory from existing local images")
    init.add_argument("--project", required=True)
    init.add_argument("--three-x-ui-image", required=True)
    init.add_argument("--core-base-image", required=True)
    init.add_argument("--agent-base-image", required=True)
    sub.add_parser("verify", help="Run read-only contract and disposable-token fault checks")
    sub.add_parser("status", help="Read laboratory state without changing it")
    sub.add_parser("stop", help="Stop only this laboratory, preserving all its volumes")
    args = parser.parse_args()
    require(sys.platform == "linux" and os.geteuid() == 0, "Run on the Linux Docker host as root")
    os.umask(0o077)
    if args.command == "init":
        initialize(args)
        return
    require(STATE.is_file(), "Laboratory has not been initialized")
    lab = Lab(json.loads(STATE.read_text()))
    lab.validate_compose()
    if args.command == "verify":
        lab.verify()
    elif args.command == "status":
        print(json.dumps({"project": lab.project, "isolation": lab.verify_isolation(), "node": lab.node()}, indent=2))
    elif args.command == "stop":
        lab.compose("stop", "--timeout", "10")
        print("Stopped " + lab.project + "; all laboratory data retained")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.TimeoutExpired) as exc:
        print("Laboratory verification failed: " + str(exc), file=sys.stderr)
        sys.exit(1)
