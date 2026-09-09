package quickopen

import (
	"fmt"
	"os"
)

// writeStub creates an empty file where a test wants one to exist. It is here
// rather than inline so that the one os.WriteFile in the tests has the one
// place to explain its mode: nothing runs this file, it only has to be found.
func writeStub(path string) error {
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
