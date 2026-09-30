//go:build linux || darwin

package threexui

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openOwnershipStoreDirectory(path string, create bool) (*os.File, error) {
	if path == "" {
		return nil, errors.New("ownership directory is unavailable")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("ownership directory is unavailable")
	}
	if create {
		// Creation is one-shot. An existing directory is a fail-closed
		// tombstone even when its state file is missing.
		if err := os.Mkdir(absolute, 0o700); err != nil {
			return nil, errors.New("ownership directory is unavailable")
		}
		if err := syncOwnershipParentDirectory(absolute); err != nil {
			// Keep the newly created directory as a fail-closed tombstone.
			return nil, err
		}
	}
	fd, err := unix.Open(absolute, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("ownership directory is unavailable")
	}
	directory := os.NewFile(uintptr(fd), "ownership-state-directory")
	if directory == nil {
		_ = unix.Close(fd)
		return nil, errors.New("ownership directory is unavailable")
	}
	if err := validateOwnershipDirectory(directory); err != nil {
		_ = directory.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = directory.Close()
		return nil, errors.New("ownership directory is already locked")
	}
	return directory, nil
}

func readOwnershipStateFile(directory *os.File) ([]byte, ownershipFileIdentity, error) {
	if err := validateOwnershipDirectory(directory); err != nil {
		return nil, ownershipFileIdentity{}, err
	}
	file, identity, size, err := openOwnershipStateFile(directory)
	if err != nil {
		return nil, ownershipFileIdentity{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxOwnershipStateBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) != size || len(data) > maxOwnershipStateBytes {
		return nil, ownershipFileIdentity{}, errors.New("ownership state cannot be read")
	}
	// Confirm the directory entry still names the inode we read. This prevents
	// accepting a state that was replaced during the read.
	check, checkIdentity, _, err := openOwnershipStateFile(directory)
	if err != nil {
		return nil, ownershipFileIdentity{}, err
	}
	_ = check.Close()
	if checkIdentity != identity {
		return nil, ownershipFileIdentity{}, errors.New("ownership state changed during read")
	}
	return data, identity, nil
}

func writeOwnershipStateFile(directory *os.File, expected ownershipFileIdentity, expectedHash string, data []byte) (ownershipFileIdentity, error) {
	if len(data) == 0 || len(data) > maxOwnershipStateBytes {
		return ownershipFileIdentity{}, errors.New("ownership state has invalid size")
	}
	if err := validateOwnershipDirectory(directory); err != nil {
		return ownershipFileIdentity{}, err
	}
	if err := verifyOwnershipStateSnapshot(directory, expected, expectedHash); err != nil {
		return ownershipFileIdentity{}, err
	}
	temporaryName, temporary, err := createOwnershipTemporaryFile(directory)
	if err != nil {
		return ownershipFileIdentity{}, err
	}
	renamed := false
	defer func() {
		_ = temporary.Close()
		if !renamed {
			_ = unix.Unlinkat(int(directory.Fd()), temporaryName, 0)
		}
	}()
	if err := writeOwnershipBytes(temporary, data); err != nil {
		return ownershipFileIdentity{}, err
	}
	if err := temporary.Sync(); err != nil {
		return ownershipFileIdentity{}, errors.New("ownership state cannot be synchronized")
	}
	var temporaryStat unix.Stat_t
	if err := unix.Fstat(int(temporary.Fd()), &temporaryStat); err != nil || !validOwnershipFileStat(temporaryStat) ||
		temporaryStat.Size != int64(len(data)) {
		return ownershipFileIdentity{}, errors.New("ownership temporary file changed")
	}
	temporaryIdentity := ownershipIdentity(temporaryStat)
	if err := temporary.Close(); err != nil {
		return ownershipFileIdentity{}, errors.New("ownership state cannot be closed")
	}
	// Recheck immediately before replacement. The directory lifetime lock only
	// coordinates this component; an external writer is not assumed to honor it.
	if err := verifyOwnershipStateSnapshot(directory, expected, expectedHash); err != nil {
		return ownershipFileIdentity{}, err
	}
	if err := verifyOwnershipTemporaryIdentity(directory, temporaryName, temporaryIdentity, int64(len(data))); err != nil {
		return ownershipFileIdentity{}, err
	}
	if err := unix.Renameat(int(directory.Fd()), temporaryName, int(directory.Fd()), ownershipStoreFilename); err != nil {
		return ownershipFileIdentity{}, errors.New("ownership state cannot be replaced")
	}
	renamed = true
	if err := directory.Sync(); err != nil {
		return ownershipFileIdentity{}, errors.New("ownership directory cannot be synchronized")
	}
	written, identity, err := readOwnershipStateFile(directory)
	if err != nil {
		return ownershipFileIdentity{}, err
	}
	if identity != temporaryIdentity || updateDigest(string(written)) != updateDigest(string(data)) {
		return ownershipFileIdentity{}, errors.New("ownership state changed after replacement")
	}
	return identity, nil
}

func validateOwnershipDirectory(directory *os.File) error {
	if directory == nil {
		return errors.New("ownership directory is unavailable")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Mode&0o7777 != 0o700 || stat.Uid != uint32(unix.Geteuid()) {
		return errors.New("ownership directory must be private")
	}
	return nil
}

func openOwnershipStateFile(directory *os.File) (*os.File, ownershipFileIdentity, int64, error) {
	before, exists, err := inspectOwnershipStateEntry(directory)
	if err != nil || !exists {
		return nil, ownershipFileIdentity{}, 0, errors.New("ownership state is unavailable")
	}
	fd, err := unix.Openat(int(directory.Fd()), ownershipStoreFilename,
		unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ownershipFileIdentity{}, 0, errors.New("ownership state is unavailable")
	}
	file := os.NewFile(uintptr(fd), "ownership-state")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ownershipFileIdentity{}, 0, errors.New("ownership state is unavailable")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || !validOwnershipFileStat(stat) || stat.Size <= 0 || stat.Size > maxOwnershipStateBytes {
		_ = file.Close()
		return nil, ownershipFileIdentity{}, 0, errors.New("ownership state must be a private regular file")
	}
	after := ownershipIdentity(stat)
	if after != before {
		_ = file.Close()
		return nil, ownershipFileIdentity{}, 0, errors.New("ownership state changed during open")
	}
	return file, after, stat.Size, nil
}

func verifyOwnershipStateSnapshot(directory *os.File, expected ownershipFileIdentity, expectedHash string) error {
	if err := validateOwnershipDirectory(directory); err != nil {
		return err
	}
	entry, exists, err := inspectOwnershipStateEntry(directory)
	if err != nil {
		return err
	}
	if !exists {
		if expected.Valid {
			return errors.New("ownership state disappeared")
		}
		return nil
	}
	if !expected.Valid || entry != expected {
		return errors.New("ownership state identity changed")
	}
	fd, err := unix.Openat(int(directory.Fd()), ownershipStoreFilename,
		unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("ownership state is unavailable")
	}
	file := os.NewFile(uintptr(fd), "ownership-state-check")
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("ownership state is unavailable")
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || !validOwnershipFileStat(stat) || stat.Size <= 0 || stat.Size > maxOwnershipStateBytes {
		return errors.New("ownership state must be a private regular file")
	}
	actual := ownershipIdentity(stat)
	if actual != expected {
		return errors.New("ownership state identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxOwnershipStateBytes+1))
	if err != nil || int64(len(data)) != stat.Size || !ownershipHex(expectedHash, 32) || updateDigest(string(data)) != expectedHash {
		return errors.New("ownership state content changed")
	}
	return nil
}

func inspectOwnershipStateEntry(directory *os.File) (ownershipFileIdentity, bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(directory.Fd()), ownershipStoreFilename, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return ownershipFileIdentity{}, false, nil
	}
	if err != nil {
		return ownershipFileIdentity{}, false, errors.New("ownership state is unavailable")
	}
	if !validOwnershipFileStat(stat) ||
		stat.Size <= 0 || stat.Size > maxOwnershipStateBytes {
		return ownershipFileIdentity{}, false, errors.New("ownership state must be a private regular file")
	}
	return ownershipIdentity(stat), true, nil
}

func verifyOwnershipTemporaryIdentity(directory *os.File, name string, expected ownershipFileIdentity, size int64) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		!validOwnershipFileStat(stat) || stat.Size != size || ownershipIdentity(stat) != expected {
		return errors.New("ownership temporary file changed")
	}
	return nil
}

func validOwnershipFileStat(stat unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0o7777 == 0o600 && stat.Nlink == 1 &&
		stat.Uid == uint32(unix.Geteuid())
}

func syncOwnershipParentDirectory(path string) error {
	fd, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("ownership parent directory is unavailable")
	}
	parent := os.NewFile(uintptr(fd), "ownership-state-parent")
	if parent == nil {
		_ = unix.Close(fd)
		return errors.New("ownership parent directory is unavailable")
	}
	syncErr := parent.Sync()
	closeErr := parent.Close()
	if syncErr != nil || closeErr != nil {
		return errors.New("ownership parent directory cannot be synchronized")
	}
	return nil
}

func createOwnershipTemporaryFile(directory *os.File) (string, *os.File, error) {
	for attempt := 0; attempt < 16; attempt++ {
		random := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, random); err != nil {
			return "", nil, errors.New("ownership temporary name unavailable")
		}
		name := ".ownership-state-" + hex.EncodeToString(random) + ".tmp"
		fd, err := unix.Openat(int(directory.Fd()), name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, errors.New("ownership temporary file unavailable")
		}
		if err := unix.Fchmod(fd, 0o600); err != nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
			return "", nil, errors.New("ownership temporary file cannot be secured")
		}
		file := os.NewFile(uintptr(fd), "ownership-state-temporary")
		if file == nil {
			_ = unix.Close(fd)
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
			return "", nil, errors.New("ownership temporary file unavailable")
		}
		return name, file, nil
	}
	return "", nil, errors.New("ownership temporary file unavailable")
}

func writeOwnershipBytes(file *os.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil || written <= 0 {
			return errors.New("ownership state cannot be written")
		}
		data = data[written:]
	}
	return nil
}

func ownershipIdentity(stat unix.Stat_t) ownershipFileIdentity {
	return ownershipFileIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Valid: true}
}
