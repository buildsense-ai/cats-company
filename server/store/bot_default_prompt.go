package store

import (
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

// ShouldReplaceBotDefaultPrompt prevents an older XiaoBa installation from
// replacing a snapshot reported by a newer one. Unknown and equal versions
// remain compatible with clients that predate versioned reporting.
func ShouldReplaceBotDefaultPrompt(
	current *types.BotDefaultPromptSnapshot,
	incoming types.BotDefaultPromptSnapshot,
) bool {
	if current == nil {
		return true
	}
	currentVersion, currentOK := parsePromptSemver(current.XiaoBaVersion)
	incomingVersion, incomingOK := parsePromptSemver(incoming.XiaoBaVersion)
	if !currentOK || !incomingOK {
		return true
	}
	return comparePromptSemver(incomingVersion, currentVersion) >= 0
}

type promptSemver struct {
	major      uint64
	minor      uint64
	patch      uint64
	prerelease []string
}

func parsePromptSemver(value string) (promptSemver, bool) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "v") {
		value = value[1:]
	}
	if build := strings.IndexByte(value, '+'); build >= 0 {
		if !validSemverIdentifiers(value[build+1:], false) {
			return promptSemver{}, false
		}
		value = value[:build]
	}
	var prerelease []string
	if separator := strings.IndexByte(value, '-'); separator >= 0 {
		candidate := value[separator+1:]
		if !validSemverIdentifiers(candidate, true) {
			return promptSemver{}, false
		}
		prerelease = strings.Split(candidate, ".")
		value = value[:separator]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return promptSemver{}, false
	}
	numbers := make([]uint64, 3)
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return promptSemver{}, false
		}
		parsed, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return promptSemver{}, false
		}
		numbers[index] = parsed
	}
	return promptSemver{
		major: numbers[0], minor: numbers[1], patch: numbers[2], prerelease: prerelease,
	}, true
}

func validSemverIdentifiers(value string, prerelease bool) bool {
	parts := strings.Split(value, ".")
	for _, part := range parts {
		if part == "" {
			return false
		}
		numeric := true
		for _, character := range []byte(part) {
			if character < '0' || character > '9' {
				numeric = false
			}
			if (character < '0' || character > '9') &&
				(character < 'A' || character > 'Z') &&
				(character < 'a' || character > 'z') &&
				character != '-' {
				return false
			}
		}
		if prerelease && numeric && len(part) > 1 && part[0] == '0' {
			return false
		}
	}
	return true
}

func comparePromptSemver(left, right promptSemver) int {
	for _, values := range [][2]uint64{
		{left.major, right.major},
		{left.minor, right.minor},
		{left.patch, right.patch},
	} {
		if values[0] < values[1] {
			return -1
		}
		if values[0] > values[1] {
			return 1
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) == 0 {
		return 0
	}
	if len(left.prerelease) == 0 {
		return 1
	}
	if len(right.prerelease) == 0 {
		return -1
	}
	for index := 0; index < len(left.prerelease) && index < len(right.prerelease); index++ {
		leftPart, rightPart := left.prerelease[index], right.prerelease[index]
		leftNumeric := numericSemverIdentifier(leftPart)
		rightNumeric := numericSemverIdentifier(rightPart)
		switch {
		case leftNumeric && rightNumeric:
			if compared := compareNumericSemverIdentifier(leftPart, rightPart); compared != 0 {
				return compared
			}
		case leftNumeric && !rightNumeric:
			return -1
		case !leftNumeric && rightNumeric:
			return 1
		case !leftNumeric && !rightNumeric && leftPart < rightPart:
			return -1
		case !leftNumeric && !rightNumeric && leftPart > rightPart:
			return 1
		}
	}
	if len(left.prerelease) < len(right.prerelease) {
		return -1
	}
	if len(left.prerelease) > len(right.prerelease) {
		return 1
	}
	return 0
}

func numericSemverIdentifier(value string) bool {
	for _, character := range []byte(value) {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}

func compareNumericSemverIdentifier(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}
