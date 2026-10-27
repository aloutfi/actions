package core

// Host is what one platform's markdown can carry. The same row serves the
// repo host (comment limit and dialect) and the CI of the same name (job
// summary limit). Hosts are data, not types: supporting a new one is one row
// in hosts.
type Host struct {
	Name       string
	MaxComment int  // characters (runes) in one PR comment
	MaxSummary int  // bytes in one job summary
	Details    bool // <details>/<summary> renders as a collapsible block
	DiffFence  bool // a ```diff fence colours + and - lines
	HiddenHTML bool // <!-- --> comments are invisible
}

// hosts is the dialect table. The first row is the default for names it
// does not list (local runs, "none").
var hosts = []Host{
	{Name: "github", MaxComment: 65536, MaxSummary: 1 << 20, Details: true, DiffFence: true, HiddenHTML: true},
}

// HostFor is the row for name, or the default row.
func HostFor(name string) Host {
	for _, h := range hosts {
		if h.Name == name {
			return h
		}
	}
	return hosts[0]
}
