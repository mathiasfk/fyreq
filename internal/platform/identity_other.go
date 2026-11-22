//go:build !darwin && !linux && !windows

package platform

import "io/fs"

// Other platforms can collect portable metadata, but native identity and full
// platform support are not implemented yet.
func nativeMetadata(_ string, _ fs.FileInfo) (string, *string, error) { return "", nil, nil }
