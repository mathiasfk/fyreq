//go:build !darwin

package storage

import "fmt"

func publicationSupported() error {
	return fmt.Errorf("catalog publication is not supported on this platform")
}

func publish(source, destination, directory string) (bool, error) {
	return false, publicationSupported()
}
