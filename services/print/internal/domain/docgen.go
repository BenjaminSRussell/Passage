package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Docgen (#46-1, #46-4): tenant document templates, standard text blocks with
// placeholders and language versions, and the composition that turns a request, a template
// and text blocks into the DocumentRequest the deterministic renderer lays out. Nothing
// here draws: composition only changes the data, so the golden PDFs still pin the output.

const (
	CodeTemplateNotFound  = "PRN-0012" // document template not found (service-local)
	CodeTextBlockNotFound = "PRN-0013" // text block (or its language) not found (service-local)
	CodeTextBlockData     = "PRN-0014" // a placeholder of a text block has no value (service-local)
	CodeTemplateStale     = "PRN-0017" // If-Match names another version than the template's current one (service-local)
	CodeTemplateIfMatch   = "PRN-0018" // a save of an existing template without If-Match (service-local)

	maxBlockBody     = 4000
	maxTemplateItems = 8
	defaultLanguage  = "en"
)

// BuiltinPlaceholders are the placeholders every document can fill without the caller's
// values. A default template, which applies to every render of its type, may only use these.
var BuiltinPlaceholders = []string{"documentNumber", "legalEntityName", "counterpartyName"}

func isBuiltin(name string) bool {
	for _, b := range BuiltinPlaceholders {
		if b == name {
			return true
		}
	}
	return false
}

var (
	blockCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{1,39}$`)
	languagePattern  = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	textPlaceholder  = regexp.MustCompile(`\{\{\s*([A-Za-z][A-Za-z0-9_.]*)\s*\}\}`)
)

// TemplateConfig is what a tenant may change about a document without changing the
// renderer: its title and footer line, the names of columns (by the header the caller
// sends), columns to leave out, the text block that is the letter body and the text blocks
// appended to the notes (form notes).
type TemplateConfig struct {
	Title         string            `json:"title,omitempty"`
	FooterText    string            `json:"footerText,omitempty"`
	RenameColumns map[string]string `json:"renameColumns,omitempty"`
	HideColumns   []string          `json:"hideColumns,omitempty"`
	BodyBlock     string            `json:"bodyBlock,omitempty"`
	NoteBlocks    []string          `json:"noteBlocks,omitempty"`
}

// DocumentTemplate is a stored template. Version counts the saves; at most one template of
// a document type is the default, used when a render request names none.
type DocumentTemplate struct {
	Code         string         `json:"code"`
	Name         string         `json:"name"`
	DocumentType string         `json:"documentType"`
	Default      bool           `json:"default"`
	Version      int            `json:"version"`
	Config       TemplateConfig `json:"config"`
}

// TemplateVersion is one saved version of a template, with who saved it and when.
type TemplateVersion struct {
	DocumentTemplate
	SavedBy string    `json:"savedBy,omitempty"`
	SavedAt time.Time `json:"savedAt"`
}

// ValidCodeName reports whether s is a template or text block code.
func ValidCodeName(s string) bool { return blockCodePattern.MatchString(s) }

// ValidLanguage reports whether s is a language tag such as "en" or "de-CH".
func ValidLanguage(s string) bool { return languagePattern.MatchString(s) }

// Validate returns field-level errors for a template, or none.
func (t DocumentTemplate) Validate() map[string]string {
	f := map[string]string{}
	if !ValidCodeName(t.Code) {
		f["code"] = "must be 2 to 40 characters: capital letters, digits, - and _, starting with a letter"
	}
	if strings.TrimSpace(t.Name) == "" || len([]rune(t.Name)) > maxDocField {
		f["name"] = "is required and at most 200 characters"
	}
	known := false
	for _, d := range DocumentTypes() {
		known = known || d == t.DocumentType
	}
	if !known {
		f["documentType"] = "must be a document type the renderer knows"
	}
	c := t.Config
	if len([]rune(c.Title)) > maxDocTitle {
		f["config.title"] = fmt.Sprintf("is longer than %d characters", maxDocTitle)
	}
	if len([]rune(c.FooterText)) > maxDocFooter {
		f["config.footerText"] = fmt.Sprintf("is longer than %d characters", maxDocFooter)
	}
	if len(c.RenameColumns) > maxDocCols || len(c.HideColumns) > maxDocCols || len(c.NoteBlocks) > maxTemplateItems {
		f["config"] = "has too many renamed columns, hidden columns or note blocks"
	}
	for from, to := range c.RenameColumns {
		if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" || len([]rune(to)) > maxDocField {
			f["config.renameColumns"] = "needs a header and a new name of at most 200 characters"
		}
	}
	if c.BodyBlock != "" && !ValidCodeName(c.BodyBlock) {
		f["config.bodyBlock"] = "must be a text block code"
	}
	if c.BodyBlock != "" && !IsCorrespondence(t.DocumentType) {
		f["config.bodyBlock"] = "is only used by correspondence documents"
	}
	for _, b := range c.NoteBlocks {
		if !ValidCodeName(b) {
			f["config.noteBlocks"] = "must be text block codes"
		}
	}
	return f
}

// TextBlock is a standard text in one language. Placeholders are written {{name}}.
type TextBlock struct {
	Code         string   `json:"code"`
	Language     string   `json:"language"`
	Body         string   `json:"body"`
	Placeholders []string `json:"placeholders"`
	Version      int      `json:"version"`
}

// Validate returns field-level errors for a text block, or none.
func (b TextBlock) Validate() map[string]string {
	f := map[string]string{}
	if !ValidCodeName(b.Code) {
		f["code"] = "must be 2 to 40 characters: capital letters, digits, - and _, starting with a letter"
	}
	if !ValidLanguage(b.Language) {
		f["language"] = "must be a language tag such as en or de-CH"
	}
	if strings.TrimSpace(b.Body) == "" || len([]rune(b.Body)) > maxBlockBody {
		f["body"] = fmt.Sprintf("is required and at most %d characters", maxBlockBody)
	}
	return f
}

// Placeholders lists the distinct placeholder names in body, sorted.
func Placeholders(body string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range textPlaceholder.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// FillText replaces every placeholder with its value. A placeholder without a value, or
// with a blank one, is an error naming all of them: a letter must never print "{{amountDue}}"
// or an empty gap.
func FillText(code, body string, values map[string]string) (string, error) {
	var missing []string
	for _, p := range Placeholders(body) {
		if v, ok := values[p]; !ok || strings.TrimSpace(v) == "" {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return "", &Error{Code: CodeTextBlockData, Detail: fmt.Sprintf("text block %s needs values for %s", code, strings.Join(missing, ", "))}
	}
	return textPlaceholder.ReplaceAllStringFunc(body, func(m string) string {
		return values[textPlaceholder.FindStringSubmatch(m)[1]]
	}), nil
}

// PickBlock chooses the version of a block for lang: the exact tag, then the language
// without its region, then English.
func PickBlock(versions []TextBlock, lang string) (TextBlock, bool) {
	if lang == "" {
		lang = defaultLanguage
	}
	base, _, _ := strings.Cut(lang, "-")
	for _, want := range []string{lang, base, defaultLanguage} {
		for _, b := range versions {
			if b.Language == want {
				return b, true
			}
		}
	}
	return TextBlock{}, false
}

// BlockSource returns every language version of a text block (none when it does not exist).
type BlockSource func(code string) ([]TextBlock, error)

// ComposeInput is what a render request adds to the document data.
type ComposeInput struct {
	Language  string
	BodyBlock string            // overrides the template's body block
	Values    map[string]string // placeholder values
}

// Compose applies a template (which may be nil) and text blocks to a document request.
// Request fields win over the template: a Title, FooterText or Body the caller sent stays.
func Compose(req DocumentRequest, tpl *DocumentTemplate, in ComposeInput, blocks BlockSource) (DocumentRequest, error) {
	// The caller's values come first; the built-ins always win, so a request cannot make
	// a letter name another entity or number than the document it is printed on.
	values := map[string]string{}
	for k, v := range in.Values {
		values[k] = v
	}
	values["documentNumber"], values["legalEntityName"] = req.Number, req.LegalEntity.Name
	delete(values, "counterpartyName")
	if req.Counterparty != nil {
		values["counterpartyName"] = req.Counterparty.Name
	}
	resolve := func(code string) (string, error) {
		versions, err := blocks(code)
		if err != nil {
			return "", err
		}
		if len(versions) == 0 {
			return "", &Error{Code: CodeTextBlockNotFound, Detail: fmt.Sprintf("text block %s does not exist", code)}
		}
		b, ok := PickBlock(versions, in.Language)
		if !ok {
			return "", &Error{Code: CodeTextBlockNotFound, Detail: fmt.Sprintf("text block %s has no version for language %q or en", code, in.Language)}
		}
		return FillText(code, b.Body, values)
	}

	var cfg TemplateConfig
	if tpl != nil {
		cfg = tpl.Config
		if tpl.DocumentType != req.DocumentType {
			return req, &Error{Code: CodeDocument, Detail: fmt.Sprintf("template %s is for %s, not %s", tpl.Code, tpl.DocumentType, req.DocumentType)}
		}
	}
	if req.Title == "" {
		req.Title = cfg.Title
	}
	if req.FooterText == "" {
		req.FooterText = cfg.FooterText
	}

	if len(cfg.HideColumns) > 0 || len(cfg.RenameColumns) > 0 {
		hide := map[string]bool{}
		for _, h := range cfg.HideColumns {
			hide[h] = true
		}
		var keep []int
		var cols []DocColumn
		for i, c := range req.Columns {
			if hide[c.Header] {
				continue
			}
			keep = append(keep, i)
			if to, ok := cfg.RenameColumns[c.Header]; ok {
				c.Header = to
			}
			cols = append(cols, c)
		}
		lines := make([][]string, len(req.Lines))
		for i, l := range req.Lines {
			if len(l) != len(req.Columns) {
				return req, docErr(fmt.Sprintf("lines[%d]", i), fmt.Sprintf("has %d cells but there are %d columns", len(l), len(req.Columns)))
			}
			row := make([]string, len(keep))
			for j, k := range keep {
				row[j] = l[k]
			}
			lines[i] = row
		}
		req.Columns, req.Lines = cols, lines
	}

	bodyBlock := in.BodyBlock
	if bodyBlock == "" {
		bodyBlock = cfg.BodyBlock
	}
	if bodyBlock != "" && req.Body == "" {
		text, err := resolve(bodyBlock)
		if err != nil {
			return req, err
		}
		req.Body = text
	}
	for _, code := range cfg.NoteBlocks {
		text, err := resolve(code)
		if err != nil {
			return req, err
		}
		if strings.TrimSpace(req.Notes) != "" {
			req.Notes = strings.TrimSpace(req.Notes) + "\n\n"
		}
		req.Notes += text
	}
	return req, nil
}

// CheckTemplateBlocks checks, when a template is saved, that rendering with it cannot fail
// later for a reason the template itself causes: every text block it names exists with an
// English version (the fallback), the form notes fit one document, and a default template,
// which every render of its type uses, needs no placeholder beyond the built-in ones. It
// returns field errors, or none.
func CheckTemplateBlocks(t DocumentTemplate, versions BlockSource) (map[string]string, error) {
	f := map[string]string{}
	check := func(field, code string) (TextBlock, error) {
		vs, err := versions(code)
		if err != nil {
			return TextBlock{}, err
		}
		if len(vs) == 0 {
			f[field] = "text block " + code + " does not exist"
			return TextBlock{}, nil
		}
		en, ok := PickBlock(vs, defaultLanguage)
		if !ok || en.Language != defaultLanguage {
			f[field] = "text block " + code + " has no English version to fall back on"
			return TextBlock{}, nil
		}
		if t.Default {
			for _, v := range vs {
				for _, ph := range v.Placeholders {
					if !isBuiltin(ph) {
						f[field] = fmt.Sprintf("text block %s (%s) needs the value %s, which a default template cannot get; use only %s", code, v.Language, ph, strings.Join(BuiltinPlaceholders, ", "))
						return TextBlock{}, nil
					}
				}
			}
		}
		return en, nil
	}
	if t.Config.BodyBlock != "" {
		en, err := check("config.bodyBlock", t.Config.BodyBlock)
		if err != nil {
			return nil, err
		}
		if len([]rune(en.Body)) > maxDocBody {
			f["config.bodyBlock"] = fmt.Sprintf("text is longer than the %d characters a letter body holds", maxDocBody)
		}
	}
	notes := 0
	for _, code := range t.Config.NoteBlocks {
		en, err := check("config.noteBlocks", code)
		if err != nil {
			return nil, err
		}
		notes += len([]rune(en.Body)) + 2
	}
	if notes > maxDocNotes {
		f["config.noteBlocks"] = fmt.Sprintf("the form notes together are longer than the %d characters a document's notes hold", maxDocNotes)
	}
	return f, nil
}

// DefaultTemplateBlockProblem reports why body cannot be saved as a version of block code
// when a default template uses the block: it would need a value no render supplies.
func DefaultTemplateBlockProblem(templates []DocumentTemplate, code, body string) string {
	for _, t := range templates {
		if !t.Default || (t.Config.BodyBlock != code && !containsString(t.Config.NoteBlocks, code)) {
			continue
		}
		for _, ph := range Placeholders(body) {
			if !isBuiltin(ph) {
				return fmt.Sprintf("is used by the default template %s, which cannot supply the value %s; use only %s", t.Code, ph, strings.Join(BuiltinPlaceholders, ", "))
			}
		}
	}
	return ""
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
