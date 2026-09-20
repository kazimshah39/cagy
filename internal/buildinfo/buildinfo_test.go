package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestBuildIdentityFromRevision(t *testing.T) {
	identity := From(&debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abcdef"}, {Key: "vcs.modified", Value: "true"},
	}})
	if !identity.Known || identity.Revision != "abcdef" || identity.ModuleVersion != "v1.2.3" || !identity.Modified || identity.Fingerprint() != "abcdef+modified" {
		t.Fatalf("identity=%+v fingerprint=%q", identity, identity.Fingerprint())
	}
}

func TestBuildIdentityUnknownAndComparison(t *testing.T) {
	unknown := From(&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}})
	if unknown.Known || unknown.Fingerprint() != "unknown" {
		t.Fatalf("unknown=%+v", unknown)
	}
	one := Identity{Revision: "one", Known: true}
	if !one.Same(Identity{Revision: "one", Known: true}) || one.Same(Identity{Revision: "two", Known: true}) || one.Same(unknown) {
		t.Fatal("unexpected identity comparison")
	}
}

func TestBuildIdentityRejectsUnsafeMetadata(t *testing.T) {
	identity := From(&debug.BuildInfo{Main: debug.Module{Version: "bad\nversion"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "bad\x00revision"}}})
	if identity.Known {
		t.Fatalf("unsafe identity accepted: %+v", identity)
	}
}
