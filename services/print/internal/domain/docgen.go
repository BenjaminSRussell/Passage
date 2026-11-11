package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Docgen (#46-1, #46-4): tenant document templates, standard text blocks with
// placeholders and language versions, and the composition that turns a request, a template
// and text blocks into the DocumentRequest the deterministic renderer lays out. Nothing
// here draws: composition only changes the data, so the golden PDFs still pin the output.

const (
	CodeTemplateNotFound  = "PRN-0012" // document template not found (service-local)
	CodeTextBlockNotFound = "PRN-0013" // text block (or its language) not found (service-local)
	CodeTextBlockData     = "PRN-0014" // a placeholder of a text block has no value (service-local)

	maxBlockBody     = 4000
	maxTemplateItems = 8
	defaultLanguage  = "en"
)

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

// FillText replaces every placeholder with its value. A placeholder without a value is an
// error naming all of them: a letter must never print "{{amountDue}}".
func FillText(code, body string, values map[string]string) (string, error) {
	var missing []string
	for _, p := range Placeholders(body) {
		if _, ok := values[p]; !ok {
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
	values := map[string]string{"documentNumber": req.Number, "legalEntityName": req.LegalEntity.Name}
	if req.Counterparty != nil {
		values["counterpartyName"] = req.Counterparty.Name
	}
	for k, v := range in.Values {
		values[k] = v
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
