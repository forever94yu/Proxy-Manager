package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Version is the Proxy Manager release version. It is bumped together with
// package.json for every release, so source and Docker builds report it too.
var Version = "1.4.0"

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(?:-([0-9A-Za-z.-]{1,40}))?$`)

type semanticVersion struct {
	core       [3]int
	prerelease string
}

func parseVersion(value string) (semanticVersion, bool) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return semanticVersion{}, false
	}
	var parsed semanticVersion
	for index := range parsed.core {
		parsed.core[index], _ = strconv.Atoi(match[index+1])
	}
	parsed.prerelease = match[4]
	return parsed, true
}

// normalizeVersion turns a release tag such as v1.4.0 into 1.4.0. It returns
// an empty string for values that are not semantic versions.
func normalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	if _, ok := parseVersion(value); !ok {
		return ""
	}
	return strings.TrimPrefix(value, "v")
}

// compareVersions orders two semantic versions; unparsable values sort first.
// A pre-release sorts before its release, and pre-releases compare as strings.
func compareVersions(left, right string) int {
	a, leftOK := parseVersion(left)
	b, rightOK := parseVersion(right)
	switch {
	case !leftOK && !rightOK:
		return 0
	case !leftOK:
		return -1
	case !rightOK:
		return 1
	}
	for index := range a.core {
		if a.core[index] != b.core[index] {
			if a.core[index] < b.core[index] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.prerelease == b.prerelease:
		return 0
	case a.prerelease == "":
		return 1
	case b.prerelease == "":
		return -1
	}
	return strings.Compare(a.prerelease, b.prerelease)
}
