package storage

import "os"

func publicationSupported() error { return nil }

// publish atomically replaces the catalog and confirms the directory update.
func publish(source, destination, directory string) (bool, error) {
	if err := os.Rename(source, destination); err != nil {
		return false, err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return true, err
	}
	defer dir.Close()
	return true, dir.Sync()
}
