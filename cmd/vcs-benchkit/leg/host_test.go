package leg

import (
	"os"
	"path/filepath"
	"testing"
)

// The throwaway coalition passwords and the server's data directories are
// scratch: the results directory is uploaded as a public artifact, so they must
// never be written under it.
func TestPasswordsFileLivesInTheScratchDirNotTheResults(t *testing.T) {
	out, scratch := t.TempDir(), t.TempDir()
	e := &hostEnv{outDir: out, scratchDir: scratch}
	p, err := e.passwordsFile()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p) != scratch {
		t.Errorf("passwords file at %s, want it under the scratch dir %s", p, scratch)
	}
	if _, err := os.Stat(filepath.Join(out, "passwords.json")); err == nil {
		t.Error("passwords.json was written into the results directory")
	}
}

func TestServerDirIsScratch(t *testing.T) {
	e := &hostEnv{outDir: "/results", scratchDir: "/scratch"}
	if got := e.serverDir(3); filepath.Dir(got) != filepath.Clean("/scratch") {
		t.Errorf("server dir = %s, want it under the scratch dir", got)
	}
}
