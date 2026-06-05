package version

var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildTime = "unknown"
)

// Info contains version metadata injected at build time.
type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"gitCommit"`
	BuildTime string `json:"buildTime"`
}

// Get returns the current version info.
func Get() Info {
	return Info{
		Version:   Version,
		GitCommit: GitCommit,
		BuildTime: BuildTime,
	}
}
