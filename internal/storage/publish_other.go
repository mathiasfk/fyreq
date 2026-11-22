//go:build !darwin && !linux && !windows

package storage

import (
	"fmt"
	"os"
)

func publicationSupported() error {
	return fmt.Errorf("catalog publication is not supported on this platform")
}

func publish(source, destination, directory string) (bool, error) {
	return false, publicationSupported()
}

func syncDirectory(string) error { return publicationSupported() }

func openCatalogFile(path string) (*os.File, error) { return os.Open(path) }

func removeStale(path string) error { return os.Remove(path) }

func segmentRetiring(error) bool { return false }
