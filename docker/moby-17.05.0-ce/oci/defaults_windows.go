package oci

import (
	"runtime"

	"github.com/opencontainers/runtime-spec/specs-go"
)

// DefaultSpec returns default spec used by docker.
//containerStart->createSpec中执行
func DefaultSpec() specs.Spec {
	return specs.Spec{
		Version: specs.Version,
		Platform: specs.Platform{
			OS:   runtime.GOOS,
			Arch: runtime.GOARCH,
		},
		Windows: &specs.Windows{},
	}
}
