package atomic

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
)

// --- Happy path -----------------------------------------------------------

func TestWriteFile_CreatesTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	want := []byte("hello world\n")
	if err := WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("contents = %q; want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v; want 0o644", info.Mode().Perm())
	}
}

func TestWriteFile_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("contents = %q; want %q", got, "new")
	}
}

// --- Error paths leave the target file untouched -------------------------

func TestWriteAtomic_WriteCallbackErrorLeavesTargetUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(path, []byte("good-old"), 0o644); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("simulated mid-write crash")
	err := WriteAtomic(path, func(w io.Writer) error {
		// Write something, then "crash".
		if _, _ = w.Write([]byte("partial-")); false {
			return nil
		}
		return wantErr
	}, 0o644)
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped %v; got %v", wantErr, err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "good-old" {
		t.Errorf("target was mutated despite error: %q", got)
	}
	if leftover := globTmp(t, dir); len(leftover) != 0 {
		t.Errorf("temp files leaked after error: %v", leftover)
	}
}

func TestWriteFile_ReadOnlyDirReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only directory semantics are platform-specific")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	path := filepath.Join(dir, "target.txt")
	err := WriteFile(path, []byte("x"), 0o644)
	if err == nil {
		t.Fatal("expected error writing into a read-only dir")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("target should not exist; stat err = %v", statErr)
	}
}

// --- Permission preserved (perm argument honored even on overwrite) ------

func TestWriteFile_PermAppliedOnOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode after overwrite = %v; want 0o644 (not 0o600 from CreateTemp)", info.Mode().Perm())
	}
}

func TestWriteFile_PermRespectedForRestrictiveMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.env")
	if err := WriteFile(path, []byte("TOKEN=...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v; want 0o600", info.Mode().Perm())
	}
}

// --- Streamed variant works for io.Copy-style writes ---------------------

func TestWriteAtomic_StreamedWriteFromReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.bin")

	// 1 MB of deterministic bytes (cycles 0..255).
	const n = 1 << 20
	src := make([]byte, n)
	for i := range src {
		src[i] = byte(i)
	}

	r := newByteReader(src)
	if err := WriteAtomic(path, func(w io.Writer) error {
		_, err := io.Copy(w, r)
		return err
	}, 0o644); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("size = %d; want %d", len(got), n)
	}
	for i := range got {
		if got[i] != byte(i) {
			t.Fatalf("byte at %d = %d; want %d", i, got[i], byte(i))
			break
		}
	}
}

// --- Tmp files are cleaned up across error and success paths -------------

func TestWriteFile_TmpCleanedOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	if err := WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if leftover := globTmp(t, dir); len(leftover) != 0 {
		t.Errorf("temp files leaked after success: %v", leftover)
	}
}

func TestWriteFile_TmpCleanedOnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(path, []byte("good-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = WriteAtomic(path, func(w io.Writer) error {
		_, _ = w.Write([]byte("partial-"))
		return errors.New("boom")
	}, 0o644)
	if leftover := globTmp(t, dir); len(leftover) != 0 {
		t.Errorf("temp files leaked after error: %v", leftover)
	}
}

// --- Concurrent writers — each call commits or fails atomically ---------

func TestWriteFile_ConcurrentWritesNeverInterleave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "race.txt")

	const n = 16
	payloads := make([][]byte, n)
	for i := 0; i < n; i++ {
		payloads[i] = []byte("payload-" + string(rune('A'+i)))
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			if err := WriteFile(path, payloads[i], 0o644); err != nil {
				t.Errorf("WriteFile (goroutine %d): %v", i, err)
			}
		}()
	}
	wg.Wait()

	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range payloads {
		if string(final) == string(p) {
			return
		}
	}
	t.Errorf("final contents %q does not match any single payload — interleaving?", final)
}

// --- IsSpaceError sentinel maps ENOSPC ------------------------------------

func TestIsSpaceError_RecognizesENOSPC(t *testing.T) {
	if !IsSpaceError(syscall.ENOSPC) {
		t.Errorf("IsSpaceError(syscall.ENOSPC) = false; want true on this platform")
	}
	if IsSpaceError(errors.New("unrelated")) {
		t.Error("IsSpaceError(unrelated) = true; want false")
	}
}

// --- helpers --------------------------------------------------------------

func globTmp(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// byteReader is a minimal io.Reader over a byte slice; using
// strings.NewReader would lose the byte-pattern semantics we want.
type byteReader struct {
	data []byte
	pos  int
}

func newByteReader(data []byte) *byteReader { return &byteReader{data: data} }

func (b *byteReader) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}
