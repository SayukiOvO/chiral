//go:build !linux && !darwin

package threexui

import (
	"errors"
	"os"
)

func openOwnershipStoreDirectory(string, bool) (*os.File, error) {
	return nil, errors.New("durable 3x-ui ownership state is unsupported on this platform")
}

func readOwnershipStateFile(*os.File) ([]byte, ownershipFileIdentity, error) {
	return nil, ownershipFileIdentity{}, errors.New("durable 3x-ui ownership state is unsupported on this platform")
}

func writeOwnershipStateFile(*os.File, ownershipFileIdentity, string, []byte) (ownershipFileIdentity, error) {
	return ownershipFileIdentity{}, errors.New("durable 3x-ui ownership state is unsupported on this platform")
}

func verifyOwnershipStateSnapshot(*os.File, ownershipFileIdentity, string) error {
	return errors.New("durable 3x-ui ownership state is unsupported on this platform")
}
