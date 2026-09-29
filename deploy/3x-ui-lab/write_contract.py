#!/usr/bin/env python3
"""Run one official-HTTP write contract against a new, loopback-only Docker lab.

Requires an already pulled Linux/amd64 image digest and cross-compiled test
binary. No image pull, existing-container adoption, host ports, volume deletion,
or production discovery is performed. Containers and volumes remain for review.
"""

import argparse
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import stat
import subprocess
import sys
import time


LABEL = "io.chiral.write-contract"
ROLE = "io.chiral.write-contract.role"
IMAGE_RE = r"[A-Za-z0-9][A-Za-z0-9._:/-]*@sha256:[0-9a-f]{64}"
ID_RE = r"[0-9a-f]{64}"
TEST_NAME = "TestLive3XUIWriteContract"
LIMIT = 384 * 1024 * 1024
VOLUME_TARGETS = {"data": "/etc/x-ui", "bin": "/app/bin", "cert": "/root/cert"}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def private_write(path, data):
    if isinstance(data, str):
        data = data.encode()
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC
    flags |= getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags, 0o600)
    with os.fdopen(fd, "wb") as output:
        os.fchmod(output.fileno(), 0o600)
        output.write(data)


def execute(args, data=None, timeout=60):
    # Do not let inherited context/TLS settings override the explicitly pinned
    # local Unix socket. No shell evaluates host command arguments.
    env = {key: value for key, value in os.environ.items()
           if key not in {"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"}}
    return subprocess.run(args, input=data, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                          timeout=timeout, env=env)


def local_docker_endpoint():
    explicit = os.environ.get("DOCKER_HOST")
    if explicit:
        require(explicit.startswith("unix:///"), "Only a local Docker Unix socket is permitted")
        return explicit
    args = ["docker", "context", "inspect"]
    if os.environ.get("DOCKER_CONTEXT"):
        args.append(os.environ["DOCKER_CONTEXT"])
    result = execute(args)
    require(result.returncode == 0, "Cannot inspect the selected Docker context")
    try:
        endpoint = json.loads(result.stdout)[0]["Endpoints"]["docker"]["Host"]
    except (ValueError, KeyError, IndexError, TypeError):
        raise RuntimeError("Docker context did not provide a local endpoint") from None
    require(isinstance(endpoint, str) and endpoint.startswith("unix:///"),
            "Remote Docker contexts are forbidden")
    return endpoint


def redact(text, sensitive):
    text = text.decode(errors="replace") if isinstance(text, bytes) else str(text)
    for secret in sorted(filter(None, sensitive), key=len, reverse=True):
        text = text.replace(secret, "[REDACTED]")
    text = re.sub(r"(?i)(bearer\s+)[A-Za-z0-9._~+/-]+", r"\1[REDACTED]", text)
    text = re.sub(r"(?i)(\b(?:apiToken|password|privateKey|token)\b[\s\"':=]+)[A-Za-z0-9._~+/-]+",
                  r"\1[REDACTED]", text)
    return text


class WriteContract:
    def __init__(self, image, binary, output):
        require(re.fullmatch(IMAGE_RE, image), "Image must be an explicit repository@sha256 digest")
        supplied = Path(binary).expanduser()
        require(supplied.is_absolute() and not supplied.is_symlink(), "Binary must be an absolute, non-symlink path")
        self.binary = supplied.resolve(strict=True)
        require(self.binary.is_file() and self.binary.stat().st_mode & stat.S_IXUSR,
                "Test binary must be an executable regular file")
        with self.binary.open("rb") as source:
            header = source.read(20)
        require(len(header) == 20 and header[:6] == b"\x7fELF\x02\x01" and header[18:20] == b"\x3e\x00",
                "Test binary must be a Linux/amd64 ELF executable")
        self.output = Path(output).expanduser()
        require(self.output.is_absolute() and not self.output.exists() and not self.output.is_symlink(),
                "Output must be a new dedicated absolute directory")
        require(self.output.parent.is_dir() and self.output.parent.resolve() == self.output.parent,
                "Output parent must already exist without symlink aliases")
        require("," not in str(self.binary) and "," not in str(self.output), "Mount paths cannot contain commas")
        self.output.mkdir(mode=0o700)
        self.output.chmod(0o700)
        self.image = image
        self.run_id = "chiral-write-" + secrets.token_hex(12)
        self.names = {role: self.run_id + "-" + role for role in ("bootstrap", "app", "test")}
        self.volumes = {role: self.run_id + "-" + role for role in (*VOLUME_TARGETS, "test-data")}
        self.ids = {}
        self.launches = {}
        self.intended = set()
        self.password = secrets.token_hex(32)
        self.token = ""
        self.base_path = secrets.token_hex(16)
        self.endpoint = None
        self.docker_desktop = False
        self.image_id = None
        self.state = {"run_id": self.run_id, "image": image, "output": str(self.output),
                      "containers": {}, "volumes": self.volumes, "result": "NOT_RUN",
                      "test_exit_code": None, "cleanup": {}, "retained": True}

    def save_state(self):
        private_write(self.output / "state.json", json.dumps(self.state, indent=2) + "\n")

    def docker(self, args, data=None, timeout=60, check=True):
        require(self.endpoint is not None, "Docker endpoint is not pinned")
        try:
            result = execute(["docker", "--host", self.endpoint] + args, data=data, timeout=timeout)
        except subprocess.TimeoutExpired:
            raise RuntimeError("Docker operation exceeded its bounded timeout") from None
        if check and result.returncode != 0:
            raise RuntimeError("Docker operation failed with exit code " + str(result.returncode))
        return result

    def inspect(self, target, kind="container"):
        # Containerd-backed image stores can otherwise return the multi-arch
        # index descriptor, which has no OS/architecture or runnable image ID.
        platform = ["--platform", "linux/amd64"] if kind == "image" else []
        result = self.docker([kind, "inspect"] + platform + [target])
        try:
            values = json.loads(result.stdout)
            require(isinstance(values, list) and len(values) == 1, "Unexpected Docker inspect result")
            return values[0]
        except (ValueError, TypeError):
            raise RuntimeError("Invalid Docker inspect result") from None

    def preflight(self):
        self.endpoint = local_docker_endpoint()
        try:
            operating_system = json.loads(self.docker(["info", "--format", "{{json .OperatingSystem}}"]).stdout)
        except (ValueError, TypeError):
            raise RuntimeError("Cannot establish Docker daemon operating system") from None
        require(isinstance(operating_system, str), "Invalid Docker daemon operating system")
        self.docker_desktop = operating_system == "Docker Desktop"
        self.state["docker_desktop"] = self.docker_desktop
        image = self.inspect(self.image, "image")
        require(image.get("Os") == "linux" and image.get("Architecture") == "amd64", "Wrong image platform")
        require(self.image in image.get("RepoDigests", []), "Requested digest is not present locally")
        require(re.fullmatch(r"sha256:" + ID_RE, image.get("Id", "")), "Invalid immutable image ID")
        self.image_id = image["Id"]
        declared = set((image.get("Config") or {}).get("Volumes") or {})
        require(declared <= {"/etc/x-ui"}, "Unexpected implicit image volumes")
        for kind, names, fmt in (("container", self.names.values(), "{{.Names}}"),
                                 ("volume", self.volumes.values(), "{{.Name}}")):
            args = [kind, "ls"] + (["--all"] if kind == "container" else []) + ["--format", fmt]
            existing = set(self.docker(args).stdout.decode().splitlines())
            require(not existing.intersection(names), "A generated resource name already exists")
            owned = self.docker([kind, "ls"] + (["--all"] if kind == "container" else []) +
                                ["--filter", "label=" + LABEL + "=" + self.run_id, "--format", fmt])
            require(not owned.stdout.strip(), "Resources already carry this run's ownership label")
        self.state["image_id"] = self.image_id
        self.save_state()

    def create_volumes(self):
        for role, name in self.volumes.items():
            self.docker(["volume", "create", "--driver", "local", "--label", LABEL + "=" + self.run_id,
                         "--label", ROLE + "=" + role, name])
            self.verify_volume(role)
        self.save_state()

    def verify_volume(self, role):
        info = self.inspect(self.volumes[role], "volume")
        require(info.get("Name") == self.volumes[role] and info.get("Driver") == "local" and not info.get("Options"),
                "Volume is not a new local laboratory volume")
        labels = info.get("Labels") or {}
        require(labels.get(LABEL) == self.run_id and labels.get(ROLE) == role, "Foreign volume ownership")

    def expected_mounts(self, role):
        if role == "test":
            return {"/etc/x-ui": ("volume", self.volumes["test-data"], True),
                    "/run/chiral-write-contract.test": ("bind", str(self.binary), False),
                    "/run/secrets/chiral-write-token": ("bind", str(self.output / "api.token"), False)}
        return {target: ("volume", self.volumes[key], True) for key, target in VOLUME_TARGETS.items()}

    def create_container(self, role, entrypoint, command, environment=()):
        network = "container:" + self.ids["app"] if role == "test" else "none"
        # A 0600 macOS/Linux bind-mounted token may retain the host UID. With
        # all capabilities dropped, container root cannot override that DAC
        # boundary. Only the test client runs as the artifact owner's UID.
        user = f"{os.geteuid()}:{os.getegid()}" if role == "test" else "0:0"
        args = ["container", "create", "--pull=never", "--platform=linux/amd64", "--name", self.names[role],
                "--label", LABEL + "=" + self.run_id, "--label", ROLE + "=" + role,
                "--network", network, "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                "--memory", "384m", "--memory-swap", "384m", "--cpus", "0.5", "--pids-limit", "128",
                "--restart", "no", "--ipc", "private", "--user", user,
                "--log-opt", "max-size=5m", "--log-opt", "max-file=2",
                "--entrypoint", entrypoint]
        if role == "bootstrap":
            args.append("--interactive")
        for target, (kind, source, writable) in self.expected_mounts(role).items():
            spec = f"type={kind},source={source},target={target}"
            if not writable:
                spec += ",readonly"
            args += ["--mount", spec]
        for value in ["XUI_ENABLE_FAIL2BAN=false", "XUI_IN_DOCKER=true", "XUI_MAIN_FOLDER=/app",
                      "XUI_DB_TYPE=sqlite", "XUI_DB_DSN=", "TZ=UTC", *environment]:
            args += ["--env", value]
        self.launches[role] = (entrypoint, command, tuple(environment))
        self.intended.add(role)
        result = self.docker(args + [self.image] + command)
        identity = result.stdout.decode().strip()
        require(re.fullmatch(ID_RE, identity), "Docker did not return a unique container ID")
        self.ids[role] = identity
        self.state["containers"][role] = {"id": identity, "name": self.names[role]}
        self.save_state()
        self.verify_container(role)

    def inspect_owned(self, role):
        info = self.inspect(self.ids.get(role, self.names[role]))
        identity = info.get("Id", "")
        require(re.fullmatch(ID_RE, identity), "Invalid container identity")
        require(info.get("Name") == "/" + self.names[role], "Unexpected container name")
        require(role not in self.ids or self.ids[role] == identity, "Container identity changed")
        labels = info.get("Config", {}).get("Labels") or {}
        require(labels.get(LABEL) == self.run_id and labels.get(ROLE) == role, "Foreign container ownership")
        selected_image = info.get("Image") == self.image_id
        if not selected_image:
            # Docker's containerd store may expose the parent index in Image.
            # Accept that only when the original immutable reference and the
            # selected runnable manifest/platform are independently identical.
            descriptor = info.get("ImageManifestDescriptor") or {}
            platform = descriptor.get("platform") or {}
            selected_image = (
                info.get("Image") == self.image.rsplit("@", 1)[1]
                and info.get("Config", {}).get("Image") == self.image
                and descriptor.get("digest") == self.image_id
                and platform.get("os") == "linux"
                and platform.get("architecture") == "amd64"
            )
        require(selected_image, "Container uses an unexpected image")
        return info

    def verify_container(self, role):
        info = self.inspect_owned(role)
        entrypoint, command, environment = self.launches[role]
        config = info.get("Config") or {}
        user = f"{os.geteuid()}:{os.getegid()}" if role == "test" else "0:0"
        require(config.get("User") == user, "Unexpected container filesystem identity")
        require(config.get("Entrypoint") == [entrypoint] and (config.get("Cmd") or []) == command,
                "Unexpected container executable or arguments")
        effective_env = dict(value.split("=", 1) for value in config.get("Env", []) if "=" in value)
        require(all(effective_env.get(key) == value for key, value in
                    (entry.split("=", 1) for entry in environment)), "Unexpected test opt-in or endpoint")
        host = info.get("HostConfig") or {}
        network = "container:" + self.ids["app"] if role == "test" else "none"
        require(host.get("NetworkMode") == network and not host.get("PortBindings") and not host.get("PublishAllPorts"),
                "Unsafe container network or published ports")
        require(not any(host.get(k) for k in ("Privileged", "CapAdd", "Devices", "DeviceRequests", "VolumesFrom", "Links", "PidMode")),
                "Unsafe container privilege or namespace option")
        require(host.get("IpcMode") == "private" and host.get("CapDrop") == ["ALL"], "Missing namespace or capability guard")
        require("no-new-privileges:true" in host.get("SecurityOpt", []), "Missing privilege escalation guard")
        require(host.get("Memory") == LIMIT and host.get("MemorySwap") == LIMIT and
                host.get("NanoCpus") == 500000000 and host.get("PidsLimit") == 128, "Unexpected resource limits")
        require((host.get("RestartPolicy") or {}).get("Name") == "no", "Automatic restart is forbidden")
        expected = self.expected_mounts(role)
        mounts = info.get("Mounts") or []
        require(len(mounts) == len(expected), "Unexpected container mounts")
        seen = set()
        for mount in mounts:
            target = mount.get("Destination")
            require(target in expected and target not in seen, "Foreign or repeated mount target")
            seen.add(target)
            kind, source, writable = expected[target]
            actual = mount.get("Name") if kind == "volume" else mount.get("Source")
            same_source = actual == source
            if kind == "bind" and self.docker_desktop:
                # Docker Desktop's Linux VM reports the exact host path below
                # /host_mnt. Never accept arbitrary prefixes/suffix matches or
                # apply this translation to an unverified native daemon.
                same_source = same_source or actual == "/host_mnt" + source
            require(mount.get("Type") == kind and same_source and mount.get("RW") is writable,
                    "Foreign or writable secret/binary mount")
            if kind == "volume":
                volume_role = next(key for key, value in self.volumes.items() if value == source)
                self.verify_volume(volume_role)
        return info

    def bootstrap(self):
        private_write(self.output / "admin.password", self.password + "\n")
        script = ('IFS= read -r write_password; exec /app/x-ui setting -username chiral-write '
                  '-password "$write_password" -port 2053 -listenIP 127.0.0.1 '
                  '-webBasePath /' + self.base_path + '/ -tokenName chiral-write-bootstrap -getApiToken')
        self.create_container("bootstrap", "/bin/sh", ["-c", script])
        result = self.docker(["container", "start", "--attach", "--interactive", self.ids["bootstrap"]],
                             data=(self.password + "\n").encode(), timeout=90)
        info = self.verify_container("bootstrap")
        require(not info["State"]["Running"] and info["State"].get("ExitCode") == 0, "Bootstrap did not exit successfully")
        match = re.search(rb"apiToken:\s*([A-Za-z0-9_-]+)", result.stdout)
        require(match is not None, "Official CLI did not return an API token")
        self.token = match.group(1).decode()
        private_write(self.output / "api.token", self.token + "\n")

    def start_app(self):
        self.create_container("app", "/app/x-ui", [])
        self.docker(["container", "start", self.ids["app"]])
        url = "http://127.0.0.1:2053/" + self.base_path + "/"
        for _ in range(30):
            info = self.verify_container("app")
            require(info["State"]["Running"] and not info["State"].get("OOMKilled"), "Isolated application stopped before readiness")
            result = self.docker(["container", "exec", self.ids["app"], "wget", "--spider", "-q", "-T", "2", url],
                                 timeout=5, check=False)
            if result.returncode == 0:
                return url
            time.sleep(1)
        raise RuntimeError("Application did not become ready within the read-only readiness window")

    def test_once(self, url):
        # The app's fresh /app/bin volume was populated from this exact image.
        # Check its bundled client and data-file DAC permissions under the same
        # UID/GID used by the test container, before enabling any HTTP writes.
        self.verify_container("app")
        self.docker(["container", "exec", "--user", f"{os.geteuid()}:{os.getegid()}", self.ids["app"],
                     "/bin/sh", "-c", "test -x /app/bin/xray-linux-amd64 && "
                     "test -r /app/bin/geoip.dat && test -r /app/bin/geosite.dat"])
        self.state["test_client_files_readable"] = True
        self.create_container("test", "/run/chiral-write-contract.test",
                              ["-test.run", "^" + TEST_NAME + "$", "-test.v", "-test.count=1", "-test.timeout=5m"],
                              ["CHIRAL_3XUI_TEST_URL=" + url,
                               "CHIRAL_3XUI_TEST_TOKEN_FILE=/run/secrets/chiral-write-token",
                               "CHIRAL_3XUI_TEST_XRAY_BIN=/app/bin/xray-linux-amd64",
                               "CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance"])
        # Final ownership, network and every effective mount are checked before
        # the first HTTP write. A failed run is never automatically retried.
        require(self.verify_container("app")["State"]["Running"], "Application is not running")
        self.verify_container("test")
        result = self.docker(["container", "start", "--attach", self.ids["test"]], timeout=360, check=False)
        private_write(self.output / "contract-test.log", result.stdout)
        safe = redact(result.stdout, [self.password, self.token, self.base_path])
        private_write(self.output / "contract-test.redacted.log", safe)
        print(safe[:65536], flush=True)
        info = self.verify_container("test")
        self.state["test_exit_code"] = info["State"].get("ExitCode")
        self.state["docker_test_exit_code"] = result.returncode
        skipped = bool(re.search(r"^\s*--- SKIP:", safe, re.MULTILINE))
        passed = bool(re.search(r"^--- PASS: " + TEST_NAME + r" \(", safe, re.MULTILINE))
        passed = passed and bool(re.search(r"^PASS$", safe, re.MULTILINE))
        self.state["positive_pass"] = passed
        self.state["skipped"] = skipped
        self.state["result"] = "SKIP" if skipped else "FAIL"
        if result.returncode == 0 and self.state["test_exit_code"] == 0 and passed and not skipped and not info["State"]["Running"]:
            self.state["result"] = "PASS"
        self.save_state()

    def cleanup(self):
        # Even after an ambiguous create response, resolve only the exact names
        # this run intended to create and require all ownership checks again.
        verified_ids = []
        for role in ("test", "app", "bootstrap"):
            if role not in self.intended:
                continue
            try:
                # Cleanup only needs exact ownership, not safe runtime
                # metadata: if our own container failed a network/mount guard,
                # leaving it running would make that failure worse.
                info = self.inspect_owned(role)
                identity = info["Id"]
                verified_ids.append(identity)
                self.state["containers"][role] = {"id": identity, "name": self.names[role]}
                if info["State"]["Running"]:
                    self.docker(["container", "stop", "--time", "10", identity], timeout=20)
                final = self.inspect_owned(role)
                require(not final["State"]["Running"], "Owned container is still running")
                if role == "test":
                    self.state["test_exit_code"] = final["State"].get("ExitCode")
                self.state["cleanup"][role] = "stopped; container and volumes retained"
                if role in {"test", "app"}:
                    logs = self.docker(["container", "logs", "--tail", "5000", identity], check=False)
                    if logs.returncode == 0:
                        safe = redact(logs.stdout, [self.password, self.token, self.base_path])
                        private_write(self.output / (role + ".redacted.log"), safe)
                        if role == "test" and not (self.output / "contract-test.log").exists():
                            private_write(self.output / "contract-test.log", logs.stdout)
                            print(safe[:65536], flush=True)
            except (RuntimeError, KeyError, ValueError, OSError) as exc:
                self.state["cleanup"][role] = "not stopped: " + redact(str(exc), [self.password, self.token, self.base_path])
                if self.state["result"] == "PASS":
                    self.state["result"] = "CLEANUP_FAILED"
        self.state["cleanup_commands"] = []
        for identity in verified_ids:
            # These are exact-ID, non-destructive stop commands, not a broad
            # cleanup selector. Never print volume removal or prune commands.
            self.state["cleanup_commands"].append(shlex.join(["docker", "--host", self.endpoint,
                "container", "stop", "--time", "10", identity]))
        self.save_state()

    def run(self):
        try:
            self.preflight()
            self.create_volumes()
            self.bootstrap()
            self.test_once(self.start_app())
        except (RuntimeError, KeyError, ValueError, OSError) as exc:
            self.state["result"] = "ERROR"
            self.state["error"] = redact(str(exc), [self.password, self.token, self.base_path])
        finally:
            self.cleanup()
            print(json.dumps(self.state, indent=2), flush=True)
        return 0 if self.state["result"] == "PASS" else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        return WriteContract(args.image, args.binary, args.output).run()
    except (RuntimeError, OSError) as exc:
        print(json.dumps({"result": "ERROR", "error": str(exc)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
