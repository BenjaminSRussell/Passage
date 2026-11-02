package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The read-only query guard. A sql binding may carry one configured SELECT that runs on the customer's ERP database.
// The connection should log in with a read-only account (see the README); this guard is the second line of defence
// and is deliberately an allowlist: it tokenizes the query once, accepts only constructs every supported engine
// (PostgreSQL, SQL Server, MySQL, Oracle) reads the same way, and REFUSES anything ambiguous, any statement other
// than one SELECT (optionally led by WITH), and any word that could write, lock, wait or reach outside the
// database. A refused query is never sent anywhere; the error says which construct was refused.

// WatermarkToken is the placeholder a query binding uses for the last watermark; it is bound as a parameter.
const WatermarkToken = ":watermark"

// maxQueryLen bounds a configured query.
const maxQueryLen = 8000

// watermarkMark stands for a :watermark token in the cleaned text. Control characters are refused in queries, so it
// cannot collide with query text.
const watermarkMark = '\x00'

// refusedWords may not appear as a word anywhere outside string literals and quoted identifiers: statements and
// clauses that write, lock or change session state, and functions with side effects outside the result set.
var refusedWords = map[string]bool{
	// statements and clauses that write or change state
	"insert": true, "update": true, "delete": true, "merge": true, "upsert": true, "drop": true, "alter": true,
	"create": true, "truncate": true, "grant": true, "revoke": true, "deny": true, "exec": true, "execute": true,
	"call": true, "do": true, "into": true, "copy": true, "set": true, "declare": true, "begin": true,
	"commit": true, "rollback": true, "savepoint": true, "lock": true, "unlock": true, "vacuum": true,
	"analyze": true, "reindex": true, "cluster": true, "refresh": true, "discard": true, "checkpoint": true,
	"notify": true, "listen": true, "pragma": true, "attach": true, "detach": true, "load": true, "handler": true,
	"prepare": true, "deallocate": true, "kill": true, "shutdown": true, "reconfigure": true, "backup": true,
	"restore": true, "bulk": true, "procedure": true, "uescape": true,
	// file output
	"outfile": true, "dumpfile": true,
	// SQL Server lock hints
	"updlock": true, "xlock": true, "tablock": true, "tablockx": true, "holdlock": true, "rowlock": true,
	"paglock": true,
	// remote and ad hoc data sources
	"openquery": true, "opendatasource": true, "openrowset": true, "httpuritype": true,
	// waits, locks and file reads
	"waitfor": true, "sleep": true, "benchmark": true, "get_lock": true, "release_lock": true,
	"release_all_locks": true, "load_file": true, "master_pos_wait": true, "source_pos_wait": true,
	// session and sequence changes
	"set_config": true, "nextval": true, "setval": true,
}

// refusedPrefixes may not start a word: system procedure and package families of the supported engines.
var refusedPrefixes = []string{"xp_", "sp_", "pg_", "lo_", "dbms_", "utl_", "dblink", "query_to_", "cursor_to_", "wait_for_", "wait_until_"}

func refusedWord(w string) error {
	lw := strings.ToLower(w)
	if refusedWords[lw] {
		return fmt.Errorf("may not use %s: only a read-only SELECT is allowed", strings.ToUpper(lw))
	}
	for _, p := range refusedPrefixes {
		if strings.HasPrefix(lw, p) {
			return fmt.Errorf("may not use %s (%s… names system procedures and packages): only a read-only SELECT is allowed", strings.ToUpper(lw), strings.ToUpper(p))
		}
	}
	return nil
}

// ReadOnlySelect is a configured query that passed the guard, with comments removed and :watermark tokens marked.
type ReadOnlySelect struct {
	cleaned string // the statement without comments or trailing semicolons; watermarkMark marks each :watermark
	mainAt  int    // byte offset in cleaned of the main SELECT (0 when there is no WITH)
}

// ValidateSelect reports whether q is one read-only SELECT the guard accepts.
func ValidateSelect(q string) error {
	_, err := ParseSelect(q)
	return err
}

type tokKind int

const (
	tokWord tokKind = iota
	tokNumber
	tokString
	tokQuotedIdent
	tokPunct
	tokWatermark
)

// ParseSelect runs the guard over q and returns the accepted statement, or an error naming the refused construct.
func ParseSelect(q string) (ReadOnlySelect, error) {
	if strings.TrimSpace(q) == "" {
		return ReadOnlySelect{}, errors.New("is empty")
	}
	if len(q) > maxQueryLen {
		return ReadOnlySelect{}, fmt.Errorf("is longer than %d characters", maxQueryLen)
	}
	if !utf8.ValidString(q) {
		return ReadOnlySelect{}, errors.New("is not valid UTF-8")
	}
	var (
		out       strings.Builder
		depth     int
		ended     bool    // a ';' was read: only whitespace, comments and more ';' may follow
		last      tokKind = -1
		lastWord  string  // the previous word, lowercased, when the previous token was a word
		first     = true
		withQuery bool
		mainAt    = -1
	)
	emit := func(kind tokKind, text string) error {
		if ended {
			return errors.New("must be one statement (nothing may follow ';')")
		}
		if first {
			first = false
			lw := strings.ToLower(text)
			switch {
			case kind == tokWord && lw == "select":
				mainAt = out.Len()
			case kind == tokWord && lw == "with":
				withQuery = true
			default:
				return errors.New("must be a single SELECT statement (optionally starting with WITH)")
			}
		} else if withQuery && mainAt < 0 && kind == tokWord && depth == 0 && strings.EqualFold(text, "select") {
			if last != tokPunct || !strings.HasSuffix(strings.TrimRight(out.String(), " \t\n\r"), ")") {
				return errors.New("must follow WITH with name AS (query) definitions and then the SELECT")
			}
			mainAt = out.Len()
		}
		if kind == tokWord {
			lw := strings.ToLower(text)
			if err := refusedWord(lw); err != nil {
				return err
			}
			if last == tokWord && lastWord == "for" && (lw == "share" || lw == "key" || lw == "no") {
				return errors.New("may not take row locks (FOR SHARE / FOR KEY SHARE / FOR NO KEY UPDATE)")
			}
			if last == tokWord && lastWord == "next" && lw == "value" {
				return errors.New("may not use NEXT VALUE FOR: it advances a sequence")
			}
			lastWord = lw
		} else {
			lastWord = ""
		}
		if kind == tokWatermark {
			out.WriteByte(watermarkMark)
		} else {
			out.WriteString(text)
		}
		last = kind
		return nil
	}

	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			out.WriteByte(c)
			i++
		case c == '\r':
			// A lone carriage return ends a line comment in some engines and not in others.
			if i+1 >= len(q) || q[i+1] != '\n' {
				return ReadOnlySelect{}, errors.New("may not contain a carriage return that is not followed by a line feed")
			}
			out.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			return ReadOnlySelect{}, errors.New("may not contain control characters")
		case c >= 0x80:
			return ReadOnlySelect{}, errors.New("may use non-ASCII characters only inside string literals or double-quoted names")

		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			// A line comment. MySQL needs whitespace after "--" to read it as a comment, so anything else is refused.
			if i+2 < len(q) && q[i+2] != ' ' && q[i+2] != '\t' && q[i+2] != '\n' && q[i+2] != '\r' {
				return ReadOnlySelect{}, errors.New("must have a space after -- (a line comment)")
			}
			j := i + 2
			for j < len(q) && q[j] != '\n' && q[j] != '\r' {
				if q[j] < 0x20 && q[j] != '\t' || q[j] >= 0x7f {
					return ReadOnlySelect{}, errors.New("may hold only plain ASCII text in comments")
				}
				j++
			}
			out.WriteByte(' ') // comments are dropped, so one cannot swallow the clauses the extractor appends
			i = j
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			end := strings.Index(q[i+2:], "*/")
			if end < 0 {
				return ReadOnlySelect{}, errors.New("has an unterminated comment")
			}
			body := q[i+2 : i+2+end]
			switch {
			case strings.HasPrefix(body, "!") || strings.HasPrefix(body, "M!"):
				return ReadOnlySelect{}, errors.New("may not use /*! */ comments: MySQL runs their content")
			case strings.Contains(body, "/*"):
				return ReadOnlySelect{}, errors.New("may not nest comments: engines disagree on where a nested comment ends")
			}
			for k := 0; k < len(body); k++ {
				if body[k] < 0x20 && body[k] != '\t' && body[k] != '\n' && body[k] != '\r' || body[k] >= 0x7f {
					return ReadOnlySelect{}, errors.New("may hold only plain ASCII text in comments")
				}
			}
			out.WriteByte(' ')
			i += 2 + end + 2
		case c == '#':
			return ReadOnlySelect{}, errors.New("may not use # (MySQL reads it as a comment); quote a name that contains it")

		case c == '\'':
			// A string literal: '' is the only escape. A backslash means something different per engine, so refused.
			j, err := scanQuoted(q, i, '\'')
			if err != nil {
				return ReadOnlySelect{}, err
			}
			if i > 0 && isWordByte(q[i-1]) && !strings.EqualFold(lastWordRaw(q, i), "n") {
				return ReadOnlySelect{}, errors.New("may not use prefixed string literals (E'', q'', X'', _charset'' and the like); only N'' is allowed")
			}
			if i > 0 && q[i-1] == '&' {
				return ReadOnlySelect{}, errors.New("may not use U&'' escapes")
			}
			if err := emit(tokString, q[i:j]); err != nil {
				return ReadOnlySelect{}, err
			}
			i = j
		case c == '"':
			// A double-quoted name (a string in MySQL's default mode; either way it is not code).
			j, err := scanQuoted(q, i, c)
			if err != nil {
				return ReadOnlySelect{}, err
			}
			if i > 0 && (isWordByte(q[i-1]) || q[i-1] == '&') {
				return ReadOnlySelect{}, errors.New("may not put a prefix directly before a quoted name")
			}
			if err := emit(tokQuotedIdent, q[i:j]); err != nil {
				return ReadOnlySelect{}, err
			}
			i = j
		case c == '`' || c == '[':
			// A MySQL `name` or a SQL Server [name]. PostgreSQL reads ` as an operator and [ ] as a subscript, so
			// for it the content is code: the content is held to name characters and checked like code.
			closer := byte('`')
			if c == '[' {
				closer = ']'
			}
			end := strings.IndexByte(q[i+1:], closer)
			if end < 0 {
				return ReadOnlySelect{}, fmt.Errorf("has an unterminated %cname%c", c, closer)
			}
			body := q[i+1 : i+1+end]
			j := i + 1 + end + 1
			if j < len(q) && q[j] == closer {
				return ReadOnlySelect{}, fmt.Errorf("may not use %c%c inside a %cname%c", closer, closer, c, closer)
			}
			if err := checkNameBody(body, c, closer); err != nil {
				return ReadOnlySelect{}, err
			}
			if err := emit(tokQuotedIdent, q[i:j]); err != nil {
				return ReadOnlySelect{}, err
			}
			i = j
		case c == ']':
			return ReadOnlySelect{}, errors.New("has a ] without a matching [")
		case c == '{' || c == '}':
			return ReadOnlySelect{}, errors.New("may not use { } (ODBC escape sequences)")
		case c == '\\':
			return ReadOnlySelect{}, errors.New("may not use a backslash")
		case c == '$':
			return ReadOnlySelect{}, errors.New("may not use $ outside a name (dollar quoting and positional parameters)")
		case c == '?' || c == '@':
			return ReadOnlySelect{}, fmt.Errorf("may not use %c (parameters and variables); use :watermark for the watermark", c)

		case c == ':':
			switch {
			case i+1 < len(q) && q[i+1] == ':':
				if err := emit(tokPunct, "::"); err != nil {
					return ReadOnlySelect{}, err
				}
				i += 2
			case strings.HasPrefix(q[i:], WatermarkToken) && (i+len(WatermarkToken) == len(q) || !isWordByte(q[i+len(WatermarkToken)])):
				if err := emit(tokWatermark, WatermarkToken); err != nil {
					return ReadOnlySelect{}, err
				}
				i += len(WatermarkToken)
			default:
				return ReadOnlySelect{}, errors.New("may use : only in :watermark or a :: cast")
			}
		case c == ';':
			ended = true
			i++
		case c == '(':
			if last == tokQuotedIdent {
				return ReadOnlySelect{}, errors.New("may not call a function through a quoted name")
			}
			depth++
			if err := emit(tokPunct, "("); err != nil {
				return ReadOnlySelect{}, err
			}
			i++
		case c == ')':
			depth--
			if depth < 0 {
				return ReadOnlySelect{}, errors.New("has a ) without a matching (")
			}
			if err := emit(tokPunct, ")"); err != nil {
				return ReadOnlySelect{}, err
			}
			i++
		case strings.IndexByte(",.*+-/%=<>!|&^~", c) >= 0:
			if err := emit(tokPunct, string(c)); err != nil {
				return ReadOnlySelect{}, err
			}
			i++
		case c >= '0' && c <= '9':
			j := scanNumber(q, i)
			if j < len(q) && (isWordByte(q[j]) || q[j] == '.' || q[j] == '\'' || q[j] == '"') {
				// Some engines end the number there and read the rest as a separate word.
				return ReadOnlySelect{}, errors.New("must separate a number from the word or quote after it")
			}
			if err := emit(tokNumber, q[i:j]); err != nil {
				return ReadOnlySelect{}, err
			}
			i = j
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(q) && isWordByte(q[j]) {
				j++
			}
			if err := emit(tokWord, q[i:j]); err != nil {
				return ReadOnlySelect{}, err
			}
			i = j
		default:
			return ReadOnlySelect{}, fmt.Errorf("may not use %q", c)
		}
	}
	if depth != 0 {
		return ReadOnlySelect{}, errors.New("has a ( without a matching )")
	}
	if first {
		return ReadOnlySelect{}, errors.New("must be a single SELECT statement")
	}
	if mainAt < 0 {
		return ReadOnlySelect{}, errors.New("must end a WITH query in a SELECT")
	}
	cleaned := out.String()
	lead := len(cleaned) - len(strings.TrimLeft(cleaned, " \t\n\r"))
	return ReadOnlySelect{cleaned: strings.TrimSpace(cleaned), mainAt: mainAt - lead}, nil
}

// checkNameBody holds the content of a `name` or [name] to letters, digits, spaces and _ $ # . - (no "--", which
// would start a comment where the content is read as code) and refuses the words refused in code.
func checkNameBody(body string, open, closer byte) error {
	if strings.Contains(body, "--") {
		return fmt.Errorf("may not use -- inside a %cname%c", open, closer)
	}
	for k := 0; k < len(body); k++ {
		b := body[k]
		if !(isWordByte(b) || b == ' ' || b == '#' || b == '.' || b == '-') {
			return fmt.Errorf("may use only letters, digits, spaces and _ $ # . - inside %cname%c; use a double-quoted name for anything else", open, closer)
		}
	}
	for _, w := range strings.FieldsFunc(body, func(r rune) bool { return !isWordByte(byte(r)) }) {
		if err := refusedWord(w); err != nil {
			return err
		}
	}
	return nil
}

// scanQuoted returns the index just past the closing quote of the quoted token starting at q[i], where a doubled
// quote is an escaped quote. A backslash inside is refused: MySQL reads it as an escape, other engines do not.
func scanQuoted(q string, i int, quote byte) (int, error) {
	for j := i + 1; j < len(q); j++ {
		switch q[j] {
		case '\\':
			return 0, errors.New("may not use a backslash inside quotes (engines disagree on whether it escapes)")
		case quote:
			if j+1 < len(q) && q[j+1] == quote {
				j++
				continue
			}
			return j + 1, nil
		}
	}
	return 0, errors.New("has an unterminated quote")
}

// scanNumber returns the index just past a decimal, decimal with fraction and exponent, or 0x hex number at q[i].
func scanNumber(q string, i int) int {
	digits := func(j int, hex bool) int {
		for j < len(q) && (q[j] >= '0' && q[j] <= '9' || hex && (q[j] >= 'a' && q[j] <= 'f' || q[j] >= 'A' && q[j] <= 'F')) {
			j++
		}
		return j
	}
	if q[i] == '0' && i+1 < len(q) && (q[i+1] == 'x' || q[i+1] == 'X') {
		return digits(i+2, true)
	}
	j := digits(i, false)
	if j < len(q) && q[j] == '.' {
		j = digits(j+1, false)
	}
	if j < len(q) && (q[j] == 'e' || q[j] == 'E') {
		k := j + 1
		if k < len(q) && (q[k] == '+' || q[k] == '-') {
			k++
		}
		if k < len(q) && q[k] >= '0' && q[k] <= '9' {
			j = digits(k, false)
		}
	}
	return j
}

// lastWordRaw returns the word that ends just before q[i].
func lastWordRaw(q string, i int) string {
	j := i
	for j > 0 && isWordByte(q[j-1]) {
		j--
	}
	return q[j:i]
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// HasWatermark reports whether the query uses :watermark.
func (s ReadOnlySelect) HasWatermark() bool { return strings.IndexByte(s.cleaned, watermarkMark) >= 0 }

// Render returns the WITH clause (empty when the query has none) and the main SELECT, without comments or a trailing
// semicolon, calling param for each :watermark in text order and writing what it returns in its place.
func (s ReadOnlySelect) Render(param func() string) (with, main string) {
	fill := func(t string) string {
		var b strings.Builder
		parts := strings.Split(t, string(watermarkMark))
		for i, p := range parts {
			b.WriteString(p)
			if i < len(parts)-1 {
				b.WriteString(param())
			}
		}
		return b.String()
	}
	with = strings.TrimSpace(fill(s.cleaned[:s.mainAt]))
	main = strings.TrimSpace(fill(s.cleaned[s.mainAt:]))
	return with, main
}
