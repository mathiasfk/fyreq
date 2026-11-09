//go:build !darwin

package platform

import "io/fs"

// Other platforms can collect portable metadata, but native identity and full
// platform support are not implemented yet.
func identity(info fs.FileInfo) string { return "" }
