package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

var (
	Version   = "0.1.0"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if Commit == "unknown" && setting.Value != "" {
				Commit = setting.Value[:min(len(setting.Value), 12)]
			}
		case "vcs.time":
			if BuildTime == "unknown" {
				BuildTime = setting.Value
			}
		}
	}
}

func String() string {
	return fmt.Sprintf("llm-broker %s (%s %s/%s) commit=%s built=%s",
		Version, runtime.Version(), runtime.GOOS, runtime.GOARCH, Commit, BuildTime)
}

func Short() string {
	return fmt.Sprintf("llm-broker %s", Version)
}
