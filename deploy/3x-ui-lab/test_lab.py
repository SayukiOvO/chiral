"""Local safety regressions; no Docker daemon, network, or SSH is used."""

import argparse
from copy import deepcopy
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("chiral_lab_controller", Path(__file__).with_name("lab.py"))
lab = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(lab)

PROJECT = "chiral-3x-ui-lab-unit-test"
VOLUMES = ("core-data", "agent-data", "three-x-ui-data", "three-x-ui-bin", "three-x-ui-cert")


def safe_config():
    common = {
        "cap_drop": ["ALL"],
        "security_opt": ["no-new-privileges:true"],
        "mem_limit": 192 * 1024 * 1024,
        "cpus": 0.25,
        "pids_limit": 128,
    }
    services = {name: deepcopy(common) for name in ("3x-ui", "core", "agent")}
    services["3x-ui"].update(network_mode="none", volumes=[
        {"type": "volume", "source": name, "target": target}
        for name, target in (("three-x-ui-data", "/etc/x-ui"),
                             ("three-x-ui-bin", "/app/bin"),
                             ("three-x-ui-cert", "/root/cert"))
    ])
    services["core"].update(network_mode="service:3x-ui", volumes=[
        {"type": "volume", "source": "core-data", "target": "/var/lib/chiral"},
    ])
    services["agent"].update(network_mode="service:3x-ui", volumes=[
        {"type": "volume", "source": "agent-data", "target": "/var/lib/chiral-agent"},
        {"type": "bind", "source": str(lab.ROOT / "secrets/3x-ui.token"),
         "target": "/run/secrets/chiral-3x-ui-token", "read_only": True,
         "bind": {"create_host_path": False}},
    ])
    return {"name": PROJECT, "services": services,
            "volumes": {name: {"name": PROJECT + "_" + name} for name in VOLUMES}}


class NoExternalCommands(unittest.TestCase):
    def setUp(self):
        # Any missing mock must fail before a process could contact Docker or a
        # remote host, even when these tests run on a production Docker host.
        guard = mock.patch.object(lab.subprocess, "run", side_effect=AssertionError("External process forbidden in unit tests"))
        guard.start()
        self.addCleanup(guard.stop)


class ComposeSafetyTests(NoExternalCommands):
    def validate(self, config):
        instance = lab.Lab({"project": PROJECT})
        with mock.patch.object(instance, "compose", return_value=json.dumps(config).encode()) as compose:
            instance.validate_compose()
        compose.assert_called_once_with("config", "--format", "json")

    def test_accepts_loopback_only_project_scoped_config(self):
        self.validate(safe_config())

    def test_accepts_compose_memory_limits_encoded_as_numeric_strings(self):
        config = safe_config()
        for name, limit in (("3x-ui", 384), ("core", 256), ("agent", 192)):
            config["services"][name]["mem_limit"] = str(limit * 1024 * 1024)
        self.validate(config)

    def test_rejects_published_host_ports(self):
        config = safe_config()
        config["services"]["3x-ui"]["ports"] = [{"target": 2053, "published": "2053"}]
        with self.assertRaisesRegex(RuntimeError, "ports"):
            self.validate(config)

    def test_rejects_host_networking_on_every_service(self):
        for name in ("3x-ui", "core", "agent"):
            with self.subTest(service=name):
                config = safe_config()
                config["services"][name]["network_mode"] = "host"
                with self.assertRaisesRegex(RuntimeError, "network mode"):
                    self.validate(config)

    def test_rejects_production_bind_mount(self):
        config = safe_config()
        config["services"]["core"]["volumes"] = [
            {"type": "bind", "source": "/srv/chiral", "target": "/var/lib/chiral", "read_only": True},
        ]
        with self.assertRaisesRegex(RuntimeError, "host mount"):
            self.validate(config)

    def test_rejects_production_named_volume(self):
        config = safe_config()
        config["services"]["agent"]["volumes"][0]["source"] = "agent_chiral-agent-data"
        with self.assertRaisesRegex(RuntimeError, "Foreign volume"):
            self.validate(config)

    def test_rejects_external_volume(self):
        config = safe_config()
        config["volumes"]["core-data"]["external"] = True
        with self.assertRaisesRegex(RuntimeError, "External volumes"):
            self.validate(config)

    def test_rejects_volume_name_alias_to_production(self):
        config = safe_config()
        config["volumes"]["core-data"]["name"] = "chiral_core-data"
        with self.assertRaisesRegex(RuntimeError, "project-scoped"):
            self.validate(config)

    def test_rejects_writable_or_automatically_created_token_mount(self):
        for change in ({"read_only": False}, {"bind": {"create_host_path": True}}):
            with self.subTest(change=change):
                config = safe_config()
                config["services"]["agent"]["volumes"][1].update(change)
                with self.assertRaisesRegex(RuntimeError, "Unsafe token mount"):
                    self.validate(config)


class NetworkInterfaceSafetyTests(NoExternalCommands):
    def loopback(self):
        return {"ifname": "lo", "link_type": "loopback", "operstate": "UNKNOWN",
                "flags": ["LOOPBACK", "UP", "LOWER_UP"],
                "addr_info": [{"family": "inet", "local": "127.0.0.1"},
                              {"family": "inet6", "local": "::1"}]}

    def inactive_sit(self):
        return {"ifname": "sit0", "link_type": "sit", "operstate": "DOWN",
                "flags": ["NOARP"], "addr_info": []}

    def test_accepts_only_active_loopback(self):
        lab.validate_interfaces([self.loopback()])

    def test_accepts_unaddressed_down_kernel_fallback_sit(self):
        lab.validate_interfaces([self.loopback(), self.inactive_sit()])

    def test_requires_active_loopback(self):
        for interfaces in ([], [self.inactive_sit()],
                           [{**self.loopback(), "flags": ["LOOPBACK"]}]):
            with self.subTest(interfaces=interfaces):
                with self.assertRaisesRegex(RuntimeError, "loopback is not up"):
                    lab.validate_interfaces(interfaces)

    def test_rejects_ethernet_even_when_down_and_unaddressed(self):
        ethernet = {"ifname": "eth0", "link_type": "ether", "operstate": "DOWN",
                    "flags": ["BROADCAST", "MULTICAST"], "addr_info": []}
        with self.assertRaisesRegex(RuntimeError, "non-loopback interface"):
            lab.validate_interfaces([self.loopback(), ethernet])

    def test_rejects_active_addressed_or_wrong_type_sit(self):
        changes = [
            {"flags": ["NOARP", "UP"]},
            {"operstate": "UP"},
            {"addr_info": [{"family": "inet", "local": "192.0.2.1"}]},
            {"addr_info": [{"family": "inet6", "local": "2001:db8::1"}]},
            {"link_type": "ether"},
        ]
        for change in changes:
            with self.subTest(change=change):
                interface = {**self.inactive_sit(), **change}
                with self.assertRaisesRegex(RuntimeError, "non-loopback interface"):
                    lab.validate_interfaces([self.loopback(), interface])


class FreshProjectTests(NoExternalCommands):
    def docker_listing(self, all_volumes=b"", labeled_volumes=b"", containers=b""):
        responses = {
            ("docker", "container", "ls", "--all", "--filter", "label=com.docker.compose.project=" + PROJECT,
             "--format", "{{.Names}}"): containers,
            ("docker", "volume", "ls", "--format", "{{.Name}}"): all_volumes,
            ("docker", "volume", "ls", "--filter", "label=com.docker.compose.project=" + PROJECT,
             "--format", "{{.Name}}"): labeled_volumes,
        }

        def fake_run(args, **kwargs):
            if args[:3] == ["docker", "image", "inspect"]:
                return b"[]"
            self.assertIn(tuple(args), responses, "Unexpected Docker operation")
            return responses[tuple(args)]

        return mock.patch.object(lab, "run", side_effect=fake_run)

    def test_accepts_fresh_project_among_unrelated_resources(self):
        volumes = ("production_core-data\n" + PROJECT + "_core-data-old\n").encode()
        with self.docker_listing(all_volumes=volumes):
            lab.require_fresh_project(PROJECT)

    def test_rejects_each_same_name_volume_without_labels(self):
        for name in VOLUMES:
            with self.subTest(volume=name), self.docker_listing(all_volumes=(PROJECT + "_" + name + "\n").encode()):
                with self.assertRaises(RuntimeError):
                    lab.require_fresh_project(PROJECT)

    def test_rejects_resources_labeled_for_this_project(self):
        for existing in ({"labeled_volumes": b"renamed-laboratory-volume\n"}, {"containers": b"existing-laboratory-core\n"}):
            with self.subTest(existing=existing), self.docker_listing(**existing):
                with self.assertRaises(RuntimeError):
                    lab.require_fresh_project(PROJECT)

    def test_initialize_rejects_unlabeled_volume_before_any_write(self):
        image = "example.invalid/lab@sha256:" + "a" * 64
        args = argparse.Namespace(project=PROJECT, three_x_ui_image=image, core_base_image=image, agent_base_image=image)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with mock.patch.object(lab, "ROOT", root), \
                 mock.patch.object(lab, "STATE", root / "artifacts/state.json"), \
                 self.docker_listing(all_volumes=(PROJECT + "_agent-data\n").encode()), \
                 mock.patch.object(lab, "save") as save, \
                 mock.patch.object(lab.Lab, "compose") as compose:
                with self.assertRaisesRegex(RuntimeError, "[Vv]olume"):
                    lab.initialize(args)
                save.assert_not_called()
                compose.assert_not_called()
                self.assertEqual(list(root.iterdir()), [])


class TokenFaultRecoveryTests(NoExternalCommands):
    def test_restores_observer_token_when_revocation_response_is_lost(self):
        for error in (RuntimeError("revocation response lost"), subprocess.TimeoutExpired("simulated request", 8)):
            with self.subTest(error=type(error).__name__):
                instance = lab.Lab({"project": PROJECT, "base_path": "test-only", "observer_token_id": 12})
                ready = {"runtime_provider": "3x-ui", "runtime_mode": "SHADOW", "runtime_health": "READY",
                         "runtime_capabilities": [str(i) for i in range(16)]}
                endpoint = "/panel/api/setting/apiTokens/setEnabled/12"

                def request(path, method="GET", payload=None, provider="core"):
                    if path == endpoint and payload["enabled"] is False:
                        raise error
                    return {"inbounds": []} if "getConfigJson" in path else "ok"

                with mock.patch.object(instance, "verify_isolation", return_value={}), \
                     mock.patch.object(instance, "pid", return_value=12345), \
                     mock.patch.object(instance, "wait", return_value=ready), \
                     mock.patch.object(instance, "request", side_effect=request) as calls, \
                     mock.patch.object(Path, "is_file", return_value=True), \
                     mock.patch.object(lab, "run", return_value=b"--- PASS: TestLive3XUIReadOnlyContract\n"), \
                     mock.patch.object(lab, "save"), mock.patch("builtins.print"):
                    with self.assertRaises(type(error)):
                        instance.verify()
                self.assertEqual(calls.call_args_list[-2:], [
                    mock.call(endpoint, "POST", {"enabled": False, "expectedScope": "admin"}, "3x-ui"),
                    mock.call(endpoint, "POST", {"enabled": True, "expectedScope": "admin"}, "3x-ui"),
                ])


if __name__ == "__main__":
    unittest.main()
