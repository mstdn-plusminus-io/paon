"""Private parity harness lifecycle regressions; no Docker or dump required."""
import sys

sys.dont_write_bytecode = True

import argparse
import contextlib
import hashlib
import io
import json
from pathlib import Path
import runpy
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[2] / "bin/test-migration-parity"


class ParityRunnerTest(unittest.TestCase):
    def setUp(self):
        self.runner = runpy.run_path(str(SCRIPT))
        self.globals = self.runner["main"].__globals__
        temporary = tempfile.TemporaryDirectory(prefix="paon-parity-runner-test-")
        self.addCleanup(temporary.cleanup)
        self.output = Path(temporary.name)

    def report(self):
        return json.loads((self.output / "result.json").read_text())

    def test_missing_dump_invalidates_an_earlier_pass(self):
        (self.output / "result.json").write_text(json.dumps({
            "status": "passed", "passed": True, "run_id": "previous",
            "versions": {"old": {"passed": True}},
        }))
        arguments = [str(SCRIPT), "--output", str(self.output), "--dump", str(self.output / "missing.dump")]
        with patch.object(sys, "argv", arguments), patch("subprocess.run", side_effect=AssertionError("preflight must not launch a process")):
            with self.assertRaisesRegex(RuntimeError, "staging dump is missing"):
                self.runner["main"]()
        report = self.report()
        self.assertEqual(report["status"], "failed")
        self.assertFalse(report["passed"])
        self.assertEqual(report["versions"], {})
        self.assertNotEqual(report["run_id"], "previous")
        self.assertEqual((self.output / "result.json").stat().st_mode & 0o777, 0o600)
        self.assertEqual(list(self.output.glob(".result-*")), [])

    def test_cleanup_failure_cannot_publish_success(self):
        def completed_gate(args, manifest, report):
            report["versions"]["4.6.6"] = {"passed": True}
            self.runner["cleanup_containers"](["owned-pg"])

        results = [subprocess.CompletedProcess([], 0, "", ""), subprocess.CompletedProcess([], 1, "", "removal failed")]
        output = io.StringIO()
        with patch.dict(self.globals, {"run_gate": completed_gate}), patch.object(sys, "argv", [str(SCRIPT), "--output", str(self.output)]), patch("subprocess.run", side_effect=results), contextlib.redirect_stdout(output):
            with self.assertRaisesRegex(RuntimeError, "owned-pg"):
                self.runner["main"]()
        self.assertEqual(self.report()["status"], "failed")
        self.assertFalse(self.report()["passed"])
        self.assertNotIn("release gates passed", output.getvalue())

    def test_partial_container_startup_removes_its_anonymous_volumes(self):
        fixture = self.output / "synthetic.dump"
        fixture.write_bytes(b"synthetic fixture; never restored")
        manifest = {
            "dump_sha256": hashlib.sha256(fixture.read_bytes()).hexdigest(),
            "versions": {"4.3.23": {"commit": "reviewed-commit", "image": "synthetic-image"}},
        }
        args = argparse.Namespace(versions="4.3.23", dump=fixture, worktree=[], go="go", mastodon=self.output, output=self.output)
        started = []

        def command(args, **kwargs):
            if args[0] == "git":
                return "reviewed-commit" if "rev-parse" in args else ""
            if "--entrypoint" in args:
                return ""  # No migration files in this synthetic preflight.
            if args[:2] == ["docker", "run"] and "--name" in args:
                started.append(args[args.index("--name") + 1])
                raise RuntimeError("startup failed after container creation")
            self.fail(f"unexpected external command: {args[0]}")

        with patch.dict(self.globals, {"run": command}), patch("shutil.which", return_value="/synthetic/tool"), patch("subprocess.check_output", side_effect=AssertionError("no real process allowed")), patch("subprocess.run", return_value=subprocess.CompletedProcess([], 0, "", "")) as cleanup:
            with self.assertRaisesRegex(RuntimeError, "startup failed"):
                self.runner["run_gate"](args, manifest, {"versions": {}})
        self.assertEqual(len(started), 1)
        self.assertEqual([call.args[0] for call in cleanup.call_args_list], [
            ["docker", "container", "inspect", started[0]],
            ["docker", "rm", "-fv", started[0]],
        ])

    def test_missing_container_is_safe_but_inspection_failure_is_not(self):
        for stderr, must_fail in [("Error: No such container: owned-pg", False), ("Cannot connect to the Docker daemon", True)]:
            with self.subTest(stderr=stderr), patch("subprocess.run", return_value=subprocess.CompletedProcess([], 1, "", stderr)) as command:
                if must_fail:
                    with self.assertRaisesRegex(RuntimeError, "owned-pg"):
                        self.runner["cleanup_containers"](["owned-pg"])
                else:
                    self.runner["cleanup_containers"](["owned-pg"])
                self.assertEqual(command.call_count, 1)


if __name__ == "__main__":
    unittest.main()
