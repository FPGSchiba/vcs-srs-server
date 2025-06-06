package srs

func checkVersion(version string) bool {
	// Check if the version is supported
	// For now, we assume all versions are supported
	return version == "v0.1.0"
}
