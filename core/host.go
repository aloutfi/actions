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
	// Azure DevOps: conservative until a real PR verifies the dialect.
	//  - MaxComment: PR thread comments are reported (unverified) to take
	//    about 150,000 characters; 140,000 leaves a margin.
	//  - MaxSummary: no documented limit for ##vso[task.uploadsummary]
	//    (unverified). Half of GitHub's 1 MiB keeps the Extensions tab quick
	//    to load; the summary falls back to its minimal layout past it.
	//  - Details, DiffFence: whether <details> and ```diff render is
	//    unverified, so headings and plain fences.
	//  - HiddenHTML: the marker and record live in thread properties.
	{Name: "azure", MaxComment: 140000, MaxSummary: 512 << 10},
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
