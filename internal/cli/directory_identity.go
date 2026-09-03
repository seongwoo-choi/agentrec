package cli

import (
	"fmt"
	"os"
)

func requireOpenDirectoryAt(parent *os.Root, name string, held *os.Root) error {
	current, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	opened, err := held.Lstat(".")
	if err != nil {
		return err
	}
	if !current.IsDir() || !opened.IsDir() || !os.SameFile(current, opened) {
		return fmt.Errorf("cli: directory %q changed after it was opened", name)
	}
	return nil
}
