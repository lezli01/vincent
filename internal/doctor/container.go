package doctor

import (
	"context"
	"runtime"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/container"
)

// Container is the §14 row for container execution (§16, task 061). It sits
// in the adapters' half of the report and follows their rule exactly: it
// **reports and does not change the exit code**. The Problems set is closed
// (task 041 decision 4), and a machine with no docker is a healthy machine —
// containerization is off by default and every step runs on the host.
type Container struct {
	// Enabled is whether `container.image` names an image at all. False is
	// the default and means every step runs on the host.
	Enabled bool `json:"enabled"`
	// Image is the configured image, empty when containerization is off.
	Image string `json:"image,omitempty"`
	// Runtime is the configured binary, with the `docker` default applied.
	Runtime string `json:"runtime"`
	// Available is whether that binary answered. It is probed whether or not
	// containerization is enabled: "would this work if I turned it on" is the
	// question a user runs `vincent doctor` to answer.
	Available bool `json:"available"`
	// Supported is false on a Windows daemon, where a containerized task is
	// refused at creation (decision 2) — paths are identical inside and out,
	// and a Windows path cannot exist in a Linux container.
	Supported bool `json:"supported"`
	// Message explains a false Available or Supported, empty otherwise.
	Message string `json:"message,omitempty"`
}

// DetectContainer probes the configured runtime.
func DetectContainer(ctx context.Context, cfg config.Config) Container {
	c := cfg.Container
	row := Container{
		Enabled:   c.Enabled(),
		Image:     c.Image,
		Runtime:   c.RuntimeBinary(),
		Supported: runtime.GOOS != "windows",
	}
	if !row.Supported {
		row.Message = "containerized tasks are not supported on a windows daemon"
		return row
	}
	if err := container.New(c.RuntimeBinary()).Available(ctx); err != nil {
		row.Message = err.Error()
		return row
	}
	row.Available = true
	return row
}
