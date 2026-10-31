import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest


@unittest.skipUnless(shutil.which("golangci-lint") and shutil.which("gofmt"), "golangci-lint and Go are required")
class ReliabilityRulesTests(unittest.TestCase):
    def test_unknown_configuration_option_blocks_build(self):
        with tempfile.TemporaryDirectory(prefix="marina-lint-config-") as directory:
            repo = pathlib.Path(directory)
            source = pathlib.Path(__file__).resolve().parent.parent
            shutil.copyfile(source / "Makefile", repo / "Makefile")
            config = (source / ".golangci.yml").read_text()
            (repo / ".golangci.yml").write_text(config + "\nunexpected-option: true\n")
            result = subprocess.run(["make", "-j4", "build"], cwd=repo, text=True, capture_output=True, timeout=30)
            output = result.stdout + result.stderr
            self.assertNotEqual(result.returncode, 0, output)
            self.assertIn("unexpected-option", output)
            self.assertNotIn("go build -trimpath", output)

    def test_reliability_violations_are_rejected(self):
        cases = {
            "bodyclose": ('"net/http"', 'func Check(url string) (int, error) { response, err := http.Get(url); if err != nil { return 0, err }; return response.StatusCode, nil }'),
            "durationcheck": ('"time"', 'func Check(first, second time.Duration) time.Duration { return first * second }'),
            "errorlint": ('"os"', 'func Check(err error) bool { return err == os.ErrNotExist }'),
            "fatcontext": ('"context"\n"fmt"', 'type key struct{}\n\n// Check exercises context lifetime.\nfunc Check(ctx context.Context) { for range 3 { ctx = context.WithValue(ctx, key{}, true) }; fmt.Println(ctx) }'),
            "gocheckcompilerdirectives": ('', 'func Check() {}\n\n//go:genrate echo fixture'),
            "nilnesserr": ('"io"', 'func Check(reader io.Reader) error { _, first := reader.Read(nil); if first != nil { return first }; _, second := reader.Read(nil); if second != nil { return first }; return second }'),
            "noctx": ('"net/http"', 'func Check(url string) (*http.Request, error) { return http.NewRequest(http.MethodGet, url, nil) }'),
            "nolintlint": ('', '//nolint:revive\nfunc Check(unused int) {}'),
            "nosprintfhostport": ('"fmt"', 'func Check(host string, port int) string { return fmt.Sprintf("http://%s:%d/path", host, port) }'),
            "rowserrcheck": ('"context"\n"database/sql"', 'func Check(ctx context.Context, db *sql.DB) (int, error) { rows, err := db.QueryContext(ctx, "SELECT 1"); if err != nil { return 0, err }; defer func() { _ = rows.Close() }(); count := 0; for rows.Next() { count++ }; return count, nil }'),
            "sqlclosecheck": ('"context"\n"database/sql"', 'func Check(ctx context.Context, db *sql.DB) error { rows, err := db.QueryContext(ctx, "SELECT 1"); if err != nil { return err }; for rows.Next() {}; return rows.Err() }'),
            "errcheck": ('', 'func Check(value any) string { return value.(string) }'),
            "govet": ('"reflect"', 'func Check(first, second error) bool { return reflect.DeepEqual(first, second) }'),
            "bidichk": ('', 'func Check() string { return "start' + '\u202e' + 'end" }'),
        }
        with tempfile.TemporaryDirectory(prefix="marina-lint-rules-") as directory:
            repo = pathlib.Path(directory)
            source = pathlib.Path(__file__).resolve().parent.parent
            shutil.copyfile(source / ".golangci.yml", repo / ".golangci.yml")
            (repo / "go.mod").write_text("module example.invalid/lint-rules\n\ngo 1.27.1\n")
            code = repo / "example.go"
            env = dict(os.environ, GOWORK="off", GOPROXY="off", GOSUMDB="off", GOFLAGS="", GOLANGCI_LINT_CACHE=str(repo / "lint-cache"))
            for linter, (imports, body) in cases.items():
                with self.subTest(linter=linter):
                    content = "// Package example supplies synthetic lint fixtures.\npackage example\n\n"
                    if imports:
                        content += "import (\n" + imports + "\n)\n\n"
                    content += "// Check supplies a synthetic example for reliability analysis.\n" + body + "\n"
                    code.write_text(content)
                    subprocess.run(["gofmt", "-w", str(code)], check=True, capture_output=True, timeout=30)
                    result = subprocess.run(
                        ["golangci-lint", "run", "--allow-serial-runners", "--uniq-by-line=false", "--config", ".golangci.yml", "./..."],
                        cwd=repo, env=env, text=True, capture_output=True, timeout=90,
                    )
                    output = result.stdout + result.stderr
                    self.assertNotEqual(result.returncode, 0, output)
                    self.assertIn("(" + linter + ")", output)


if __name__ == "__main__":
    unittest.main()
