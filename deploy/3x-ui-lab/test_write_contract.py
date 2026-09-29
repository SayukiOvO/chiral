"""Runner safety checks with a simulated Docker daemon; no external processes."""

from copy import deepcopy
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("chiral_write_contract", Path(__file__).with_name("write_contract.py"))
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)
IMAGE = "ghcr.io/mhsanaei/3x-ui@sha256:" + "a" * 64
IMAGE_ID = "sha256:" + "b" * 64
SOCKET = "unix:///test-only/docker.sock"
TOKEN = "test-bootstrap-secret-token"
PASS_LOG = b"--- PASS: TestLive3XUIWriteContract (0.01s)\nPASS\n"


def flag(args, name):
    return args[args.index(name) + 1]


def flags(args, name):
    return [args[i + 1] for i, value in enumerate(args[:-1]) if value == name]


class FakeDocker:
    def __init__(self):
        self.calls = []
        self.volumes = {}
        self.containers = {}
        self.test_output = PASS_LOG
        self.test_exit = 0
        self.readiness_failures = 0
        self.timeout_test = False
        self.client_access_exit = 0
        self.containerd_images = False
        self.operating_system = "Test Linux"
        self.desktop_bind_mounts = False
        self.after_create = None
        self.colliding_volume = None
        self.image = {"Id": IMAGE_ID, "Os": "linux", "Architecture": "amd64", "RepoDigests": [IMAGE],
                      "Config": {"Volumes": {"/etc/x-ui": {}}}}

    def response(self, args, value=b"", code=0):
        if not isinstance(value, bytes):
            value = json.dumps(value).encode()
        return subprocess.CompletedProcess(args, code, value)

    def lookup(self, target):
        if target in self.containers:
            return self.containers[target]
        return next(value for value in self.containers.values() if value["Name"] == "/" + target)

    def __call__(self, args, data=None, timeout=60):
        self.calls.append((args, data, timeout))
        if args[:3] == ["docker", "context", "inspect"]:
            return self.response(args, [{"Endpoints": {"docker": {"Host": SOCKET}}}])
        assert args[:3] == ["docker", "--host", SOCKET], args
        command = args[3:]
        kind, operation = command[:2]
        if kind == "info":
            assert command == ["info", "--format", "{{json .OperatingSystem}}"]
            return self.response(args, self.operating_system)
        if kind == "image" and operation == "inspect":
            if "--platform" not in command:
                return self.response(args, [{"Id": "sha256:" + "a" * 64, "RepoDigests": [IMAGE]}])
            assert flag(command, "--platform") == "linux/amd64"
            return self.response(args, [self.image])
        if operation == "ls":
            if "--filter" in command:
                return self.response(args)
            names = ["production-agent"] if kind == "container" else ["agent_chiral-agent-data"]
            if self.colliding_volume and kind == "volume":
                names.append(self.colliding_volume)
            return self.response(args, ("\n".join(names) + "\n").encode())
        if kind == "volume":
            if operation == "create":
                name = command[-1]
                self.volumes[name] = {"Name": name, "Driver": "local", "Options": {},
                                      "Labels": dict(item.split("=", 1) for item in flags(command, "--label"))}
                return self.response(args, (name + "\n").encode())
            if operation == "inspect":
                return self.response(args, [self.volumes[command[-1]]])
        if kind == "container" and operation == "create":
            identity = f"{len(self.containers) + 1:064x}"
            labels = dict(item.split("=", 1) for item in flags(command, "--label"))
            mounts = []
            for value in flags(command, "--mount"):
                parts = value.split(",")
                options = dict(item.split("=", 1) for item in parts if "=" in item)
                mount = {"Type": options["type"], "Destination": options["target"], "RW": "readonly" not in parts}
                source = options["source"]
                if options["type"] == "bind" and self.desktop_bind_mounts:
                    source = "/host_mnt" + source
                mount["Name" if options["type"] == "volume" else "Source"] = source
                mounts.append(mount)
            info = {"Id": identity, "Name": "/" + flag(command, "--name"), "Image": IMAGE_ID,
                    "Config": {"Labels": labels, "Entrypoint": [flag(command, "--entrypoint")],
                               "Cmd": command[command.index(IMAGE) + 1:], "Env": flags(command, "--env"),
                               "User": flag(command, "--user")},
                    "HostConfig": {"NetworkMode": flag(command, "--network"), "PortBindings": {},
                                   "CapDrop": flags(command, "--cap-drop"), "SecurityOpt": flags(command, "--security-opt"),
                                   "Memory": runner.LIMIT, "MemorySwap": runner.LIMIT,
                                   "NanoCpus": 500000000, "PidsLimit": 128, "IpcMode": flag(command, "--ipc"),
                                   "RestartPolicy": {"Name": flag(command, "--restart")}},
                    "Mounts": mounts, "State": {"Running": False, "ExitCode": 0}}
            if self.containerd_images:
                info["Image"] = IMAGE.rsplit("@", 1)[1]
                info["Config"]["Image"] = IMAGE
                info["ImageManifestDescriptor"] = {
                    "mediaType": "application/vnd.oci.image.manifest.v1+json",
                    "digest": IMAGE_ID, "platform": {"os": "linux", "architecture": "amd64"},
                }
            self.containers[identity] = info
            if self.after_create:
                self.after_create(info)
            return self.response(args, (identity + "\n").encode())
        if kind == "container" and operation == "inspect":
            return self.response(args, [self.lookup(command[-1])])
        if kind == "container" and operation == "start":
            info = self.lookup(command[-1])
            role = info["Config"]["Labels"][runner.ROLE]
            if role == "bootstrap":
                assert data and data.endswith(b"\n")
                return self.response(args, ("apiToken: " + TOKEN + "\n").encode())
            if role == "app":
                info["State"]["Running"] = True
                return self.response(args)
            if role == "test":
                if self.timeout_test:
                    info["State"]["Running"] = True
                    raise subprocess.TimeoutExpired(args, timeout)
                info["State"]["ExitCode"] = self.test_exit
                return self.response(args, self.test_output, self.test_exit)
        if kind == "container" and operation == "exec":
            if command[2] == "--user":
                assert command[3] == f"{os.geteuid()}:{os.getegid()}"
                assert command[5:7] == ["/bin/sh", "-c"]
                assert command[-1] == "test -x /app/bin/xray-linux-amd64 && test -r /app/bin/geoip.dat && test -r /app/bin/geosite.dat"
                return self.response(args, code=self.client_access_exit)
            assert command[3:7] == ["wget", "--spider", "-q", "-T"], command
            assert command[-1].startswith("http://127.0.0.1:2053/")
            if self.readiness_failures:
                self.readiness_failures -= 1
                return self.response(args, code=1)
            return self.response(args)
        if kind == "container" and operation == "stop":
            self.lookup(command[-1])["State"]["Running"] = False
            return self.response(args)
        if kind == "container" and operation == "logs":
            role = self.lookup(command[-1])["Config"]["Labels"][runner.ROLE]
            return self.response(args, self.test_output if role == "test" else b"application diagnostic\n")
        raise AssertionError("Unexpected or destructive Docker operation: " + repr(command))


class WriteContractTests(unittest.TestCase):
    def setUp(self):
        guard = mock.patch.object(runner.subprocess, "run", side_effect=AssertionError("External processes are forbidden"))
        guard.start()
        self.addCleanup(guard.stop)
        environment = mock.patch.dict(os.environ, {}, clear=True)
        environment.start()
        self.addCleanup(environment.stop)
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.binary = self.root / "contract.test"
        self.binary.write_bytes(b"\x7fELF\x02\x01" + b"\x00" * 12 + b"\x3e\x00")
        self.binary.chmod(0o700)
        self.fake = FakeDocker()
        self.execute = mock.patch.object(runner, "execute", side_effect=self.fake)
        self.execute.start()
        self.addCleanup(self.execute.stop)
        sleep = mock.patch.object(runner.time, "sleep")
        sleep.start()
        self.addCleanup(sleep.stop)
        self.console = io.StringIO()
        stdout = mock.patch.object(runner.sys, "stdout", self.console)
        stdout.start()
        self.addCleanup(stdout.stop)

    def instance(self):
        return runner.WriteContract(IMAGE, str(self.binary), str(self.root / "result"))

    def test_success_is_single_run_isolated_and_retained(self):
        lab = self.instance()
        self.fake.readiness_failures = 2
        self.assertEqual(lab.run(), 0)
        self.assertEqual(lab.state["result"], "PASS")
        self.assertEqual(lab.state["test_exit_code"], 0)
        self.assertEqual(len(self.fake.volumes), 4)
        self.assertEqual(len(self.fake.containers), 3)
        self.assertTrue(all(not info["State"]["Running"] for info in self.fake.containers.values()))
        calls = [args[3:] for args, _, _ in self.fake.calls if args[:3] == ["docker", "--host", SOCKET]]
        starts = [args for args in calls if args[:2] == ["container", "start"] and args[-1] == lab.ids["test"]]
        self.assertEqual(len(starts), 1)
        for args in [args for args in calls if args[:2] == ["container", "create"]]:
            self.assertIn("--pull=never", args)
            self.assertNotIn("--publish", args)
            self.assertEqual(flag(args, "--memory"), "384m")
            self.assertEqual(flag(args, "--cpus"), "0.5")
            self.assertEqual(flag(args, "--pids-limit"), "128")
        test_info = self.fake.lookup(lab.ids["test"])
        self.assertEqual(test_info["HostConfig"]["NetworkMode"], "container:" + lab.ids["app"])
        self.assertEqual(test_info["Config"]["User"], f"{os.geteuid()}:{os.getegid()}")
        self.assertIn("-test.timeout=5m", test_info["Config"]["Cmd"])
        self.assertIn("CHIRAL_3XUI_TEST_XRAY_BIN=/app/bin/xray-linux-amd64", test_info["Config"]["Env"])
        self.assertTrue(all(not mount["RW"] for mount in test_info["Mounts"] if mount["Type"] == "bind"))
        self.assertEqual((lab.output.stat().st_mode & 0o777), 0o700)
        for path in lab.output.iterdir():
            self.assertEqual(path.stat().st_mode & 0o777, 0o600, str(path))
        all_arguments = " ".join(" ".join(args) for args, _, _ in self.fake.calls)
        for secret in (lab.password, TOKEN):
            self.assertNotIn(secret, all_arguments)
            self.assertNotIn(secret, self.console.getvalue())
            self.assertNotIn(secret, (lab.output / "state.json").read_text())
        self.assertEqual((lab.output / "api.token").read_text().strip(), TOKEN)
        self.assertTrue(all(" stop " in command and " rm " not in command for command in lab.state["cleanup_commands"]))

    def test_failure_and_skip_never_count_as_pass_or_retry(self):
        for name, exit_code, output in (("fail", 1, b"--- FAIL: TestLive3XUIWriteContract (0.1s)\nFAIL\n"),
                                       ("skip", 0, b"--- SKIP: TestLive3XUIWriteContract (0.1s)\nPASS\n"),
                                       ("subtest_skip", 0, b"    --- SKIP: TestLive3XUIWriteContract/data (0.1s)\n" + PASS_LOG),
                                       ("no_test", 0, b"testing: warning: no tests to run\nPASS\n"),
                                       ("nonzero_with_pass", 1, PASS_LOG)):
            with self.subTest(name=name):
                lab = runner.WriteContract(IMAGE, str(self.binary), str(self.root / name))
                fake = FakeDocker()
                fake.test_exit, fake.test_output = exit_code, output + ("token=" + TOKEN + "\n").encode()
                with mock.patch.object(runner, "execute", side_effect=fake):
                    self.assertEqual(lab.run(), 1)
                self.assertNotEqual(lab.state["result"], "PASS")
                self.assertEqual(lab.state["test_exit_code"], exit_code)
                self.assertNotIn(TOKEN, self.console.getvalue())
                starts = [args for args, _, _ in fake.calls if args[3:5] == ["container", "start"] and args[-1] == lab.ids["test"]]
                self.assertEqual(len(starts), 1)
                self.assertTrue((lab.output / "contract-test.log").is_file())
                self.assertTrue(all(not info["State"]["Running"] for info in fake.containers.values()))

    def test_timeout_stops_owned_process_and_retains_diagnostics(self):
        lab = self.instance()
        self.fake.timeout_test = True
        self.fake.test_output = ("diagnostic Authorization: Bearer " + TOKEN + "\n").encode()
        self.assertEqual(lab.run(), 1)
        self.assertTrue((lab.output / "contract-test.log").is_file())
        self.assertNotIn(TOKEN, self.console.getvalue())
        self.assertTrue(all(not info["State"]["Running"] for info in self.fake.containers.values()))

    def test_unreadable_client_files_prevent_test_container_and_writes(self):
        lab = self.instance()
        self.fake.client_access_exit = 1
        self.assertEqual(lab.run(), 1)
        self.assertNotIn("test", lab.ids)
        self.assertFalse(self.fake.lookup(lab.ids["app"])["State"]["Running"])

    def test_rejects_existing_output_directory_and_non_elf_before_docker(self):
        (self.root / "result").mkdir()
        with self.assertRaises(RuntimeError):
            self.instance()
        self.binary.write_bytes(b"not a Linux binary")
        with self.assertRaisesRegex(RuntimeError, "Linux/amd64"):
            runner.WriteContract(IMAGE, str(self.binary), str(self.root / "new"))
        self.assertEqual(self.fake.calls, [])

    def test_rejects_tag_and_remote_docker_endpoint(self):
        with self.assertRaisesRegex(RuntimeError, "digest"):
            runner.WriteContract("ghcr.io/mhsanaei/3x-ui:latest", str(self.binary), str(self.root / "new"))
        with mock.patch.dict(os.environ, {"DOCKER_HOST": "ssh://turin"}):
            with self.assertRaisesRegex(RuntimeError, "local Docker"):
                runner.local_docker_endpoint()
        self.assertEqual(self.fake.calls, [])
        with mock.patch.object(runner, "execute", return_value=subprocess.CompletedProcess([], 0,
                json.dumps([{"Endpoints": {"docker": {"Host": "tcp://remote:2375"}}}]).encode())):
            with self.assertRaisesRegex(RuntimeError, "Remote Docker"):
                runner.local_docker_endpoint()

    def test_rejects_colliding_unlabelled_volume_before_any_creation(self):
        lab = self.instance()
        self.fake.colliding_volume = lab.volumes["data"]
        self.assertEqual(lab.run(), 1)
        self.assertEqual(self.fake.volumes, {})
        self.assertEqual(self.fake.containers, {})

    def test_rejects_implicit_anonymous_image_volume(self):
        lab = self.instance()
        self.fake.image["Config"]["Volumes"]["/production"] = {}
        self.assertEqual(lab.run(), 1)
        self.assertEqual(self.fake.volumes, {})

    def test_manifest_list_inspection_selects_runnable_amd64_image(self):
        lab = self.instance()
        lab.preflight()
        calls = [args for args, _, _ in self.fake.calls if args[3:5] == ["image", "inspect"]]
        self.assertEqual(calls, [["docker", "--host", SOCKET, "image", "inspect",
                                  "--platform", "linux/amd64", IMAGE]])
        self.assertEqual(lab.image_id, IMAGE_ID)
        self.assertNotEqual(lab.image_id, "sha256:" + "a" * 64)

    def test_containerd_index_requires_selected_manifest_and_platform(self):
        self.fake.containerd_images = True
        lab = self.instance()
        self.assertEqual(lab.run(), 0)
        self.assertEqual(lab.state["result"], "PASS")
        self.assertEqual(self.fake.lookup(lab.ids["app"])["Image"], IMAGE.rsplit("@", 1)[1])
        original = deepcopy(self.fake.lookup(lab.ids["app"]))
        changes = [
            ("descriptor_missing", lambda info: info.pop("ImageManifestDescriptor")),
            ("descriptor_mismatch", lambda info: info["ImageManifestDescriptor"].update(digest="sha256:" + "f" * 64)),
            ("wrong_arch", lambda info: info["ImageManifestDescriptor"]["platform"].update(architecture="arm64")),
            ("wrong_os", lambda info: info["ImageManifestDescriptor"]["platform"].update(os="windows")),
            ("reference_mismatch", lambda info: info["Config"].update(Image="ghcr.io/mhsanaei/3x-ui:latest")),
            ("index_mismatch", lambda info: info.update(Image="sha256:" + "f" * 64)),
        ]
        for name, mutate in changes:
            with self.subTest(name=name):
                info = deepcopy(original)
                mutate(info)
                self.fake.containers[lab.ids["app"]] = info
                with self.assertRaisesRegex(RuntimeError, "unexpected image"):
                    lab.inspect_owned("app")

    def test_desktop_accepts_only_exact_host_mnt_translation(self):
        self.fake.operating_system = "Docker Desktop"
        self.fake.desktop_bind_mounts = True
        lab = self.instance()
        self.assertEqual(lab.run(), 0)
        self.assertTrue(lab.state["docker_desktop"])
        original = deepcopy(self.fake.lookup(lab.ids["test"]))
        source = str(lab.binary)
        for bad_source in ("/wrong/host_mnt" + source, "/host_mnt" + source + "-foreign",
                           "/host_mnt/another-directory/" + lab.binary.name, source + "-foreign"):
            with self.subTest(source=bad_source):
                info = deepcopy(original)
                binary_mount = next(mount for mount in info["Mounts"]
                                    if mount["Destination"] == "/run/chiral-write-contract.test")
                binary_mount["Source"] = bad_source
                self.fake.containers[lab.ids["test"]] = info
                with self.assertRaisesRegex(RuntimeError, "Foreign or writable"):
                    lab.verify_container("test")

    def test_native_daemon_rejects_desktop_translation(self):
        self.fake.desktop_bind_mounts = True
        lab = self.instance()
        self.assertEqual(lab.run(), 1)
        self.assertFalse(lab.state["docker_desktop"])
        starts = [args for args, _, _ in self.fake.calls if args[3:5] == ["container", "start"] and args[-1] == lab.ids["test"]]
        self.assertEqual(starts, [])

    def prepared(self):
        lab = self.instance()
        lab.preflight()
        lab.create_volumes()
        lab.bootstrap()
        lab.start_app()
        return lab

    def test_rejects_unsafe_effective_metadata(self):
        lab = self.prepared()
        original = deepcopy(self.fake.lookup(lab.ids["app"]))
        changes = [
            ("host_network", lambda info: info["HostConfig"].update(NetworkMode="host")),
            ("port", lambda info: info["HostConfig"].update(PortBindings={"2053/tcp": [{}]})),
            ("privilege", lambda info: info["HostConfig"].update(Privileged=True)),
            ("foreign_mount", lambda info: info["Mounts"][0].update(Name="agent_chiral-agent-data")),
            ("foreign_label", lambda info: info["Config"]["Labels"].update({runner.LABEL: "production"})),
            ("wrong_image", lambda info: info.update(Image="sha256:" + "f" * 64)),
            ("resource_limit", lambda info: info["HostConfig"].update(Memory=0)),
            ("wrong_executable", lambda info: info["Config"].update(Entrypoint=["/bin/sh"])),
        ]
        for name, mutate in changes:
            with self.subTest(name=name):
                info = deepcopy(original)
                mutate(info)
                self.fake.containers[lab.ids["app"]] = info
                with self.assertRaises(RuntimeError):
                    lab.verify_container("app")
        self.fake.containers[lab.ids["app"]] = original

    def test_foreign_owner_is_never_stopped_or_offered_cleanup_command(self):
        lab = self.prepared()
        self.fake.lookup(lab.ids["app"])["Config"]["Labels"][runner.LABEL] = "production"
        lab.cleanup()
        stops = [args[-1] for args, _, _ in self.fake.calls if args[3:5] == ["container", "stop"]]
        self.assertNotIn(lab.ids["app"], stops)
        self.assertTrue(self.fake.lookup(lab.ids["app"])["State"]["Running"])
        self.assertTrue(all(lab.ids["app"] not in command for command in lab.state["cleanup_commands"]))

    def test_failed_test_metadata_prevents_writes_but_stops_owned_app(self):
        lab = self.instance()

        def tamper(info):
            if info["Config"]["Labels"][runner.ROLE] == "test":
                info["Mounts"][1]["RW"] = True

        self.fake.after_create = tamper
        self.assertEqual(lab.run(), 1)
        starts = [args for args, _, _ in self.fake.calls if args[3:5] == ["container", "start"] and args[-1] == lab.ids["test"]]
        self.assertEqual(starts, [])
        self.assertFalse(self.fake.lookup(lab.ids["app"])["State"]["Running"])


if __name__ == "__main__":
    unittest.main()
