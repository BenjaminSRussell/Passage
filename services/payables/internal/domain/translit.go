package domain

import (
	"strconv"
	"strings"
)

var transliterations = buildTransliterations(
	"àa áa âa ãa äa åa æae çc èe ée êe ëe ìi íi îi ïi ñn òo óo ôo õo öo øo œoe ùu úu ûu üu ýy ÿy " +
		"ąa ćc ęe łl ńn śs źz żz čc ďd ěe ňn řr šs ťt ůu žz ðd þth ßss",
)

func buildTransliterations(table string) map[rune]string {
	m := map[rune]string{}
	for _, entry := range strings.Fields(table) {
		runes := []rune(entry)
		from, to := runes[0], string(runes[1:])
		m[from] = to
		if upper := []rune(strings.ToUpper(string(from)))[0]; upper != from {
			m[upper] = strings.ToUpper(to)
		}
	}
	return m
}

func inCharset(r rune, charset string) bool {
	if charset == CharsetASCII {
		return r >= 0x20 && r <= 0x7e
	}
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("/-?:().,'+ ", r)
}

func Transliterate(name, charset string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", &Error{Code: CodeFileInvalid, Detail: "name is empty"}
	}
	var out strings.Builder
	for i, r := range name {
		if mapped, ok := transliterations[r]; ok {
			out.WriteString(mapped)
			continue
		}
		if !inCharset(r, charset) {
			return "", &Error{Code: CodeFileInvalid, Detail: "name has a character outside the " + charset + " charset at byte " + strconv.Itoa(i)}
		}
		out.WriteRune(r)
	}
	return out.String(), nil
}
