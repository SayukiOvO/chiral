"""Restricted-egress runner tests. Every Docker operation is simulated."""

from copy import deepcopy
import io
import ipaddress
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

import real_upgrade as runner
import write_contract as base
from test_write_contract import FakeDocker, IMAGE, SOCKET, TOKEN, flag, flags


def passed(stage):
    return ("    real_upgrade_stage_verified=" + stage + "\n"
            "--- PASS: TestLive3XUIRealUpgrade (0.01s)\nPASS\n").encode()


class RealUpgradeDocker(FakeDocker):
    """Extend the established fake with networks and detached stage handles."""

    def __init__(self):
        super().__init__()
        self.networks = {}
        self.after_network_create = None
        self.stage_outputs = {}
        self.stage_exits = {}
        self.wait_actions = {}
        self.runtime_sysctls = {}
        self.route_output = None
        self.stubborn_stops = set()
        self.lost_start_responses = set()
        self.inspect_failures = {}
        self.defer_endpoints = False
        self.pending_endpoints = {}
        self.after_start = None
        self.clock = 0.0

    def sleep(self, seconds):
        self.clock += seconds

    def role(self, info):
        return info["Config"]["Labels"][base.ROLE]

    def net(self, identity):
        if identity in self.networks:
            return self.networks[identity]
        return next(net for net in self.networks.values() if net["Name"] == identity)

    def attach(self, info, identity, address=None):
        net = self.net(identity)
        subnet = ipaddress.ip_network(net["IPAM"]["Config"][0]["Subnet"])
        requested = address
        address = address or str(subnet.network_address + 10 + len(net["Containers"]))
        if self.defer_endpoints and not info["State"]["Running"]:
            info["NetworkSettings"]["Networks"][identity] = {
                "NetworkID": "", "IPAddress": "", "GlobalIPv6Address": "",
                "IPAMConfig": {"IPv4Address": requested} if requested else None,
            }
            self.pending_endpoints[(info["Id"], identity)] = address
            return
        endpoint = {"NetworkID": net["Id"], "IPAddress": address, "GlobalIPv6Address": ""}
        info["NetworkSettings"]["Networks"][net["Name"]] = endpoint
        net["Containers"][info["Id"]] = {"Name": info["Name"].removeprefix("/"), "IPv4Address": address + "/24"}

    def __call__(self, args, data=None, timeout=60):
        command = args[3:] if args[:3] == ["docker", "--host", SOCKET] else []
        if command[:1] == ["network"]:
            self.calls.append((args, data, timeout))
            operation = command[1]
            if operation == "ls":
                return self.response(args)
            if operation == "create":
                identity = f"{256 + len(self.networks):064x}"
                self.networks[identity] = {
                    "Id": identity, "Name": command[-1], "Driver": flag(command, "--driver"), "Scope": "local",
                    "Labels": dict(value.split("=", 1) for value in flags(command, "--label")),
                    "Internal": "--internal" in command, "EnableIPv6": "--ipv6=false" not in command,
                    "Options": dict(value.split("=", 1) for value in flags(command, "--opt")),
                    "IPAM": {"Config": [{"Subnet": f"172.29.{len(self.networks) + 1}.0/24"}]}, "Containers": {},
                }
                if self.after_network_create:
                    self.after_network_create(self.networks[identity])
                return self.response(args, (identity + "\n").encode())
            if operation == "inspect":
                return self.response(args, [self.net(command[-1])])
            if operation == "connect":
                self.attach(self.lookup(command[-1]), command[-2])
                return self.response(args)
            raise AssertionError("Unexpected network mutation: " + repr(command))

        if command[:2] == ["container", "create"]:
            after_create, self.after_create = self.after_create, None
            try:
                result = super().__call__(args, data=data, timeout=timeout)
            finally:
                self.after_create = after_create
            info = self.lookup(result.stdout.decode().strip())
            host = info["HostConfig"]
            host["Sysctls"] = dict(value.split("=", 1) for value in flags(command, "--sysctl"))
            host["Dns"] = flags(command, "--dns")
            host["ReadonlyRootfs"] = "--read-only" in command
            host["Tmpfs"] = dict(value.split(":", 1) for value in flags(command, "--tmpfs"))
            host["Mounts"] = []
            for value in flags(command, "--mount"):
                parts = value.split(",")
                fields = dict(part.split("=", 1) for part in parts if "=" in part)
                host["Mounts"].append({"Type": fields["type"], "Target": fields["target"],
                                       "Source": fields["source"], "VolumeOptions": {"NoCopy": "volume-nocopy" in parts}})
            info["NetworkSettings"] = {"Networks": {}}
            if host["NetworkMode"] in self.networks:
                address = flag(command, "--ip") if "--ip" in command else None
                self.attach(info, host["NetworkMode"], address)
            elif host["NetworkMode"] == "none":
                info["NetworkSettings"]["Networks"] = {"none": {}}
            if after_create:
                after_create(info)
            return result

        if command[:2] == ["container", "start"]:
            info = self.lookup(command[-1])
            role = self.role(info)
            if role != "bootstrap":
                self.calls.append((args, data, timeout))
                info["State"]["Running"] = True
                for identity in list(info["NetworkSettings"]["Networks"]):
                    key = (info["Id"], identity)
                    if key in self.pending_endpoints:
                        del info["NetworkSettings"]["Networks"][identity]
                        self.attach(info, identity, self.pending_endpoints.pop(key))
                if self.after_start:
                    self.after_start(info)
                if role in self.lost_start_responses:
                    self.lost_start_responses.remove(role)
                    raise subprocess.TimeoutExpired(args, timeout)
                return self.response(args)

        if command[:2] == ["container", "inspect"]:
            info = self.lookup(command[-1])
            role = self.role(info)
            if info["State"]["Running"] and self.inspect_failures.get(role, 0) > 0:
                self.calls.append((args, data, timeout))
                self.inspect_failures[role] -= 1
                return self.response(args, code=1)

        if command[:2] == ["container", "wait"]:
            self.calls.append((args, data, timeout))
            info = self.lookup(command[-1])
            stage = self.role(info).removeprefix("test-")
            pending = self.wait_actions.setdefault(stage, [])
            action = pending.pop(0) if pending else "complete"
            if action == "timeout":
                self.clock += timeout
                raise subprocess.TimeoutExpired(args, timeout)
            if action == "nonzero":
                self.clock += 1
                return self.response(args, code=1)
            if action != "complete":
                raise AssertionError("Invalid fake wait action")
            code = self.stage_exits.get(stage, 0)
            info["State"].update(Running=False, ExitCode=code)
            return self.response(args, (str(code) + "\n").encode())

        if command[:2] == ["container", "logs"]:
            self.calls.append((args, data, timeout))
            role = self.role(self.lookup(command[-1]))
            if role.startswith("test-"):
                stage = role.removeprefix("test-")
                return self.response(args, self.stage_outputs.get(stage, passed(stage)))
            return self.response(args, b"non-sensitive process diagnostic\n")

        if command[:2] == ["container", "exec"] and command[3:4] == ["cat"]:
            self.calls.append((args, data, timeout))
            role = self.role(self.lookup(command[2]))
            path = command[-1]
            if path == "/proc/net/route":
                if self.route_output is not None:
                    return self.response(args, self.route_output)
                net = self.net(self.lookup(command[2])["HostConfig"]["NetworkMode"])
                subnet = ipaddress.ip_network(net["IPAM"]["Config"][0]["Subnet"])
                address = subnet.network_address.packed[::-1].hex().upper()
                mask = subnet.netmask.packed[::-1].hex().upper()
                return self.response(args, ("Iface Destination Gateway Flags RefCnt Use Metric Mask\n"
                                            f"eth0 {address} 00000000 0001 0 0 0 {mask}\n").encode())
            key = path.removeprefix("/proc/sys/").replace("/", ".")
            if key not in runner.SYSCTLS:
                raise AssertionError("Unexpected namespace sysctl read")
            value = self.runtime_sysctls.get((role, key), runner.SYSCTLS[key])
            return self.response(args, (value + "\n").encode())

        if command[:2] == ["container", "stop"] and self.role(self.lookup(command[-1])) in self.stubborn_stops:
            self.calls.append((args, data, timeout))
            return self.response(args)
        return super().__call__(args, data=data, timeout=timeout)


class RealUpgradeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.binary = self.root / "contract.test"
        self.proxy = self.root / "proxy"
        for path in (self.binary, self.proxy):
            path.write_bytes(b"\x7fELF\x02\x01" + b"\x00" * 12 + b"\x3e\x00")
            path.chmod(0o700)
        self.fake = RealUpgradeDocker()
        self.console = io.StringIO()
        self.counter = 0
        for patch in (
            mock.patch.object(base.subprocess, "run", side_effect=AssertionError("External processes are forbidden")),
            mock.patch.dict(os.environ, {}, clear=True),
            mock.patch.object(base, "execute", side_effect=self.fake),
            mock.patch.object(runner, "execute", side_effect=self.fake),
            mock.patch.object(runner.time, "sleep", side_effect=self.fake.sleep),
            mock.patch.object(runner.time, "monotonic", side_effect=lambda: self.fake.clock),
            mock.patch.object(runner.sys, "stdout", self.console),
        ):
            patch.start()
            self.addCleanup(patch.stop)

    def instance(self):
        self.counter += 1
        return runner.RealUpgrade(IMAGE, str(self.binary), str(self.proxy), str(self.root / f"result-{self.counter}"))

    def commands(self, kind=None, operation=None):
        commands = [args[3:] for args, _, _ in self.fake.calls if args[:3] == ["docker", "--host", SOCKET]]
        if kind is not None:
            commands = [cmd for cmd in commands if cmd[0] == kind]
        if operation is not None:
            commands = [cmd for cmd in commands if cmd[1] == operation]
        return commands

    def starts(self, role):
        return [cmd for cmd in self.commands("container", "start") if self.fake.role(self.fake.lookup(cmd[-1])) == role]

    def prepared(self):
        lab = self.instance()
        lab.preflight()
        lab.create_volumes()
        lab.create_networks()
        lab.bootstrap()
        lab.start_proxy()
        return lab, lab.start_application("app-0")

    def test_four_stages_pass_with_recreation_and_stop_retention(self):
        lab = self.instance()
        self.assertEqual(lab.run(), 0)
        self.assertEqual(lab.state["result"], "PASS")
        self.assertEqual(tuple(lab.state["stages"]), runner.STAGES)
        self.assertTrue(all(stage["result"] == "PASS" and stage["exit_code"] == 0 for stage in lab.state["stages"].values()))
        self.assertEqual(len(self.fake.containers), 9)
        self.assertEqual(len(self.fake.networks), 2)
        self.assertEqual(len(self.fake.volumes), 5)
        self.assertTrue(all(not info["State"]["Running"] for info in self.fake.containers.values()))
        self.assertTrue(all(value == "stopped; resources retained" for value in lab.state["cleanup"].values()))
        self.assertEqual(len({lab.ids[role] for role in ("app-0", "app-1", "app-2")}), 3)
        for stage, app in zip(runner.STAGES, ("app-0", "app-1", "app-1", "app-2")):
            role = "test-" + stage
            self.assertEqual(len(self.starts(role)), 1)
            info = self.fake.lookup(lab.ids[role])
            self.assertEqual(info["HostConfig"]["NetworkMode"], "container:" + lab.ids[app])
            self.assertEqual(flag(info["Config"]["Cmd"], "-test.run"), "^TestLive3XUIRealUpgrade$")
            targets = {mount["Destination"]: mount for mount in info["Mounts"]}
            self.assertEqual(targets["/run/chiral-upgrade-state"]["Source"], str(lab.test_state))
            self.assertTrue(targets["/run/chiral-upgrade-state"]["RW"])
            self.assertNotIn("/app/bin", targets)
            self.assertNotEqual(targets["/etc/x-ui"]["Name"], lab.volumes["data"])
            self.assertEqual(lab.state["stages"][stage]["log_read_exit_code"], 0)
        for role in ("app-0", "app-1", "app-2"):
            mounts = {mount["Destination"]: mount for mount in self.fake.lookup(lab.ids[role])["Mounts"]}
            self.assertEqual(mounts["/etc/x-ui"]["Name"], lab.volumes["data"])
            self.assertEqual(mounts["/app/bin"]["Name"], lab.volumes["bin"])
        for cmd in self.commands():
            self.assertNotIn(cmd[1], {"rm", "prune", "pull", "disconnect"})
        for cmd in self.commands("container", "create"):
            self.assertNotIn("--publish", cmd)
            self.assertNotIn("--privileged", cmd)
            self.assertIn("--pull=never", cmd)
        self.assertEqual(lab.test_state.stat().st_mode & 0o777, 0o700)
        for path in lab.output.iterdir():
            if path.is_file():
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        for value in (lab.password, TOKEN):
            self.assertNotIn(value, self.console.getvalue())
            self.assertNotIn(value, (lab.output / "state.json").read_text())

    def test_created_endpoints_can_resolve_only_after_start(self):
        self.fake.defer_endpoints = True
        lab = self.instance()
        self.assertEqual(lab.run(), 0)
        self.assertFalse(self.fake.pending_endpoints)
        self.assertEqual(tuple(lab.state["stages"]), runner.STAGES)
        self.assertTrue(all(stage["result"] == "PASS" for stage in lab.state["stages"].values()))

    def test_created_proxy_requires_the_exact_requested_static_address(self):
        self.fake.defer_endpoints = True

        def tamper(info):
            if self.fake.role(info) == "proxy":
                endpoint = next(iter(info["NetworkSettings"]["Networks"].values()))
                endpoint["IPAMConfig"]["IPv4Address"] = "192.0.2.1"

        self.fake.after_create = tamper
        lab = self.instance()
        self.assertEqual(lab.run(), 1)
        self.assertEqual(lab.state["error"], "Proxy address changed")
        self.assertEqual(self.starts("proxy"), [])
        self.assertNotIn("app-0", lab.ids)

    def test_running_endpoints_must_have_resolved_network_identity_and_address(self):
        self.fake.defer_endpoints = True
        for name in ("missing-id", "foreign-id", "missing-address", "outside-subnet", "unresolved-key"):
            with self.subTest(name=name):
                def tamper(info):
                    if self.fake.role(info) != "app-0":
                        return
                    networks = info["NetworkSettings"]["Networks"]
                    key, endpoint = next(iter(networks.items()))
                    if name == "missing-id":
                        endpoint["NetworkID"] = ""
                    elif name == "foreign-id":
                        endpoint["NetworkID"] = "f" * 64
                    elif name == "missing-address":
                        endpoint["IPAddress"] = ""
                    elif name == "outside-subnet":
                        endpoint["IPAddress"] = "192.0.2.1"
                    else:
                        networks[endpoint["NetworkID"]] = networks.pop(key)

                self.fake.after_start = tamper
                lab = self.instance()
                self.assertEqual(lab.run(), 1)
                self.assertEqual(lab.state["stages"], {})
                self.assertFalse(self.fake.lookup(lab.ids["app-0"])["State"]["Running"])

    def test_unsafe_networks_stop_before_any_application_or_test(self):
        mutations = {
            "not_internal": lambda net: net.update(Internal=False),
            "wrong_driver": lambda net: net.update(Driver="host"),
            "wrong_scope": lambda net: net.update(Scope="swarm"),
            "ipv6": lambda net: net.update(EnableIPv6=True),
            "missing_isolated": lambda net: net["Options"].clear(),
            "nat_gateway": lambda net: net["Options"].update({"com.docker.network.bridge.gateway_mode_ipv4": "nat"}),
            "extra_options": lambda net: net["Options"].update({"com.docker.network.bridge.trusted_host_interfaces": "eth0"}),
            "foreign_endpoint": lambda net: net["Containers"].update({"f" * 64: {}}),
            "foreign_owner": lambda net: net["Labels"].update({base.LABEL: "production"}),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                self.fake.after_network_create = lambda net, mutate=mutate: mutate(net) if net["Labels"].get(base.ROLE) == "internal" else None
                lab = self.instance()
                self.assertEqual(lab.run(), 1)
                self.assertEqual(lab.state["result"], "INCOMPLETE")
                self.assertFalse(any(role.startswith(("app-", "test-")) for role in lab.intended))

    def test_docker_ipv4_normalization_is_allowed_only_when_true(self):
        for value in ("true", "false", "1", "TRUE", "", True):
            with self.subTest(value=value):
                self.fake.after_network_create = lambda net, value=value: net["Options"].update({"com.docker.network.enable_ipv4": value})
                lab = self.instance()
                if value == "true":
                    self.assertEqual(lab.run(), 0)
                    self.assertEqual(lab.state["result"], "PASS")
                else:
                    self.assertEqual(lab.run(), 1)
                    self.assertEqual(lab.state["result"], "INCOMPLETE")
                    self.assertFalse(lab.state["stages"])

    def test_unsafe_container_metadata_prevents_stage_dispatch(self):
        def extra_network(info):
            info["NetworkSettings"]["Networks"]["foreign-egress"] = {"NetworkID": "f" * 64}

        mutations = {
            "extra_network": extra_network,
            "ports": lambda info: info["HostConfig"].update(PortBindings={"2053/tcp": [{"HostPort": "2053"}]}),
            "forwarding": lambda info: info["HostConfig"]["Sysctls"].update({"net.ipv4.ip_forward": "1"}),
            "dns": lambda info: info["HostConfig"].update(Dns=["8.8.8.8"]),
            "hostname_override": lambda info: info["HostConfig"].update(ExtraHosts=["gateway:host-gateway"]),
            "foreign_kernel_volume": lambda info: info["Mounts"][1].update(Name="production-kernel-data"),
            "nocopy": lambda info: info["HostConfig"]["Mounts"][0]["VolumeOptions"].update(NoCopy=True),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                self.fake.after_create = lambda info, mutate=mutate: mutate(info) if self.fake.role(info) == "app-0" else None
                lab = self.instance()
                self.assertEqual(lab.run(), 1)
                self.assertEqual(lab.state["stages"], {})
                self.assertEqual(self.starts("test-prepare"), [])
                self.assertFalse(self.fake.lookup(lab.ids["proxy"])["State"]["Running"])

    def test_effective_forwarding_or_default_route_prevents_writes(self):
        for name in ("forwarding", "default-route", "wrong-route", "malformed-route"):
            with self.subTest(name=name):
                self.fake.runtime_sysctls.clear()
                self.fake.route_output = None
                if name == "forwarding":
                    self.fake.runtime_sysctls[("app-0", "net.ipv4.ip_forward")] = "1"
                elif name == "default-route":
                    self.fake.route_output = b"Iface Destination Gateway Flags RefCnt Use Metric Mask\neth0 00000000 01011DAC 0003 0 0 0 00000000\n"
                elif name == "wrong-route":
                    self.fake.route_output = b"Iface Destination Gateway Flags RefCnt Use Metric Mask\neth0 0000000A 00000000 0001 0 0 0 000000FF\n"
                else:
                    self.fake.route_output = b"Iface Destination Gateway Flags RefCnt Use Metric Mask\neth0 broken\n"
                lab = self.instance()
                self.assertEqual(lab.run(), 1)
                self.assertFalse(lab.state["stages"])
                self.assertFalse(self.fake.lookup(lab.ids["app-0"])["State"]["Running"])

    def test_wrong_stage_or_missing_success_prevents_recreation(self):
        cases = {
            "wrong-stage": passed("target-rebuilt"),
            "prefix-stage": passed("prepare-extra"),
            "no-marker": b"--- PASS: TestLive3XUIRealUpgrade (0.01s)\nPASS\n",
            "wrong-test": b"real_upgrade_stage_verified=prepare\n--- PASS: TestOther (0.01s)\nPASS\n",
            "no-final-pass": passed("prepare").removesuffix(b"PASS\n"),
            "skipped": b"    --- SKIP: TestLive3XUIRealUpgrade/data (0.01s)\n" + passed("prepare"),
            "failed": b"    --- FAIL: TestLive3XUIRealUpgrade/data (0.01s)\n" + passed("prepare"),
        }
        for name, output in cases.items():
            with self.subTest(name=name):
                self.fake.stage_outputs["prepare"] = output
                lab = self.instance()
                self.assertEqual(lab.run(), 1)
                self.assertEqual(lab.state["stages"]["prepare"]["result"], "FAIL")
                self.assertNotIn("app-1", lab.ids)
                self.assertEqual(len([cmd for cmd in self.starts("test-prepare") if cmd[-1] == lab.ids["test-prepare"]]), 1)

    def test_nonzero_test_exit_cannot_pass_green_logs(self):
        self.fake.stage_exits["prepare"] = 1
        lab = self.instance()
        self.assertEqual(lab.run(), 1)
        self.assertEqual(lab.state["stages"]["prepare"]["exit_code"], 1)
        self.assertNotIn("app-1", lab.ids)

    def test_test_filesystem_or_mount_changes_prevent_first_start(self):
        mutations = {
            "writable-root": lambda info: info["HostConfig"].update(ReadonlyRootfs=False),
            "extra-tmpfs": lambda info: info["HostConfig"]["Tmpfs"].update({"/other": "rw"}),
            "writable-binary": lambda info: info["Mounts"][1].update(RW=True),
            "writable-token": lambda info: info["Mounts"][2].update(RW=True),
            "whole-output-write": lambda info: info["Mounts"][3].update(Source=str(self.root)),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                self.fake.after_create = lambda info, mutate=mutate: mutate(info) if self.fake.role(info) == "test-prepare" else None
                lab = self.instance()
                self.assertEqual(lab.run(), 1)
                self.assertNotIn("prepare", lab.state["stages"])
                self.assertEqual(self.starts("test-prepare"), [])
                self.assertFalse(self.fake.lookup(lab.ids["app-0"])["State"]["Running"])

    def test_proxy_third_network_or_effective_forwarding_is_rejected(self):
        lab, _ = self.prepared()
        original = deepcopy(self.fake.lookup(lab.ids["proxy"]))
        info = self.fake.lookup(lab.ids["proxy"])
        info["NetworkSettings"]["Networks"]["foreign-egress"] = {"NetworkID": "f" * 64}
        with self.assertRaisesRegex(RuntimeError, "unexpected network"):
            lab.runtime_network_guard("proxy")
        self.fake.containers[lab.ids["proxy"]] = original
        self.fake.runtime_sysctls[("proxy", "net.ipv4.ip_forward")] = "1"
        with self.assertRaisesRegex(RuntimeError, "Effective network sysctl"):
            lab.runtime_network_guard("proxy")
        lab.cleanup()

    def test_observation_timeouts_poll_the_same_handle_without_restart(self):
        self.fake.wait_actions["prepare"] = ["timeout", "nonzero", "timeout", "complete"]
        lab = self.instance()
        self.assertEqual(lab.run(), 0)
        starts = self.starts("test-prepare")
        self.assertEqual(len(starts), 1)
        waits = [cmd for cmd in self.commands("container", "wait") if cmd[-1] == lab.ids["test-prepare"]]
        self.assertEqual(len(waits), 4)
        self.assertEqual(lab.state["stages"]["prepare"]["result"], "PASS")

    def test_lost_detached_start_response_observes_existing_handle(self):
        self.fake.lost_start_responses.add("test-prepare")
        lab = self.instance()
        self.assertEqual(lab.run(), 0)
        self.assertTrue(lab.state["stages"]["prepare"]["start_response_unknown"])
        self.assertEqual(lab.state["stages"]["prepare"]["result"], "PASS")
        self.assertEqual(len(self.starts("test-prepare")), 1)
        self.assertEqual(len([cmd for cmd in self.commands("container", "wait") if cmd[-1] == lab.ids["test-prepare"]]), 1)

    def test_transient_inspection_failures_do_not_end_or_restart_stage(self):
        self.fake.inspect_failures["test-prepare"] = 2
        lab = self.instance()
        self.assertEqual(lab.run(), 0)
        self.assertEqual(self.fake.inspect_failures["test-prepare"], 0)
        self.assertEqual(len(self.starts("test-prepare")), 1)
        self.assertEqual(lab.state["stages"]["prepare"]["result"], "PASS")
        self.assertGreaterEqual(self.fake.clock, 2)

    def test_exhausted_observation_is_unknown_and_cleanup_stops_test_then_app_then_proxy(self):
        self.fake.wait_actions["prepare"] = ["timeout"] * 20
        lab = self.instance()
        self.assertEqual(lab.run(), 1)
        self.assertTrue(lab.state["stages"]["prepare"]["result"].startswith("UNKNOWN"))
        self.assertNotIn("app-1", lab.ids)
        self.assertEqual(len(self.starts("test-prepare")), 1)
        stopped_roles = [self.fake.role(self.fake.lookup(cmd[-1])) for cmd in self.commands("container", "stop")]
        self.assertEqual(stopped_roles, ["test-prepare", "app-0", "proxy"])
        self.assertTrue(all(not info["State"]["Running"] for info in self.fake.containers.values()))

    def test_running_predecessor_prevents_recreation(self):
        lab, _ = self.prepared()
        with self.assertRaisesRegex(RuntimeError, "Previous application or test is still running"):
            lab.start_application("app-1")
        self.assertNotIn("app-1", lab.ids)
        self.assertTrue(self.fake.lookup(lab.ids["app-0"])["State"]["Running"])
        lab.cleanup()

    def test_test_bound_to_old_application_namespace_is_rejected(self):
        lab, url = self.prepared()
        lab.stop_owned("app-0")
        url = lab.start_application("app-1")

        def tamper(info):
            if self.fake.role(info) == "test-baseline-rebuilt":
                info["HostConfig"]["NetworkMode"] = "container:" + lab.ids["app-0"]

        self.fake.after_create = tamper
        with self.assertRaisesRegex(RuntimeError, "Unsafe container network"):
            lab.stage("baseline-rebuilt", "app-1", url)
        self.assertEqual(self.starts("test-baseline-rebuilt"), [])
        lab.cleanup()

    def test_foreign_owned_container_is_never_stopped(self):
        lab, _ = self.prepared()
        self.fake.lookup(lab.ids["app-0"])["Config"]["Labels"][base.LABEL] = "foreign"
        lab.cleanup()
        self.assertTrue(self.fake.lookup(lab.ids["app-0"])["State"]["Running"])
        self.assertNotIn(lab.ids["app-0"], [cmd[-1] for cmd in self.commands("container", "stop")])

    def test_failed_cleanup_cannot_retain_pass(self):
        self.fake.stubborn_stops.add("proxy")
        lab = self.instance()
        self.assertEqual(lab.run(), 1)
        self.assertEqual(lab.state["result"], "CLEANUP_FAILED")
        self.assertTrue(self.fake.lookup(lab.ids["proxy"])["State"]["Running"])


if __name__ == "__main__":
    unittest.main()
