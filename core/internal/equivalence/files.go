package equivalence

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const maxBundleBytes = 256 << 20

// Save creates a new private directory and never overwrites an artifact. A
// partial artifact on error is retained as private, incomplete evidence.
func Save(directory string, b Bundle) error {
	data, err := json.Marshal(b)
	if err != nil || len(data) > maxBundleBytes {
		return errors.New("cannot encode bounded evidence bundle")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return errors.New("evidence directory must be new and creatable")
	}
	f, err := os.OpenFile(filepath.Join(directory, "bundle.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot create private evidence file")
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("evidence write did not complete")
	}
	return nil
}

// Load refuses non-private files and unrecognized fields. Error text never
// includes artifact contents, which contain live credentials and tokens.
func Load(path string) (Bundle, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxBundleBytes {
		return Bundle{}, errors.New("evidence must be a bounded private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return Bundle{}, errors.New("cannot open evidence")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return Bundle{}, errors.New("evidence identity changed")
	}
	dec := json.NewDecoder(io.LimitReader(f, maxBundleBytes+1))
	dec.DisallowUnknownFields()
	var b Bundle
	if err := dec.Decode(&b); err != nil {
		return Bundle{}, errors.New("invalid evidence encoding")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Bundle{}, errors.New("unexpected trailing evidence")
	}
	return b, nil
}
