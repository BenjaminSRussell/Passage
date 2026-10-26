package domain

import "strings"

const (
	SourceTranslation = "translation"
	SourceBase        = "base"
)

func ResolveName(base string, translations map[string]string, tag string) (name, language, source string) {
	if name, ok := translations[tag]; ok && tag != "" {
		return name, tag, SourceTranslation
	}
	if primary, _, hasRegion := strings.Cut(tag, "-"); hasRegion {
		if name, ok := translations[primary]; ok {
			return name, primary, SourceTranslation
		}
	}
	return base, "", SourceBase
}
