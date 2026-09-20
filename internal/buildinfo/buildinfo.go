package buildinfo

import (
	debugbuildinfo "debug/buildinfo"
	"errors"
	"runtime/debug"
	"strings"
)

type Identity struct {
	ModuleVersion string
	Revision      string
	Modified      bool
	Known         bool
}

func Running() Identity {
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return Identity{}
	}
	return From(info)
}

func ReadFile(path string) (Identity, error) {
	if strings.TrimSpace(path) == "" {
		return Identity{}, errors.New("executable path is empty")
	}
	info, err := debugbuildinfo.ReadFile(path)
	if err != nil {
		return Identity{}, err
	}
	return From(info), nil
}

func From(info *debug.BuildInfo) Identity {
	if info == nil {
		return Identity{}
	}
	identity := Identity{ModuleVersion: normalize(info.Main.Version)}
	if identity.ModuleVersion == "(devel)" {
		identity.ModuleVersion = ""
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			identity.Revision = normalize(setting.Value)
		case "vcs.modified":
			identity.Modified = strings.EqualFold(strings.TrimSpace(setting.Value), "true")
		}
	}
	identity.Known = identity.Revision != "" || identity.ModuleVersion != ""
	return identity
}

func normalize(value string) string {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "\r\n\x00") || len(value) > 256 {
		return ""
	}
	return value
}

func (i Identity) Fingerprint() string {
	value := i.Revision
	if value == "" {
		value = i.ModuleVersion
	}
	if value == "" {
		return "unknown"
	}
	if i.Modified {
		return value + "+modified"
	}
	return value
}

func (i Identity) Same(other Identity) bool {
	if !i.Known || !other.Known {
		return false
	}
	if i.Revision != "" && other.Revision != "" {
		return i.Revision == other.Revision && i.Modified == other.Modified
	}
	return i.ModuleVersion != "" && i.ModuleVersion == other.ModuleVersion && i.Modified == other.Modified
}
