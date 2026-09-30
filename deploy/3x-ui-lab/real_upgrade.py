#!/usr/bin/env python3
"""Real official downloads in a new, restricted-egress laboratory only.

No production discovery, image pulling, existing-resource adoption or deletion.
All containers, volumes, networks and private evidence remain after a run.
"""

import argparse
import ipaddress
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import time

from write_contract import WriteContract, LABEL, ROLE, ID_RE, private_write, require, redact, execute


STAGES = ("prepare", "baseline-rebuilt", "upgrade", "target-rebuilt")
TEST_NAME = "TestLive3XUIRealUpgrade"
SYSCTLS = {"net.ipv4.ip_forward": "0", "net.ipv6.conf.all.disable_ipv6": "1"}
PROXY_ENV = ("HTTP_PROXY=", "http_proxy=", "HTTPS_PROXY=", "https_proxy=",
             "ALL_PROXY=", "all_proxy=", "NO_PROXY=", "no_proxy=")


def executable(path):
    supplied = Path(path)
    require(supplied.is_absolute() and not supplied.is_symlink() and supplied.is_file(),
            "Proxy binary must be an absolute non-symlink regular file")
    resolved = supplied.resolve(strict=True)
    require("," not in str(resolved) and resolved.stat().st_mode & stat.S_IXUSR,
            "Proxy binary must be executable and its mount path cannot contain commas")
    with resolved.open("rb") as source:
        header = source.read(20)
    require(len(header) == 20 and header[:6] == b"\x7fELF\x02\x01" and header[18:20] == b"\x3e\x00",
            "Proxy binary must be a Linux/amd64 ELF executable")
    return resolved


def stage_passed(stage, data, exit_code):
    text = data.decode(errors="replace")
    return (stage in STAGES and exit_code == 0
            and not re.search(r"^\s*--- (?:SKIP|FAIL):", text, re.MULTILINE)
            and re.search(r"^--- PASS: " + TEST_NAME + r" \(", text, re.MULTILINE) is not None
            and re.search(r"^PASS$", text, re.MULTILINE) is not None
            and re.search(r"(?:^|\s)real_upgrade_stage_verified=" + re.escape(stage) + r"(?:\s|$)", text) is not None)


class RealUpgrade(WriteContract):
    def __init__(self, image, binary, proxy_binary, output):
        self.proxy_binary = executable(proxy_binary)
        super().__init__(image, binary, output)
        roles = ["bootstrap", "proxy", "app-0", "app-1", "app-2"] + ["test-" + stage for stage in STAGES]
        self.names = {role: self.run_id + "-" + role for role in roles}
        self.volumes["proxy-data"] = self.run_id + "-proxy-data"
        self.network_names = {role: self.run_id + "-" + role for role in ("internal", "egress")}
        self.network_ids, self.network_subnets = {}, {}
        self.test_apps = {}
        self.proxy_ip = None
        self.proxy_connected = False
        self.test_state = self.output / "test-state"
        self.test_state.mkdir(mode=0o700)
        self.test_state.chmod(0o700)
        self.state.update(suite="real-upgrade", test_name=TEST_NAME, networks={}, stages={},
                          test_state=str(self.test_state), real_downloads=True,
                          scope="new local laboratory only; not production acceptance")

    def preflight(self):
        super().preflight()
        existing = set(self.docker(["network", "ls", "--format", "{{.Name}}"]).stdout.decode().splitlines())
        require(not existing.intersection(self.network_names.values()), "Generated network name already exists")
        owned = self.docker(["network", "ls", "--filter", "label=" + LABEL + "=" + self.run_id,
                             "--format", "{{.Name}}"])
        require(not owned.stdout.strip(), "Networks already carry this run's ownership label")

    def create_networks(self):
        for role in ("internal", "egress"):
            args = ["network", "create", "--driver", "bridge", "--ipv6=false",
                    "--label", LABEL + "=" + self.run_id, "--label", ROLE + "=" + role]
            if role == "internal":
                args += ["--internal", "--opt", "com.docker.network.bridge.gateway_mode_ipv4=isolated"]
            identity = self.docker(args + [self.network_names[role]]).stdout.decode().strip()
            require(re.fullmatch(ID_RE, identity), "Network creation did not return an immutable ID")
            self.network_ids[role] = identity
            self.state["networks"][role] = {"id": identity, "name": self.network_names[role]}
            self.save_state()
            info = self.verify_network(role)
            subnet = ipaddress.ip_network(info["IPAM"]["Config"][0]["Subnet"])
            self.network_subnets[role] = subnet
        require(not self.network_subnets["internal"].overlaps(self.network_subnets["egress"]), "Laboratory networks overlap")
        self.proxy_ip = str(self.network_subnets["internal"].network_address + 2)

    def verify_network(self, role):
        info = self.inspect(self.network_ids[role], "network")
        require(info.get("Id") == self.network_ids[role] and info.get("Name") == self.network_names[role], "Network identity changed")
        labels = info.get("Labels") or {}
        require(labels.get(LABEL) == self.run_id and labels.get(ROLE) == role, "Foreign network ownership")
        require(info.get("Driver") == "bridge" and info.get("Scope") == "local" and not info.get("EnableIPv6"),
                "Unexpected network driver, scope or IPv6")
        require(info.get("Internal") is (role == "internal"), "Unexpected network egress policy")
        options = dict(info.get("Options") or {})
        # Recent Docker versions materialize this default option in inspect.
        # Accept that one canonical value without permitting other overrides.
        if "com.docker.network.enable_ipv4" in options:
            require(options.pop("com.docker.network.enable_ipv4") == "true", "IPv4 network mode changed")
        if role == "internal":
            require(options == {"com.docker.network.bridge.gateway_mode_ipv4": "isolated"}, "Host gateway isolation is absent or overridden")
        else:
            require(not options, "Unexpected egress network options")
        configs = (info.get("IPAM") or {}).get("Config") or []
        require(len(configs) == 1, "Unexpected network address ranges")
        subnet = ipaddress.ip_network(configs[0].get("Subnet", ""))
        require(subnet.version == 4 and subnet.is_private and 16 <= subnet.prefixlen <= 28, "Unexpected network subnet")
        if role in self.network_subnets:
            require(subnet == self.network_subnets[role], "Network subnet changed")
        expected = set()
        for name, identity in self.ids.items():
            if name == "proxy" and (role == "internal" or self.proxy_connected):
                expected.add(identity)
            elif name.startswith("app-") and role == "internal":
                expected.add(identity)
        require(set((info.get("Containers") or {}).keys()) <= expected, "Foreign endpoint joined laboratory network")
        return info

    def expected_network(self, role):
        if role.startswith("test-"):
            return "container:" + self.test_apps[role]
        if role == "proxy" or role.startswith("app-"):
            return self.network_ids["internal"]
        return "none"

    def expected_user(self, role):
        if role.startswith("test-") or role == "proxy":
            return f"{os.geteuid()}:{os.getegid()}"
        return "0:0"

    def expected_mounts(self, role):
        if role.startswith("test-"):
            return {"/etc/x-ui": ("volume", self.volumes["test-data"], True),
                    "/run/chiral-write-contract.test": ("bind", str(self.binary), False),
                    "/run/secrets/chiral-write-token": ("bind", str(self.output / "api.token"), False),
                    "/run/chiral-upgrade-state": ("bind", str(self.test_state), True)}
        if role == "proxy":
            return {"/etc/x-ui": ("volume", self.volumes["proxy-data"], True),
                    "/run/chiral-egress-proxy": ("bind", str(self.proxy_binary), False)}
        return super().expected_mounts(role)

    def container_options(self, role):
        options = []
        if role == "proxy" or role.startswith("app-"):
            for key, value in SYSCTLS.items():
                options += ["--sysctl", key + "=" + value]
            if role == "proxy":
                options += ["--ip", self.proxy_ip]
            else:
                # Downloads resolve at the proxy. Do not give the application
                # an independent external DNS path through Docker's resolver.
                options += ["--dns", "127.0.0.1"]
        if role == "proxy" or role.startswith("test-"):
            options += ["--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=32m,mode=1777"]
        return options

    def verify_container(self, role):
        info = super().verify_container(role)
        host = info["HostConfig"]
        require(not host.get("ExtraHosts") and not host.get("DnsSearch"), "Unexpected hostname routing override")
        for mount in host.get("Mounts") or []:
            require(not (mount.get("VolumeOptions") or {}).get("NoCopy"), "Kernel volume initialization cannot be disabled")
        networks = (info.get("NetworkSettings") or {}).get("Networks") or {}
        if role == "proxy" or role.startswith("app-"):
            running = info["State"].get("Running") is True
            require(host.get("Sysctls") == SYSCTLS, "Forwarding or IPv6 protection is absent")
            wanted = {self.network_names["internal"]}
            if role == "proxy" and self.proxy_connected:
                wanted.add(self.network_names["egress"])
            # Docker may defer endpoint allocation until start. Before that,
            # require exact requested network identities and static-IP intent;
            # once running, require all resolved names, IDs and addresses.
            normalized = {}
            for name, endpoint in networks.items():
                candidates = [key for key in self.network_names
                              if name == self.network_names[key] or (not running and name == self.network_ids[key])]
                require(len(candidates) == 1, "Container joined an unexpected network")
                network_role = candidates[0]
                canonical = self.network_names[network_role]
                require(canonical not in normalized, "Repeated network identity")
                normalized[canonical] = endpoint
                actual_id = endpoint.get("NetworkID")
                require(actual_id == self.network_ids[network_role] or (not running and not actual_id), "Container network identity changed")
                require(not endpoint.get("GlobalIPv6Address"), "Container acquired an IPv6 address")
                if running:
                    address = ipaddress.ip_address(endpoint.get("IPAddress", ""))
                    require(address in self.network_subnets[network_role], "Container address is outside the laboratory subnet")
            require(set(normalized) == wanted, "Container joined an unexpected network")
            if role == "proxy":
                endpoint = normalized[self.network_names["internal"]]
                configured = (endpoint.get("IPAMConfig") or {}).get("IPv4Address")
                actual = endpoint.get("IPAddress")
                require(actual == self.proxy_ip or (not running and not actual and configured == self.proxy_ip), "Proxy address changed")
            else:
                require(host.get("Dns") == ["127.0.0.1"], "Application has an independent external DNS path")
        elif role.startswith("test-"):
            require(not networks, "Test acquired a separate network")
        else:
            require(set(networks) <= {"none"}, "Bootstrap acquired an external network")
        if role == "proxy" or role.startswith("test-"):
            require(host.get("ReadonlyRootfs") is True and set(host.get("Tmpfs") or {}) == {"/tmp"},
                    "Test or proxy filesystem guards are absent")
        return info

    def runtime_network_guard(self, role):
        info = self.verify_container(role)
        require(info["State"]["Running"], "Runtime network inspection requires the owned running process")
        for key, value in SYSCTLS.items():
            path = "/proc/sys/" + key.replace(".", "/")
            got = self.docker(["container", "exec", self.ids[role], "cat", path]).stdout.decode().strip()
            require(got == value, "Effective network sysctl differs from the isolation policy")
        if role.startswith("app-"):
            data = self.docker(["container", "exec", self.ids[role], "cat", "/proc/net/route"]).stdout.decode()
            rows = [line.split() for line in data.splitlines()[1:] if line.strip()]
            require(rows, "No isolated application route was observed")
            for row in rows:
                require(len(row) >= 8, "Cannot interpret application routes")
                destination = ipaddress.IPv4Address(int(row[1], 16).to_bytes(4, "little"))
                mask = ipaddress.IPv4Address(int(row[7], 16).to_bytes(4, "little"))
                route = ipaddress.ip_network(f"{destination}/{mask}")
                require(row[2] == "00000000" and route == self.network_subnets["internal"],
                        "Application has a gateway or non-laboratory route")
        for network in self.network_ids:
            self.verify_network(network)

    def start_proxy(self):
        self.create_container("proxy", "/run/chiral-egress-proxy", ["--listen", self.proxy_ip + ":3128"], PROXY_ENV)
        self.docker(["network", "connect", self.network_ids["egress"], self.ids["proxy"]])
        self.proxy_connected = True
        self.verify_container("proxy")
        self.docker(["container", "start", self.ids["proxy"]])
        self.runtime_network_guard("proxy")

    def start_application(self, role):
        # A recreated container must never compete with its predecessor for
        # the same database/kernel volumes, even during test failures.
        for old in self.ids:
            if old.startswith(("app-", "test-")):
                require(not self.inspect_owned(old)["State"]["Running"], "Previous application or test is still running")
        proxy = "http://" + self.proxy_ip + ":3128"
        env = [value for value in PROXY_ENV if not value.startswith(("HTTPS_PROXY=", "NO_PROXY="))]
        env += ["HTTPS_PROXY=" + proxy, "NO_PROXY=127.0.0.1,localhost"]
        self.create_container(role, "/app/x-ui", [], env)
        require(len(set(self.ids.values())) == len(self.ids), "Container recreation reused an existing identity")
        self.docker(["container", "start", self.ids[role]])
        self.runtime_network_guard(role)
        url = "http://127.0.0.1:2053/" + self.base_path + "/"
        for _ in range(30):
            info = self.verify_container(role)
            require(info["State"]["Running"] and not info["State"].get("OOMKilled"), "Application stopped before readiness")
            result = self.docker(["container", "exec", self.ids[role], "wget", "--spider", "-q", "-T", "2", url], timeout=5, check=False)
            if result.returncode == 0:
                return url
            time.sleep(1)
        raise RuntimeError("Application did not become ready")

    def wait_test(self, role, deadline):
        # docker wait only observes this already-running immutable ID. An
        # observation timeout must not dispatch start or an installation again.
        while time.monotonic() < deadline:
            try:
                info = self.inspect_owned(role)
            except RuntimeError:
                # A read failure is not proof that this known handle ended.
                time.sleep(1)
                continue
            if not info["State"]["Running"]:
                return info["State"].get("ExitCode")
            try:
                result = execute(["docker", "--host", self.endpoint, "container", "wait", self.ids[role]],
                                 timeout=min(45, max(1, deadline - time.monotonic())))
                if result.returncode != 0:
                    time.sleep(1)
            except subprocess.TimeoutExpired:
                continue
        info = self.inspect_owned(role)
        if not info["State"]["Running"]:
            return info["State"].get("ExitCode")
        raise RuntimeError("Stage remains running after observation budget; result is unknown")

    def stage(self, stage, app, url):
        role = "test-" + stage
        require(stage not in self.state["stages"], "A stage cannot be dispatched twice")
        self.runtime_network_guard(app)
        self.runtime_network_guard("proxy")
        self.test_apps[role] = self.ids[app]
        env = [*PROXY_ENV, "CHIRAL_3XUI_TEST_URL=" + url,
               "CHIRAL_3XUI_TEST_TOKEN_FILE=/run/secrets/chiral-write-token",
               "CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance",
               "CHIRAL_3XUI_TEST_UPGRADE_STAGE=" + stage,
               "CHIRAL_3XUI_TEST_STATE_DIR=/run/chiral-upgrade-state"]
        self.create_container(role, "/run/chiral-write-contract.test",
                              ["-test.run", "^" + TEST_NAME + "$", "-test.v", "-test.count=1", "-test.timeout=8m"], env)
        self.state["stages"][stage] = {"result": "DISPATCHED", "container_id": self.ids[role], "app_id": self.ids[app]}
        self.save_state()
        print(json.dumps({"stage": stage, "result": "DISPATCHED"}), flush=True)
        # Detached start is sent once. All later calls observe the same handle.
        try:
            self.docker(["container", "start", self.ids[role]])
        except RuntimeError:
            # The start response may be lost after dispatch. Observe only;
            # do not replay start or infer the install operation's outcome.
            self.state["stages"][stage]["start_response_unknown"] = True
            self.save_state()
        code = self.wait_test(role, time.monotonic() + 540)
        info = self.verify_container(role)
        require(not info["State"]["Running"], "Test did not finish")
        result = self.docker(["container", "logs", self.ids[role]])
        private_write(self.output / (stage + ".log"), result.stdout)
        safe = redact(result.stdout, [self.password, self.token, self.base_path])
        private_write(self.output / (stage + ".redacted.log"), safe)
        print(safe[:65536], flush=True)
        passed = stage_passed(stage, result.stdout, code)
        self.state["stages"][stage].update(result="PASS" if passed else "FAIL", exit_code=code,
                                           log_read_exit_code=result.returncode, positive_pass=passed)
        self.save_state()
        require(passed, "Real upgrade stage did not pass; no subsequent stage will run")

    def stop_owned(self, role):
        info = self.inspect_owned(role)
        if info["State"]["Running"]:
            self.docker(["container", "stop", "--time", "10", info["Id"]], timeout=20)
        require(not self.inspect_owned(role)["State"]["Running"], "Owned process did not stop")

    def cleanup(self):
        # Stop tests before their network owner, and stop the network gateway
        # last. Never delete evidence or redispatch an uncertain operation.
        roles = sorted(self.intended, key=lambda r: (0 if r.startswith("test-") else 1 if r.startswith("app-") else 3 if r == "proxy" else 2, r))
        for role in roles:
            try:
                self.stop_owned(role)
                self.state["cleanup"][role] = "stopped; resources retained"
                logs = self.docker(["container", "logs", "--tail", "5000", self.inspect_owned(role)["Id"]], check=False)
                if logs.returncode == 0:
                    private_write(self.output / (role + ".redacted.log"), redact(logs.stdout, [self.password, self.token, self.base_path]))
            except (RuntimeError, KeyError, ValueError, OSError) as exc:
                self.state["cleanup"][role] = "not stopped: " + redact(str(exc), [self.password, self.token, self.base_path])
                if self.state["result"] == "PASS":
                    self.state["result"] = "CLEANUP_FAILED"
        self.save_state()

    def run(self):
        try:
            self.preflight()
            self.create_volumes()
            self.create_networks()
            self.bootstrap()
            self.start_proxy()
            url = self.start_application("app-0")
            self.stage("prepare", "app-0", url)
            self.stop_owned("app-0")
            url = self.start_application("app-1")
            self.stage("baseline-rebuilt", "app-1", url)
            self.stage("upgrade", "app-1", url)
            self.stop_owned("app-1")
            url = self.start_application("app-2")
            self.stage("target-rebuilt", "app-2", url)
            self.state["result"] = "PASS"
        except (RuntimeError, KeyError, ValueError, OSError) as exc:
            self.state["result"] = "INCOMPLETE"
            self.state["error"] = redact(str(exc), [self.password, self.token, self.base_path])
            for stage in self.state["stages"].values():
                if stage["result"] == "DISPATCHED":
                    stage["result"] = "UNKNOWN; laboratory stopped without inferring installation result"
        finally:
            self.cleanup()
            print(json.dumps(self.state, indent=2), flush=True)
        return 0 if self.state["result"] == "PASS" else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ("image", "binary", "proxy-binary", "output"):
        parser.add_argument("--" + flag, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        return RealUpgrade(args.image, args.binary, args.proxy_binary, args.output).run()
    except (RuntimeError, OSError):
        print(json.dumps({"result": "ERROR", "error": "Cannot initialize private real-upgrade laboratory"}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
