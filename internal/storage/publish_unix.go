//go:build darwin || linux

package storage

import "os"

func publicationSupported() error { return nil }

// publish atomically replaces the manifest and confirms the directory update.
func publish(source, destination, directory string) (bool, error) {
	if err := os.Rename(source, destination); err != nil {
		return false, err
	}
	return true, syncDirectory(directory)
}

// syncDirectory makes created or renamed directory entries durable.
func syncDirectory(directory string) error {
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
