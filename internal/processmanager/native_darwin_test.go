package processmanager

import (
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFileLeaseRejectsSecondHolderAndSymlink(t *testing.T) {
	p := fileLeaseProvider{t.TempDir()}
	a, err := p.Acquire(validTestProfileID)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	if _, err = p.Acquire(validTestProfileID); !errors.Is(err, ErrProfileAlreadyRunning) {
		t.Fatalf("second holder: %v", err)
	}
	a.Release()
	b, err := p.Acquire(validTestProfileID)
	if err != nil {
		t.Fatal(err)
	}
	b.Release()
	path := filepath.Join(p.root, validTestProfileID+".lock")
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(p.root, "target"), path); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Acquire(validTestProfileID); err == nil {
		t.Fatal("accepted symlink lock")
	}
}

func TestProcessArgsPreserveSpacesAndExcludeEnvironment(t *testing.T) {
	data := binary.LittleEndian.AppendUint32(nil, 3)
	data = append(data, []byte("/Applications/Chromium.app/Contents/MacOS/Chromium\x00\x00Chromium\x00--user-data-dir=/Users/test/Library/Application Support/BrowserRuntime/profiles/a\x00\x00SECRET=private\x00")...)
	args, err := parseProcessArgs(data)
	if err != nil || len(args) != 3 || args[2] != "" || args[1] != "--user-data-dir=/Users/test/Library/Application Support/BrowserRuntime/profiles/a" {
		t.Fatalf("parse failed: %v", err)
	}
	if _, err = parseProcessArgs(data[:7]); err == nil {
		t.Fatal("accepted truncated args")
	}
}

func TestMacOccupancyDetectsUnmanagedProcess(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// An isolated executable with a space-containing profile argument exercises
	// real kernel enumeration without touching any user browser.
	executable := filepath.Join(root, "native-candidate")
	source := filepath.Join(root, "fixture.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"time\"\nfunc main(){time.Sleep(time.Minute)}"), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("go", "build", "-o", executable, source).CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, output)
	}
	expected := filepath.Join(root, "profile with spaces", "browser-data")
	cmd := exec.Command(executable, "--user-data-dir="+expected)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, err := macOccupancy(expected, executable)
		if status == StatusExternalRunning && err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unmanaged process not detected: %s %v", status, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
