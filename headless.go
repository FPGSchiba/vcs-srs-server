//go:build headless

package main

import "embed"

// Empty FS when not embedding
var assets embed.FS
