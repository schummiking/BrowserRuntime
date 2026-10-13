package processmanager

/*
#cgo LDFLAGS: -framework AppKit -framework ApplicationServices
#include <libproc.h>
int br_activate(int pid);
int br_terminate(int pid);
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"browserruntime/internal/engine"
	"browserruntime/internal/profile"
	"golang.org/x/sys/unix"
)

type nativeLauncher struct{}
type nativeProcess struct{ cmd *exec.Cmd }

func (nativeLauncher) Start(spec engine.LaunchSpec) (managedProcess, error) {
	cmd := exec.Command(spec.ExecutablePath, spec.Arguments...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &nativeProcess{cmd}, nil
}
func (p *nativeProcess) PID() int    { return p.cmd.Process.Pid }
func (p *nativeProcess) Wait() error { return p.cmd.Wait() }
func (p *nativeProcess) OpenWindow() error {
	if C.br_activate(C.int(p.PID())) == 0 {
		return errors.New("could not activate browser window")
	}
	return nil
}
func (p *nativeProcess) CloseWindow() error {
	if C.br_terminate(C.int(p.PID())) == 0 {
		return errors.New("could not request browser termination")
	}
	return nil
}

type fileLeaseProvider struct{ root string }
type fileLease struct {
	file *os.File
	once sync.Once
}

func (l *fileLease) Release() {
	l.once.Do(func() { _ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN); _ = l.file.Close() })
}
func (p fileLeaseProvider) Acquire(id string) (profileLease, error) {
	if profile.ValidateID(id) != nil {
		return nil, ErrOccupancyUnknown
	}
	if err := os.MkdirAll(p.root, 0700); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(p.root, id+".lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), id)
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrProfileAlreadyRunning
		}
		return nil, err
	}
	return &fileLease{file: f}, nil
}

func NewProcessManager() *ProcessManager {
	// A manager without paths deliberately cannot confirm occupancy.
	return newProcessManager(nativeLauncher{}, fileLeaseProvider{}, defaultGracefulStopTimeout)
}
func NewProcessManagerWithPaths(root, executable string) (*ProcessManager, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(executable) {
		return nil, ErrOccupancyUnknown
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, ErrOccupancyUnknown
	}
	m := newProcessManager(nativeLauncher{}, fileLeaseProvider{filepath.Join(root, ".locks")}, defaultGracefulStopTimeout)
	m.externalOccupancy = func(id string) (Status, error) {
		return macOccupancy(filepath.Join(root, id, "browser-data"), executable)
	}
	return m, nil
}

// KERN_PROCARGS2 contains argc, executable, padding, argv, then environment.
// Parse only argv; private arguments never enter diagnostics or UI responses.
func parseProcessArgs(data []byte) ([]string, error) {
	if len(data) < 5 {
		return nil, ErrOccupancyUnknown
	}
	argc := int(binary.LittleEndian.Uint32(data[:4]))
	data = data[4:]
	if argc < 1 || argc > 65536 {
		return nil, ErrOccupancyUnknown
	}
	i := bytes.IndexByte(data, 0)
	if i < 0 {
		return nil, ErrOccupancyUnknown
	}
	data = data[i+1:]
	data = bytes.TrimLeft(data, "\x00")
	args := make([]string, 0, argc)
	for len(args) < argc {
		i = bytes.IndexByte(data, 0)
		if i < 0 {
			return nil, ErrOccupancyUnknown
		}
		args = append(args, string(data[:i]))
		data = data[i+1:]
	}
	return args, nil
}

func macOccupancy(expected, executable string) (Status, error) {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return StatusUnknown, ErrOccupancyUnknown
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return StatusUnknown, ErrOccupancyUnknown
	}
	// Include helper processes: a manager crash must not permit deletion while
	// Chromium children still hold profile files.
	bundle := strings.TrimSuffix(executable, "/Contents/MacOS/Chromium")
	type observation struct {
		parent  int32
		args    []string
		created unix.Timeval
	}
	observed := map[int32]observation{}
	for _, p := range procs {
		pid := p.Proc.P_pid
		buf := make([]byte, 4096)
		n := C.proc_pidpath(C.int(pid), unsafe.Pointer(&buf[0]), C.uint32_t(len(buf)))
		if n <= 0 {
			if strings.HasPrefix(unix.ByteSliceToString(p.Proc.P_comm[:]), "Chromium") && !errors.Is(unix.Kill(int(pid), 0), syscall.ESRCH) {
				return StatusUnknown, ErrOccupancyUnknown
			}
			continue
		}
		path := string(bytes.TrimRight(buf[:int(n)], "\x00"))
		if path != executable && !(strings.HasPrefix(path, bundle+"/Contents/Frameworks/") && strings.HasPrefix(filepath.Base(path), "Chromium Helper")) {
			continue
		}
		data, err := unix.SysctlRaw("kern.procargs2", int(pid))
		if err != nil {
			if errors.Is(unix.Kill(int(pid), 0), syscall.ESRCH) {
				continue
			}
			return StatusUnknown, ErrOccupancyUnknown
		}
		args, err := parseProcessArgs(data)
		if err != nil {
			return StatusUnknown, err
		}
		observed[pid] = observation{p.Eproc.Ppid, args, p.Proc.P_starttime}
	}
	unknown := false
	for pid := range observed {
		resolved := false
		for depth := 0; depth < 32; depth++ {
			o, ok := observed[pid]
			if !ok {
				break
			}
			for i, arg := range o.args {
				var path string
				if strings.HasPrefix(arg, "--user-data-dir=") {
					path = strings.TrimPrefix(arg, "--user-data-dir=")
				}
				if arg == "--user-data-dir" && i+1 < len(o.args) {
					path = o.args[i+1]
				}
				if path != "" {
					resolved = true
					if filepath.Clean(path) == expected {
						return StatusExternalRunning, nil
					}
				}
			}
			if resolved || pid == o.parent {
				break
			}
			if parent, ok := observed[o.parent]; !ok || parent.created.Sec > o.created.Sec || (parent.created.Sec == o.created.Sec && parent.created.Usec > o.created.Usec) {
				break
			}
			pid = o.parent
		}
		unknown = unknown || !resolved
	}
	if unknown {
		return StatusUnknown, ErrOccupancyUnknown
	}
	return StatusStopped, nil
}
