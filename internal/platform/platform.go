package platform

import (
	"fmt"
	"runtime"
)

const unsupportedMessage = "herdr-tandem supports only Apple Silicon macOS (darwin/arm64)"

// Validate enforces herdr-tandem's intentionally narrow runtime support contract.
func Validate(goos, goarch string) error {
	if goos != "darwin" || goarch != "arm64" {
		return fmt.Errorf("%s", unsupportedMessage)
	}
	return nil
}

// Current validates the running binary's platform.
func Current() error { return Validate(runtime.GOOS, runtime.GOARCH) }
