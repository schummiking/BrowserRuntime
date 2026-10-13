package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"browserruntime/internal/profile"
)

const (
	configFileName = "network.json"
	secretFileName = "network.secret"
	maxConfigBytes = 64 << 10
	maxSecretBytes = 1 << 20
)

// SecretProtector provides platform credential encryption independently of storage.
type SecretProtector interface {
	Protect([]byte) ([]byte, error)
	Unprotect([]byte) ([]byte, error)
}

type ConnectionTester interface {
	TestConnection(context.Context, *ResolvedProxy) (ConnectionResult, error)
}

// Store persists profile-scoped network configuration and platform-protected secrets.
type Store struct {
	root       string
	profiles   *profile.Store
	protector  SecretProtector
	mu         sync.Mutex
	renameFile func(string, string) error
}

func NewStore(profilesRoot string) (*Store, error) {
	return NewStoreWithProtector(profilesRoot, newSecretProtector())
}

func NewStoreWithProtector(profilesRoot string, protector SecretProtector) (*Store, error) {
	if protector == nil {
		return nil, errors.New("secret protector is required")
	}
	profiles, err := profile.NewStore(profilesRoot)
	if err != nil {
		return nil, err
	}
	absoluteRoot, err := filepath.Abs(profilesRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve profiles root: %w", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve canonical profiles root: %w", err)
	}
	return &Store{
		root:       filepath.Clean(canonicalRoot),
		profiles:   profiles,
		protector:  protector,
		renameFile: os.Rename,
	}, nil
}

func (s *Store) SetDirect(profileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	profileDir, err := s.ownedProfileDir(profileID)
	if err != nil {
		return err
	}
	secretPath := filepath.Join(profileDir, secretFileName)
	if err := os.Remove(secretPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("remove obsolete proxy secret")
	}
	return s.writeConfigAtomic(profileDir, Config{Mode: ModeDirect})
}

func (s *Store) SetProxy(profileID string, config Config, password string) error {
	config.Locality = nil
	if err := config.Validate(); err != nil {
		return err
	}
	if config.Mode != ModeProxy {
		return fmt.Errorf("%w: SetProxy requires proxy mode", ErrInvalidConfig)
	}
	if config.Username != "" && password == "" {
		return ErrSecretRequired
	}
	if config.Username == "" && password != "" {
		return fmt.Errorf("%w: password requires a username", ErrInvalidConfig)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	profileDir, err := s.ownedProfileDir(profileID)
	if err != nil {
		return err
	}

	var cipher []byte
	if config.Username != "" {
		plain := []byte(password)
		cipher, err = s.protector.Protect(plain)
		zeroBytes(plain)
		if err != nil {
			return ErrSecretUnavailable
		}
		defer zeroBytes(cipher)
	}
	secretPath := filepath.Join(profileDir, secretFileName)
	if err := os.Remove(secretPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("remove obsolete proxy secret")
	}
	if err := s.writeConfigAtomic(profileDir, config); err != nil {
		return err
	}
	if config.Username == "" {
		return nil
	}
	if err := s.writeAtomic(secretPath, cipher); err != nil {
		return ErrSecretUnavailable
	}
	return nil
}

// ResolveLocality resolves and persists locality for a previously confirmed
// proxy exit IP. Network mutation is serialized through the complete flow so a
// proxy cannot change between confirmation and persistence.
func (s *Store) ResolveLocality(ctx context.Context, profileID string, exitIP string, resolver LocalityResolver) (*Locality, error) {
	if resolver == nil || net.ParseIP(exitIP) == nil {
		return nil, ErrInvalidLocality
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	profileDir, err := s.ownedProfileDir(profileID)
	if err != nil {
		return nil, err
	}
	current, err := s.readConfig(profileDir)
	if err != nil {
		return nil, err
	}
	if current.Mode != ModeProxy {
		return nil, ErrInvalidLocality
	}

	locality, err := resolver.ResolveLocality(ctx, exitIP)
	if err != nil {
		return nil, ErrLocalityUnavailable
	}
	locality, err = normalizeResolvedLocality(locality)
	if err != nil || !sameExitIP(exitIP, locality.ExitIP) {
		return nil, ErrInvalidLocality
	}

	updated := *current
	updated.Locality = &locality
	if err := s.writeConfigAtomic(profileDir, updated); err != nil {
		return nil, err
	}
	return &locality, nil
}

func (s *Store) Get(profileID string) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profileDir, err := s.ownedProfileDir(profileID)
	if err != nil {
		return nil, err
	}
	config, err := s.readConfig(profileDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrConfigMissing
	}
	return config, err
}

func (s *Store) ResolveProxy(profileID string) (*ResolvedProxy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profileDir, err := s.ownedProfileDir(profileID)
	if err != nil {
		return nil, err
	}
	config, err := s.readConfig(profileDir)
	if err != nil {
		return nil, err
	}
	if config.Mode != ModeProxy {
		return nil, nil
	}
	resolved := &ResolvedProxy{Scheme: config.Scheme, Host: config.Host, Port: config.Port, Username: config.Username}
	if config.Username == "" {
		return resolved, nil
	}
	cipher, err := os.ReadFile(filepath.Join(profileDir, secretFileName))
	if err != nil || len(cipher) == 0 || len(cipher) > maxSecretBytes {
		return nil, ErrSecretUnavailable
	}
	defer zeroBytes(cipher)
	plain, err := s.protector.Unprotect(cipher)
	if err != nil || len(plain) == 0 {
		zeroBytes(plain)
		return nil, ErrSecretUnavailable
	}
	resolved.Password = string(plain)
	zeroBytes(plain)
	if err := resolved.Validate(); err != nil {
		return nil, ErrSecretUnavailable
	}
	return resolved, nil
}

func (s *Store) TestConnection(ctx context.Context, profileID string, tester ConnectionTester) (ConnectionResult, error) {
	if tester == nil {
		return ConnectionResult{}, ErrConnectionFailed
	}
	config, err := s.Get(profileID)
	if err != nil {
		return ConnectionResult{}, err
	}
	var resolved *ResolvedProxy
	if config.Mode == ModeProxy {
		resolved, err = s.ResolveProxy(profileID)
		if err != nil {
			return ConnectionResult{}, err
		}
	}
	result, err := tester.TestConnection(ctx, resolved)
	if err != nil {
		return ConnectionResult{}, ErrConnectionFailed
	}
	return result, nil
}

func (s *Store) ownedProfileDir(id string) (string, error) {
	if err := profile.ValidateID(id); err != nil {
		return "", err
	}
	if _, err := s.profiles.Get(id); err != nil {
		return "", err
	}
	dir := filepath.Join(s.root, id)
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve profile directory: %w", err)
	}
	if !pathWithinRoot(s.root, canonical) || !samePath(dir, canonical) {
		return "", profile.ErrInvalidProfileMetadata
	}
	return canonical, nil
}

func (s *Store) readConfig(profileDir string) (*Config, error) {
	file, err := os.Open(filepath.Join(profileDir, configFileName))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxConfigBytes))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return nil, ErrInvalidConfig
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidConfig
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &config, nil
}

func (s *Store) writeConfigAtomic(profileDir string, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode network configuration: %w", err)
	}
	data = append(data, '\n')
	return s.writeAtomic(filepath.Join(profileDir, configFileName), data)
}

func (s *Store) writeAtomic(target string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(target), ".network-*.tmp")
	if err != nil {
		return errors.New("create temporary network file")
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return errors.New("secure temporary network file")
	}
	if _, err := io.Copy(temp, bytes.NewReader(data)); err != nil {
		return errors.New("write temporary network file")
	}
	if err := temp.Sync(); err != nil {
		return errors.New("sync temporary network file")
	}
	if err := temp.Close(); err != nil {
		return errors.New("close temporary network file")
	}
	if err := s.renameFile(tempPath, target); err != nil {
		return errors.New("commit network file")
	}
	committed = true
	return nil
}

func pathWithinRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(relative) || relative == "." {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func samePath(first, second string) bool {
	first, second = filepath.Clean(first), filepath.Clean(second)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(first, second)
	}
	return first == second
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
