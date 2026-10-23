package logfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func content(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, w *Writer, s string) {
	t.Helper()
	if n, err := w.Write([]byte(s)); err != nil || n != len(s) {
		t.Fatalf("Write(%q) = %d, %v", s, n, err)
	}
}

func TestAppendAndRotateAtBoundary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	put(t, p, "abc")
	w, err := New(p, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	write(t, w, "1234567")
	if got := content(t, p); got != "abc1234567" {
		t.Fatal(got)
	}
	if _, err := os.Stat(p + ".1"); !os.IsNotExist(err) {
		t.Fatalf("exact threshold rotated: %v", err)
	}
	write(t, w, "Z")
	if got := content(t, p+".1"); got != "abc1234567" {
		t.Fatal(got)
	}
	if got := content(t, p); got != "Z" {
		t.Fatal(got)
	}
}

func TestHistoryOrderAndLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	w, err := New(p, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	for _, s := range []string{"A", "B", "C", "D"} {
		write(t, w, s)
	}
	for suffix, want := range map[string]string{"": "D", ".1": "C", ".2": "B"} {
		if got := content(t, p+suffix); got != want {
			t.Fatalf("%s = %q, want %q", suffix, got, want)
		}
	}
	if _, err := os.Stat(p + ".3"); !os.IsNotExist(err) {
		t.Fatalf("excess history: %v", err)
	}
}

func TestStartupRotationAndCleanup(t *testing.T) {
	for _, initial := range []string{"123", "12345"} {
		t.Run(initial, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "sim.log")
			put(t, p, initial)
			put(t, p+".1", "previous")
			for _, s := range []string{".2", ".3", ".9999999999999999999999999999"} {
				put(t, p+s, "old")
			}
			for _, s := range []string{".01", ".0", ".bak", "other"} {
				put(t, p+s, "keep")
			}
			if err := os.Mkdir(p+".4", 0700); err != nil {
				t.Fatal(err)
			}
			w, err := New(p, 3, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			if got := content(t, p+".1"); got != initial {
				t.Fatal(got)
			}
			if got := content(t, p); got != "" {
				t.Fatal(got)
			}
			for _, s := range []string{".2", ".3", ".9999999999999999999999999999"} {
				if _, err := os.Stat(p + s); !os.IsNotExist(err) {
					t.Fatalf("%s not cleaned: %v", s, err)
				}
			}
			for _, s := range []string{".01", ".0", ".bak", "other"} {
				if got := content(t, p+s); got != "keep" {
					t.Fatal(got)
				}
			}
			if info, err := os.Stat(p + ".4"); err != nil || !info.IsDir() {
				t.Fatalf("directory removed: %v", err)
			}
		})
	}
}

func TestZeroBackups(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	put(t, p, "old")
	put(t, p+".1", "history")
	w, err := New(p, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	write(t, w, "abc")
	write(t, w, "d")
	if got := content(t, p); got != "d" {
		t.Fatal(got)
	}
	if _, err := os.Stat(p + ".1"); !os.IsNotExist(err) {
		t.Fatalf("history retained: %v", err)
	}
}

func TestOversizedEntryAndClose(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	w, err := New(p, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	write(t, w, "ab")
	if n, err := w.Write([]byte("1234")); n != 0 || err == nil {
		t.Fatalf("oversized = %d, %v", n, err)
	}
	if got := content(t, p); got != "ab" {
		t.Fatal(got)
	}
	write(t, w, "c")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("x")); n != 0 || err == nil {
		t.Fatalf("closed = %d, %v", n, err)
	}
}

func TestRotationFailureDoesNotAppendAndRecovers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	w, err := New(p, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	write(t, w, "abc")
	if err := os.Mkdir(p+".1", 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if n, err := w.Write([]byte("d")); n != 0 || err == nil {
			t.Fatalf("blocked rotation = %d, %v", n, err)
		}
		if got := content(t, p); got != "abc" {
			t.Fatal(got)
		}
	}
	if err := os.Remove(p + ".1"); err != nil {
		t.Fatal(err)
	}
	write(t, w, "d")
	if got := content(t, p+".1"); got != "abc" {
		t.Fatal(got)
	}
	if got := content(t, p); got != "d" {
		t.Fatal(got)
	}
}

func TestPartialRotationResumesWithoutShiftingAgain(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	w, err := New(p, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	write(t, w, "new")
	put(t, p+".1", "one")
	put(t, p+".2", "two")
	put(t, p+".3", "old")
	realRename := w.renameFile
	w.renameFile = func(old, new string) error {
		if old == p+".1" {
			return errors.New("模拟临时重命名失败")
		}
		return realRename(old, new)
	}
	if n, err := w.Write([]byte("x")); n != 0 || err == nil {
		t.Fatalf("partial rotation = %d, %v", n, err)
	}
	if got := content(t, p+".3"); got != "two" {
		t.Fatal(got)
	}
	w.renameFile = realRename
	write(t, w, "x")
	for suffix, want := range map[string]string{"": "x", ".1": "new", ".2": "one", ".3": "two"} {
		if got := content(t, p+suffix); got != want {
			t.Fatalf("%s = %q, want %q", suffix, got, want)
		}
	}
}

func TestHistorySymlinkIsNotFollowed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	target := filepath.Join(t.TempDir(), "target")
	put(t, target, "keep")
	if err := os.Symlink(target, p+".2"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	w, err := New(p, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	write(t, w, "abc")
	write(t, w, "d")
	if got := content(t, target); got != "keep" {
		t.Fatal(got)
	}
	if _, err := os.Lstat(p + ".2"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p+".1.tmp"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p + ".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p+".1.tmp", p+".1"); err != nil {
		t.Fatal(err)
	}
	write(t, w, "ef")
	if n, err := w.Write([]byte("g")); n != 0 || err == nil {
		t.Fatalf("symlink collision = %d, %v", n, err)
	}
	if got := content(t, target); got != "keep" {
		t.Fatal(got)
	}
}

func TestInvalidSettingsAndPaths(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	for _, c := range []struct {
		size    int64
		backups int
	}{{0, 1}, {-1, 1}, {1, -1}} {
		if w, err := New(p, c.size, c.backups); err == nil {
			w.Close()
			t.Fatal("accepted invalid settings")
		}
	}
	put(t, p, "parent is a file")
	if w, err := New(filepath.Join(p, "missing", "sim.log"), 3, 1); err == nil {
		w.Close()
		t.Fatal("accepted parent that is a file")
	}
	if w, err := New(filepath.Dir(p), 3, 1); err == nil {
		w.Close()
		t.Fatal("accepted directory")
	}
}

func TestCreatesNestedLogDirectory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "new", "nested", "sim.log")
	w, err := New(p, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	write(t, w, "abc")
	if got := content(t, p); got != "abc" {
		t.Fatal(got)
	}
}

func TestOpenFailureAfterRenameRecovers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	w, err := New(p, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	write(t, w, "new")
	put(t, p+".1", "one")
	realRename := w.renameFile
	w.renameFile = func(old, new string) error {
		if err := realRename(old, new); err != nil {
			return err
		}
		if old == p {
			return os.Mkdir(p, 0700)
		}
		return nil
	}
	if n, err := w.Write([]byte("x")); n != 0 || err == nil {
		t.Fatalf("blocked open = %d, %v", n, err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	w.renameFile = realRename
	write(t, w, "x")
	for suffix, want := range map[string]string{"": "x", ".1": "new", ".2": "one"} {
		if got := content(t, p+suffix); got != want {
			t.Fatalf("%s = %q, want %q", suffix, got, want)
		}
	}
}

func TestWriteFailureReopensFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	w, err := New(p, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	write(t, w, "ab")
	if err := w.file.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("c")); n != 0 || err == nil {
		t.Fatalf("closed descriptor = %d, %v", n, err)
	}
	write(t, w, "c")
	if got := content(t, p); got != "abc" {
		t.Fatal(got)
	}
}

func TestCleanupWithoutStartupRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	put(t, p, "x")
	put(t, p+".1", "one")
	put(t, p+".2", "two")
	put(t, p+".3", "old")
	w, err := New(p, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for suffix, want := range map[string]string{"": "x", ".1": "one", ".2": "two"} {
		if got := content(t, p+suffix); got != want {
			t.Fatalf("%s = %q, want %q", suffix, got, want)
		}
	}
	if _, err := os.Stat(p + ".3"); !os.IsNotExist(err) {
		t.Fatalf("excess history: %v", err)
	}
}

func TestLargeBackupCountOnlyVisitsExistingFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sim.log")
	put(t, p, "new")
	put(t, p+".1", "one")
	put(t, p+".9", "nine")
	w, err := New(p, 3, int(^uint(0)>>1))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for suffix, want := range map[string]string{"": "", ".1": "new", ".2": "one", ".10": "nine"} {
		if got := content(t, p+suffix); got != want {
			t.Fatalf("%s = %q, want %q", suffix, got, want)
		}
	}
}
