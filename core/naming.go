package core

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// ArtifactName is the planfile artifact name for a stack and workspace:
// tfplan-<slug>-<workspace>. Plan (upload) and apply (download) must agree.
func ArtifactName(stack, workspace string) string {
	// Slug: / and . become -, anything else that is not [A-Za-z0-9-] is
	// dropped, dash runs collapse, and the ends are trimmed.
	var b strings.Builder
	for _, c := range []byte(stack) {
		switch {
		case c == '/' || c == '.':
			c = '-'
		case !isAlnum(c) && c != '-':
			continue
		}
		if c == '-' && strings.HasSuffix(b.String(), "-") {
			continue
		}
		b.WriteByte(c)
	}
	slug := strings.TrimPrefix(strings.TrimSuffix(b.String(), "-"), "-")
	return "tfplan-" + slug + "-" + orDefault(workspace)
}

// CleanStack is the one spelling of a stack path that tfpr uses everywhere
// (comment markers and labels, plan metadata, artifact names, the planfile
// match): "infra", "infra/" and "./infra" are all "infra".
func CleanStack(s string) string { return path.Clean(filepath.ToSlash(s)) }

// PlanMarker opens the plan comment for a stack and workspace; the upsert
// finds its own comment by this prefix.
func PlanMarker(stack, workspace string) string {
	return fmt.Sprintf("<!-- tf-plan:%s :: %s -->", stack, orDefault(workspace))
}

// ApplyMarker opens the apply-result comment for a stack and workspace.
func ApplyMarker(stack, workspace string) string {
	return fmt.Sprintf("<!-- tf-apply:%s :: %s -->", stack, orDefault(workspace))
}

func orDefault(workspace string) string {
	if workspace == "" {
		return "default"
	}
	return workspace
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
