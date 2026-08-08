// Package version holds the build version, overridable at link time:
//
//	go build -ldflags "-X github.com/orkcom-tech/cogitorium/internal/version.Version=v0.1.0"
package version

var Version = "0.0.0-dev"
