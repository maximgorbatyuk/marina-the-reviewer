import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest


class LintGateTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.repo = pathlib.Path(temporary.name)
        source = pathlib.Path(__file__).resolve().parent.parent
        shutil.copyfile(source / "Makefile", self.repo / "Makefile")
        (self.repo / "VERSION").write_text("0.0.0\n")
        (self.repo / "scripts").mkdir()
        self.log = self.repo / "commands.log"
        tools = self.repo / "tools"
        tools.mkdir()
        for name, status in (("go", "0"), ("gofmt", "0"), ("golangci-lint", '${LINT_TEST_STATUS:-0}')):
            path = tools / name
            config_check = 'if [ "$1 $2" = "config verify" ]; then exit "${LINT_CONFIG_TEST_STATUS:-0}"; fi\n' if name == "golangci-lint" else ""
            path.write_text(f'#!/bin/sh\nprintf "%s\\n" "{name} $*" >> "$LINT_TEST_LOG"\n{config_check}exit {status}\n')
            path.chmod(0o755)
        self.env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"], LINT_TEST_LOG=str(self.log))

    def make(self, target, *args, status="0", config_status="0"):
        return subprocess.run(
            ["make", "-j4", *args, target],
            cwd=self.repo,
            env=dict(self.env, LINT_TEST_STATUS=status, LINT_CONFIG_TEST_STATUS=config_status),
            text=True,
            capture_output=True,
            timeout=30,
        )

    def commands(self):
        return self.log.read_text().splitlines() if self.log.exists() else []

    def test_lint_failure_blocks_build_and_ci_verification(self):
        for target in ("build", "verify"):
            with self.subTest(target=target):
                self.log.unlink(missing_ok=True)
                result = self.make(target, status="1")
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue(any(line.startswith("golangci-lint run ") for line in self.commands()))
                self.assertFalse(any(line.startswith("go ") for line in self.commands()), self.commands())

    def test_successful_lint_allows_build(self):
        result = self.make("build")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        commands = self.commands()
        self.assertTrue(commands[0].startswith("golangci-lint config verify "), commands)
        self.assertTrue(commands[1].startswith("golangci-lint run "), commands)
        self.assertTrue(any(line.startswith("go build ") for line in commands), commands)

    def test_missing_linter_blocks_build(self):
        result = self.make("build", "GOLANGCI_LINT=./missing-linter")
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(any(line.startswith("go build ") for line in self.commands()), self.commands())

    def test_invalid_linter_configuration_blocks_build_and_ci(self):
        for target in ("build", "verify"):
            with self.subTest(target=target):
                self.log.unlink(missing_ok=True)
                result = self.make(target, config_status="1")
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertFalse(any(line.startswith(("go ", "golangci-lint run ")) for line in self.commands()), self.commands())


if __name__ == "__main__":
    unittest.main()
