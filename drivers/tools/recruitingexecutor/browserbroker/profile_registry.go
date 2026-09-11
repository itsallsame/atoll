package browserbroker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const ProfileRegistryVersion = "recruiting.browser-profile-registry.v1"

type ProfileRegistryEntry struct {
	ProfileID        string `json:"profile_id"`
	ProfileVersion   uint64 `json:"profile_version"`
	SecurityDomain   string `json:"security_domain"`
	UserDataDir      string `json:"user_data_dir"`
	ProfileDirectory string `json:"profile_directory,omitempty"`
	BindingTokenHash string `json:"binding_token_hash,omitempty"`
}

type profileRegistryDocument struct {
	Version  string                 `json:"version"`
	Profiles []ProfileRegistryEntry `json:"profiles"`
}

// ProfileLease is a local authorization to use one Chrome profile directory.
// Release must be called; it serializes all sessions for the same Profile.
type ProfileLease struct {
	UserDataDir      string
	ProfileDirectory string
	lockFile         *os.File
	release          func()
}

func (l *ProfileLease) Release() {
	if l != nil && l.release != nil {
		if l.lockFile != nil {
			_ = syscall.Flock(int(l.lockFile.Fd()), syscall.LOCK_UN)
			_ = l.lockFile.Close()
			l.lockFile = nil
		}
		l.release()
		l.release = nil
	}
}

type ProfileResolver interface {
	Resolve(context.Context, string, uint64, string) (*ProfileLease, error)
}

// FileProfileResolver reloads a small, owner-only registry for each lease so
// a repaired Profile can rotate versions without restarting the Executor.
// The registry contains local paths, never cookies, passwords, or OTP values.
type FileProfileResolver struct {
	path  string
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func NewFileProfileResolver(path string) (*FileProfileResolver, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return nil, errors.New("browser Profile registry path must be absolute")
	}
	if _, err := readProfileRegistry(path); err != nil {
		return nil, err
	}
	return &FileProfileResolver{path: filepath.Clean(path), locks: make(map[string]chan struct{})}, nil
}

func (r *FileProfileResolver) Resolve(ctx context.Context, profileRef string, profileVersion uint64,
	endpointURL string) (*ProfileLease, error) {
	if r == nil || ctx == nil || profileVersion == 0 {
		return nil, errors.New("Profile lease requires resolver, context, and version")
	}
	profileID, err := parseProfileReference(profileRef)
	if err != nil {
		return nil, err
	}
	entries, err := readProfileRegistry(r.path)
	if err != nil {
		return nil, err
	}
	entry, found := entries[profileID]
	if !found || entry.ProfileVersion != profileVersion {
		return nil, errors.New("browser Profile is absent or its local version is stale")
	}
	endpoint, err := url.Parse(strings.TrimSpace(endpointURL))
	if err != nil || !strings.EqualFold(endpoint.Hostname(), entry.SecurityDomain) {
		return nil, errors.New("browser Profile security domain does not match the endpoint")
	}
	return r.acquireLease(ctx, entry)
}

// AcquireBoundProfile authenticates an interactive extension connection and
// holds the same cross-process lease used by daily Chrome sessions. This keeps
// a repair browser and a scheduled browser from opening one user-data-dir at
// the same time.
func (r *FileProfileResolver) AcquireBoundProfile(ctx context.Context, profileID, token string) (*ProfileLease, error) {
	if err := r.AuthenticateBinding(profileID, token); err != nil {
		return nil, err
	}
	entries, err := readProfileRegistry(r.path)
	if err != nil {
		return nil, err
	}
	entry, found := entries[profileID]
	if !found {
		return nil, errors.New("browser Profile binding is absent")
	}
	return r.acquireLease(ctx, entry)
}

func (r *FileProfileResolver) acquireLease(ctx context.Context, entry ProfileRegistryEntry) (*ProfileLease, error) {
	lock := r.profileLock(entry.ProfileID)
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	releaseLocal := func() { <-lock }
	lockPath := filepath.Join(entry.UserDataDir, ".atoll-recruiting-profile.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		releaseLocal()
		return nil, fmt.Errorf("open browser Profile process lock: %w", err)
	}
	if err := lockFile.Chmod(0o600); err != nil {
		_ = lockFile.Close()
		releaseLocal()
		return nil, err
	}
	for {
		err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = lockFile.Close()
			releaseLocal()
			return nil, fmt.Errorf("lock browser Profile across processes: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = lockFile.Close()
			releaseLocal()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return &ProfileLease{UserDataDir: entry.UserDataDir, ProfileDirectory: entry.ProfileDirectory,
		lockFile: lockFile, release: releaseLocal}, nil
}

// AuthenticateBinding proves that an extension connection was provisioned in
// the managed Chrome profile identified by profileID. Only the hash is stored
// in the owner-only registry; the raw token stays in chrome.storage.local.
func (r *FileProfileResolver) AuthenticateBinding(profileID, token string) error {
	if r == nil || strings.TrimSpace(profileID) == "" || len(token) < 32 || len(token) > 256 {
		return errors.New("browser Profile binding identity or token is invalid")
	}
	entries, err := readProfileRegistry(r.path)
	if err != nil {
		return err
	}
	entry, found := entries[profileID]
	if !found || entry.BindingTokenHash == "" {
		return errors.New("browser Profile binding is not provisioned")
	}
	digest := sha256.Sum256([]byte(token))
	want := "sha256:" + fmt.Sprintf("%x", digest[:])
	if subtle.ConstantTimeCompare([]byte(want), []byte(entry.BindingTokenHash)) != 1 {
		return errors.New("browser Profile binding token does not match")
	}
	return nil
}

// AdvanceVersion atomically publishes credential material already written to
// the managed directory. It is idempotent for broker/result retries. Repair
// may advance from the last ready version across the intermediate repairing
// version; verification advances exactly one version.
func (r *FileProfileResolver) AdvanceVersion(profileID, securityDomain string, allowedCurrent []uint64,
	nextVersion uint64) error {
	if r == nil || strings.TrimSpace(profileID) == "" || nextVersion == 0 {
		return errors.New("browser Profile registry rotation is invalid")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	mutationLock, err := acquireRegistryMutationLock(r.path)
	if err != nil {
		return err
	}
	defer releaseRegistryMutationLock(mutationLock)
	document, err := readProfileRegistryDocument(r.path)
	if err != nil {
		return err
	}
	matched := false
	for index := range document.Profiles {
		entry := &document.Profiles[index]
		if entry.ProfileID != profileID {
			continue
		}
		matched = true
		if !strings.EqualFold(entry.SecurityDomain, securityDomain) {
			return errors.New("browser Profile registry rotation security domain differs")
		}
		if entry.ProfileVersion == nextVersion {
			return nil
		}
		allowed := false
		for _, version := range allowedCurrent {
			allowed = allowed || entry.ProfileVersion == version
		}
		if !allowed || entry.ProfileVersion >= nextVersion {
			return errors.New("browser Profile registry rotation version is fenced")
		}
		entry.ProfileVersion = nextVersion
		break
	}
	if !matched {
		return errors.New("browser Profile registry rotation target is absent")
	}
	return writeProfileRegistryDocument(r.path, document)
}

// ProvisionProfile atomically adds one initial local browser slot. The raw
// binding token is deliberately absent: callers provide only its SHA-256 and
// keep the token in a separate owner-only file until the extension imports it.
func ProvisionProfile(path string, entry ProfileRegistryEntry) error {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || entry.ProfileVersion != 1 || entry.BindingTokenHash == "" {
		return errors.New("initial browser Profile provisioning requires an absolute registry path, version 1, and binding token hash")
	}
	normalized, err := normalizeProfileRegistryEntry(entry, 0)
	if err != nil {
		return err
	}
	mutationLock, err := acquireRegistryMutationLock(path)
	if err != nil {
		return err
	}
	defer releaseRegistryMutationLock(mutationLock)
	document := profileRegistryDocument{Version: ProfileRegistryVersion}
	if _, statErr := os.Lstat(path); statErr == nil {
		document, err = readProfileRegistryDocument(path)
		if err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("stat browser Profile registry: %w", statErr)
	}
	for _, current := range document.Profiles {
		if current.ProfileID != normalized.ProfileID {
			continue
		}
		if current == normalized {
			return nil
		}
		return errors.New("browser Profile is already provisioned with different local facts")
	}
	if len(document.Profiles) >= 10_000 {
		return errors.New("browser Profile registry reached its bounded profile limit")
	}
	document.Profiles = append(document.Profiles, normalized)
	sort.Slice(document.Profiles, func(i, j int) bool { return document.Profiles[i].ProfileID < document.Profiles[j].ProfileID })
	return writeProfileRegistryDocument(path, document)
}

func (r *FileProfileResolver) profileLock(profileID string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock := r.locks[profileID]
	if lock == nil {
		lock = make(chan struct{}, 1)
		r.locks[profileID] = lock
	}
	return lock
}

func readProfileRegistry(path string) (map[string]ProfileRegistryEntry, error) {
	document, err := readProfileRegistryDocument(path)
	if err != nil {
		return nil, err
	}
	result := make(map[string]ProfileRegistryEntry, len(document.Profiles))
	for _, entry := range document.Profiles {
		result[entry.ProfileID] = entry
	}
	return result, nil
}

func readProfileRegistryDocument(path string) (profileRegistryDocument, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return profileRegistryDocument{}, fmt.Errorf("stat browser Profile registry: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		(info.Mode().Perm() != 0o400 && info.Mode().Perm() != 0o600) || info.Size() < 2 || info.Size() > 1<<20 {
		return profileRegistryDocument{}, errors.New("browser Profile registry must be a 0400/0600 regular file no larger than one MiB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return profileRegistryDocument{}, fmt.Errorf("read browser Profile registry: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document profileRegistryDocument
	if err := decoder.Decode(&document); err != nil {
		return profileRegistryDocument{}, fmt.Errorf("decode browser Profile registry: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return profileRegistryDocument{}, errors.New("browser Profile registry has trailing JSON")
	}
	if document.Version != ProfileRegistryVersion || len(document.Profiles) == 0 || len(document.Profiles) > 10_000 {
		return profileRegistryDocument{}, errors.New("browser Profile registry version or bounded profile list is invalid")
	}
	result := make(map[string]ProfileRegistryEntry, len(document.Profiles))
	for index, rawEntry := range document.Profiles {
		entry, err := normalizeProfileRegistryEntry(rawEntry, index)
		if err != nil {
			return profileRegistryDocument{}, err
		}
		if _, duplicate := result[entry.ProfileID]; duplicate {
			return profileRegistryDocument{}, fmt.Errorf("browser Profile registry repeats profile_id %q", entry.ProfileID)
		}
		result[entry.ProfileID] = entry
		document.Profiles[index] = entry
	}
	return document, nil
}

func normalizeProfileRegistryEntry(entry ProfileRegistryEntry, index int) (ProfileRegistryEntry, error) {
	entry.ProfileID = strings.TrimSpace(entry.ProfileID)
	entry.SecurityDomain = strings.ToLower(strings.TrimSpace(entry.SecurityDomain))
	entry.UserDataDir = filepath.Clean(strings.TrimSpace(entry.UserDataDir))
	entry.ProfileDirectory = strings.TrimSpace(entry.ProfileDirectory)
	entry.BindingTokenHash = strings.TrimSpace(entry.BindingTokenHash)
	if entry.ProfileDirectory == "" {
		entry.ProfileDirectory = "Default"
	}
	if entry.ProfileID == "" || len(entry.ProfileID) > 191 || strings.ContainsAny(entry.ProfileID, "\x00\r\n\t") ||
		entry.ProfileVersion == 0 || entry.SecurityDomain == "" || strings.ContainsAny(entry.SecurityDomain, "/:@?# \t\r\n") ||
		!filepath.IsAbs(entry.UserDataDir) || entry.UserDataDir == string(filepath.Separator) ||
		entry.ProfileDirectory == "." || entry.ProfileDirectory == ".." || len(entry.ProfileDirectory) > 128 ||
		strings.ContainsAny(entry.ProfileDirectory, "/\\\\\x00\r\n") ||
		(entry.BindingTokenHash != "" && !validSHA256(entry.BindingTokenHash)) {
		return ProfileRegistryEntry{}, fmt.Errorf("browser Profile registry entry %d is invalid", index)
	}
	directoryInfo, err := os.Stat(entry.UserDataDir)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode().Perm()&0o077 != 0 {
		return ProfileRegistryEntry{}, fmt.Errorf("browser Profile directory %d must exist and be owner-only", index)
	}
	resolved, err := filepath.EvalSymlinks(entry.UserDataDir)
	if err != nil || resolved != entry.UserDataDir {
		return ProfileRegistryEntry{}, fmt.Errorf("browser Profile directory %d must be a canonical non-symlink path", index)
	}
	return entry, nil
}

func acquireRegistryMutationLock(path string) (*os.File, error) {
	directory := filepath.Dir(path)
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("browser Profile registry directory must exist and not be group/world writable: path=%q mode=%v err=%v",
			directory, func() os.FileMode {
				if info == nil {
					return 0
				}
				return info.Mode().Perm()
			}(), err)
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return nil, errors.New("browser Profile registry directory must be canonical and non-symlink")
	}
	lockFile, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open browser Profile registry mutation lock: %w", err)
	}
	if err := lockFile.Chmod(0o600); err != nil {
		_ = lockFile.Close()
		return nil, err
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("lock browser Profile registry mutation: %w", err)
	}
	return lockFile, nil
}

func releaseRegistryMutationLock(lockFile *os.File) {
	if lockFile != nil {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}
}

func writeProfileRegistryDocument(path string, document profileRegistryDocument) error {
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode browser Profile registry: %w", err)
	}
	raw = append(raw, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".atoll-profile-registry-*")
	if err != nil {
		return fmt.Errorf("create browser Profile registry replacement: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish browser Profile registry replacement: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err == nil {
		err = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return err
}

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func parseProfileReference(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "profile" || parsed.Host != "recruiting" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" || parsed.Path == "/" {
		return "", errors.New("browser Profile reference is invalid")
	}
	escaped := strings.TrimPrefix(parsed.EscapedPath(), "/")
	profileID, err := url.PathUnescape(escaped)
	if err != nil || profileID == "" || strings.TrimSpace(profileID) != profileID || len(profileID) > 191 ||
		strings.ContainsAny(profileID, "\x00\r\n\t") {
		return "", errors.New("browser Profile reference identity is invalid")
	}
	return profileID, nil
}
