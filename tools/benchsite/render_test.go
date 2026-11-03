package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/tools/benchsite/sanitize"
	"github.com/FPGSchiba/vcs-srs-server/tools/benchsite/schema"
)

func renderFixture(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	if err := Render("testdata/runs", out); err != nil {
		t.Fatalf("render: %v", err)
	}
	return out
}

func pages(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		m[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestGolden(t *testing.T) {
	got := pages(t, renderFixture(t))
	want := []string{"index.html", "runs/2026-10-20-111/index.html", "runs/2026-10-27-222/index.html"}
	if len(got) != len(want) {
		t.Fatalf("pages = %d, want %d", len(got), len(want))
	}
	for _, name := range want {
		gp := filepath.Join("testdata", "golden", filepath.FromSlash(name))
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			os.MkdirAll(filepath.Dir(gp), 0o755)
			if err := os.WriteFile(gp, []byte(got[name]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		b, err := os.ReadFile(gp)
		if err != nil {
			t.Fatalf("golden %s: %v", name, err)
		}
		if !bytes.Equal(b, []byte(got[name])) {
			t.Errorf("%s differs from golden (UPDATE_GOLDEN=1 to refresh)", name)
		}
	}
}

func TestDeterministic(t *testing.T) {
	a, b := pages(t, renderFixture(t)), pages(t, renderFixture(t))
	for k := range a {
		if a[k] != b[k] {
			t.Errorf("%s not byte-stable", k)
		}
	}
}

func TestEveryPageHasLabel(t *testing.T) {
	for name, body := range pages(t, renderFixture(t)) {
		if !strings.Contains(body, schema.Label) {
			t.Errorf("%s lacks the label", name)
		}
	}
}

func TestNoExternalResources(t *testing.T) {
	for name, body := range pages(t, renderFixture(t)) {
		for _, bad := range []string{"<script", "src=\"http", "href=\"http://", "@import", "<link", "url(http"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
	}
}

func TestSiteScanClean(t *testing.T) {
	if f := sanitize.Scan(renderFixture(t), nil); len(f) != 0 {
		t.Fatalf("scan findings: %v", f)
	}
}

func TestIndexNewestFirst(t *testing.T) {
	idx := pages(t, renderFixture(t))["index.html"]
	a, b := strings.Index(idx, "2026-10-27-222"), strings.Index(idx, "2026-10-20-111")
	if a < 0 || b < 0 || a > b {
		t.Fatalf("order wrong: newer=%d older=%d", a, b)
	}
}

func TestRunPageSections(t *testing.T) {
	p := pages(t, renderFixture(t))["runs/2026-10-27-222/index.html"]
	for _, want := range []string{
		"2 hyperthreads of 1 core", schema.ArmLabelD2, "<svg", "Seq/S", "BenchmarkSocketSend",
		"Predicted", "e2fe62b", "b55823e", "https://github.com/FPGSchiba/vcs-srs-server/actions/runs/222",
		"20261020.1.0", "org-op", "one-big-net",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("run page lacks %q", want)
		}
	}
}

func TestPredictedOnlyOnOneBigNet(t *testing.T) {
	p := pages(t, renderFixture(t))["runs/2026-10-27-222/index.html"]
	i := strings.Index(p, `id="predicted-one-big-net"`)
	if i < 0 {
		t.Fatal("no predicted section for one-big-net")
	}
	if strings.Contains(p, `id="predicted-org-op"`) {
		t.Error("predicted-vs-measured shown for org-op")
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDifferentImageNotCompared(t *testing.T) {
	in := t.TempDir()
	copyTree(t, "testdata/runs", in)
	mp := filepath.Join(in, "2026-10-27-222", "manifest.json")
	b, _ := os.ReadFile(mp)
	b = bytes.ReplaceAll(b, []byte("20261020.1.0"), []byte("20261101.2.0"))
	os.WriteFile(mp, b, 0o644)
	out := t.TempDir()
	if err := Render(in, out); err != nil {
		t.Fatal(err)
	}
	p := pages(t, out)["runs/2026-10-27-222/index.html"]
	if strings.Contains(p, "vs 2026-10-20-111") {
		t.Error("compared against a run with a different ImageVersion")
	}
	if !strings.Contains(p, "No comparable earlier run") {
		t.Error("missing not-comparable note")
	}
	// Sanity: with the same image the comparison is present.
	same := pages(t, renderFixture(t))["runs/2026-10-27-222/index.html"]
	if !strings.Contains(same, "vs 2026-10-20-111") {
		t.Error("same-image runs were not compared")
	}
}

func TestSchemaFailureNamesPath(t *testing.T) {
	in := t.TempDir()
	copyTree(t, "testdata/runs", in)
	bad := filepath.Join(in, "2026-10-20-111", "knees.csv")
	os.WriteFile(bad, []byte("not,the,header\n1,2,3\n"), 0o644)
	err := Render(in, t.TempDir())
	if err == nil {
		t.Fatal("render succeeded on a bad run")
	}
	if !strings.Contains(err.Error(), "2026-10-20-111") {
		t.Errorf("error does not name the run path: %v", err)
	}
}
