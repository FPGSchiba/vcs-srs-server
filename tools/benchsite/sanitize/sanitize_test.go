package sanitize

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func fixtures(t *testing.T, kind string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join("testdata", kind, "*"))
	if err != nil || len(m) == 0 {
		t.Fatalf("no %s fixtures: %v", kind, err)
	}
	return m
}

func scanOne(t *testing.T, file string, extra []string) []Finding {
	t.Helper()
	dir := t.TempDir()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(file)), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return Scan(dir, extra)
}

func TestFailFixturesEachFail(t *testing.T) {
	for _, f := range fixtures(t, "fail") {
		if len(scanOne(t, f, nil)) == 0 {
			t.Errorf("%s was not rejected", f)
		}
	}
}

func TestPassFixturesEachPass(t *testing.T) {
	for _, f := range fixtures(t, "pass") {
		if got := scanOne(t, f, nil); len(got) != 0 {
			t.Errorf("%s rejected: %v", f, got)
		}
	}
}

func TestExtraLiteralRejected(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("built on buildbox-17\n"), 0o644)
	if len(Scan(dir, []string{"buildbox-17"})) != 1 {
		t.Fatal("hostname extra not flagged")
	}
	if len(Scan(dir, []string{""})) != 0 {
		t.Fatal("empty extra must be ignored")
	}
}

func TestFindingDoesNotEchoExtra(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("tok s3cr3t-value\n"), 0o644)
	for _, f := range Scan(dir, []string{"s3cr3t-value"}) {
		if bytes.Contains([]byte(f.String()), []byte("s3cr3t-value")) {
			t.Fatalf("finding leaks secret: %s", f)
		}
	}
}

func TestSharedFixturesMatchShellCopies(t *testing.T) {
	for _, kind := range []string{"fail", "pass"} {
		for _, f := range fixtures(t, kind) {
			other := filepath.Join("..", "..", "..", ".github", "scripts", "testdata", "bench-scan", kind, filepath.Base(f))
			a, _ := os.ReadFile(f)
			b, err := os.ReadFile(other)
			if err != nil || !bytes.Equal(bytes.ReplaceAll(a, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))) {
				t.Errorf("shell fixture copy differs: %s", other)
			}
		}
	}
}

func TestRunOKPublishSetClean(t *testing.T) {
	if got := Scan(filepath.Join("..", "schema", "testdata", "run-ok"), nil); len(got) != 0 {
		t.Fatalf("run-ok flagged: %v", got)
	}
}
