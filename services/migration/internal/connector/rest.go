package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/oauth"
)

const (
	// MaxResponseBytes bounds one response body; a larger one fails the read instead of filling memory.
	MaxResponseBytes = 32 << 20
	maxRetries       = 3
	maxRetryWait     = 30 * time.Second
	defaultPageSize  = 100
)

// REST reads a JSON endpoint page by page. One Read is one HTTP request (plus a token request when the token is
// missing or stale); a 429 is retried after its Retry-After. Credentials go in headers only, so they are never part of
// a URL, an error or a log line.
type REST struct {
	client      *http.Client
	base        string
	auth        restAuth
	src         domain.RESTSource
	cursorField string
	cursorType  string
	keys        []string
	token       oauth.Token
	// Sleep waits between retries; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

type restAuth struct {
	kind, username, header, secret, authURL, clientID, scope string
}

func newREST(e Env, s Spec) (*REST, error) {
	client, err := httpClient(e)
	if err != nil {
		return nil, err
	}
	src, err := domain.ParseRESTSource(s.Source)
	if err != nil {
		return nil, err
	}
	str := func(k string) string { v, _ := s.Profile[k].(string); return v }
	kind := str("authType")
	if kind == "" {
		kind = "none"
	}
	if kind != "none" && s.Secret == "" {
		return nil, errors.New("no secret is set for this connection; set it first")
	}
	return &REST{
		client: client, base: strings.TrimRight(str("baseUrl"), "/"), src: src, cursorField: s.CursorField, cursorType: s.CursorType, keys: s.KeyFields,
		auth: restAuth{kind: kind, username: str("username"), header: str("headerName"), secret: s.Secret, authURL: str("authUrl"), clientID: str("clientId"), scope: str("scope")},
		Sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}, nil
}

func (r *REST) pageSize(q Query) int {
	n := r.src.Paging.PageSize
	if n < 1 {
		n = defaultPageSize
	}
	return min(n, q.Limit)
}

func orDefault(v, d string) string {
	if v != "" {
		return v
	}
	return d
}

// request builds the URL of the page at position.
func (r *REST) request(q Query) (string, error) {
	p := r.src.Paging
	style := orDefault(p.Style, "none")
	if q.Position != "" && (style == "link" || style == "odata") {
		if !domain.SameOrigin(r.base, q.Position) {
			return "", errors.New("the next-page link points to another host; it was not followed")
		}
		return q.Position, nil
	}
	u, err := url.Parse(r.base + r.src.Path)
	if err != nil {
		return "", errors.New("invalid base URL")
	}
	v := u.Query()
	for k, val := range r.src.Params {
		v.Set(k, val)
	}
	if q.Since != "" && r.src.ChangedSinceParam != "" {
		v.Set(r.src.ChangedSinceParam, q.Since)
	}
	size := strconv.Itoa(r.pageSize(q))
	switch style {
	case "page":
		first := 1
		if p.FirstPage != nil {
			first = *p.FirstPage
		}
		page := first
		if q.Position != "" {
			n, err := strconv.Atoi(q.Position)
			if err != nil || n < 0 {
				return "", errors.New("position is not one this binding returned")
			}
			page = n
		}
		v.Set(orDefault(p.PageParam, "page"), strconv.Itoa(page))
		v.Set(orDefault(p.LimitParam, "limit"), size)
	case "offset":
		off := 0
		if q.Position != "" {
			n, err := strconv.Atoi(q.Position)
			if err != nil || n < 0 {
				return "", errors.New("position is not one this binding returned")
			}
			off = n
		}
		v.Set(orDefault(p.OffsetParam, "offset"), strconv.Itoa(off))
		v.Set(orDefault(p.LimitParam, "limit"), size)
	case "cursor":
		if q.Position != "" {
			v.Set(orDefault(p.CursorParam, "cursor"), q.Position)
		}
		v.Set(orDefault(p.LimitParam, "limit"), size)
	case "skiptoken":
		if q.Position != "" {
			v.Set("$skiptoken", q.Position)
		}
		v.Set(orDefault(p.LimitParam, "$top"), size)
	case "odata":
		v.Set(orDefault(p.LimitParam, "$top"), size)
	case "link":
		v.Set(orDefault(p.LimitParam, "limit"), size)
	}
	u.RawQuery = v.Encode()
	return u.String(), nil
}

func (r *REST) bearer(ctx context.Context) (string, error) {
	if r.token.Valid() {
		return r.token.Value, nil
	}
	t, err := oauth.ClientCredentials(ctx, r.client, r.auth.authURL, r.auth.clientID, r.auth.secret, r.auth.scope)
	if err != nil {
		return "", scrubErr(err, r.auth.secret)
	}
	r.token = t
	return t.Value, nil
}

func (r *REST) do(ctx context.Context, target string) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, errors.New("invalid request URL")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "passage-migration")
		switch r.auth.kind {
		case "basic":
			req.SetBasicAuth(r.auth.username, r.auth.secret)
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+r.auth.secret)
		case "header":
			req.Header.Set(r.auth.header, r.auth.secret)
		case domain.AuthOAuth2:
			tok, err := r.bearer(ctx)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := r.client.Do(req)
		if err != nil {
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err // the URL error would repeat the request URL
			}
			return nil, scrubErr(fmt.Errorf("request failed: %w", err), r.auth.secret)
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		wait := retryAfter(resp.Header.Get("Retry-After"))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if attempt >= maxRetries-1 {
			return nil, errors.New("the endpoint kept answering 429 (too many requests); try again later")
		}
		if err := r.Sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// retryAfter reads Retry-After as seconds or an HTTP date, capped; a missing or bad value waits one second.
func retryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	d := time.Second
	if n, err := strconv.Atoi(h); err == nil && n >= 0 {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(h); err == nil {
		d = time.Until(t)
	}
	return max(0, min(d, maxRetryWait))
}

func (r *REST) Read(ctx context.Context, q Query) (Page, error) {
	if q.Limit < 1 || q.Limit > domain.MaxBatch {
		return Page{}, fmt.Errorf("limit must be 1 to %d", domain.MaxBatch)
	}
	target, err := r.request(q)
	if err != nil {
		return Page{}, err
	}
	resp, err := r.do(ctx, target)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Page{}, fmt.Errorf("the endpoint answered %d; check the credentials", resp.StatusCode)
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return Page{}, fmt.Errorf("the endpoint answered %d (a redirect); redirects are not followed, use the final base URL and path", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return Page{}, fmt.Errorf("the endpoint answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return Page{}, errors.New("the response could not be read")
	}
	if len(body) > MaxResponseBytes {
		return Page{}, fmt.Errorf("the response is larger than %d MiB; lower the page size", MaxResponseBytes>>20)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return Page{}, errors.New("the response is not JSON")
	}
	recs, err := records(doc, r.src.RecordsPath)
	if err != nil {
		return Page{}, err
	}
	var page Page
	for _, rec := range recs {
		row, ok := rec.(map[string]any)
		if !ok {
			return Page{}, errors.New("the records are not JSON objects")
		}
		page.Rows = append(page.Rows, row)
	}
	next, err := r.next(q, resp, doc, len(page.Rows))
	if err != nil {
		return Page{}, err
	}
	page.Next = next
	return finish(&page, r.cursorField, r.cursorType, q.Since, r.src.ChangedSinceParam == "")
}

// finish computes the page watermark and, when the source cannot filter by changed-since itself, drops rows not newer
// than since.
func finish(page *Page, field, typ, since string, filter bool) (Page, error) {
	if field == "" {
		return *page, nil
	}
	kept := page.Rows[:0]
	for _, row := range page.Rows {
		c, ok := domain.NormalizeCursor(typ, domain.Lookup(row, field))
		if !ok {
			if domain.Lookup(row, field) == nil {
				kept = append(kept, row)
				continue
			}
			return Page{}, fmt.Errorf("cursor field %s holds a value that is not a %s", field, typ)
		}
		if filter && since != "" && domain.CompareCursor(typ, c, since) <= 0 {
			continue
		}
		page.Watermark = domain.MaxCursor(typ, page.Watermark, c)
		kept = append(kept, row)
	}
	page.Rows = kept
	return *page, nil
}

// next reads the position that continues after this response, "" at the end.
func (r *REST) next(q Query, resp *http.Response, doc any, n int) (string, error) {
	p := r.src.Paging
	size := r.pageSize(q)
	switch orDefault(p.Style, "none") {
	case "page":
		if n == 0 || n < size {
			return "", nil
		}
		cur := 1
		if p.FirstPage != nil {
			cur = *p.FirstPage
		}
		if q.Position != "" {
			cur, _ = strconv.Atoi(q.Position)
		}
		return strconv.Itoa(cur + 1), nil
	case "offset":
		if n == 0 || n < size {
			return "", nil
		}
		off := 0
		if q.Position != "" {
			off, _ = strconv.Atoi(q.Position)
		}
		return strconv.Itoa(off + n), nil
	case "cursor":
		v := domain.Lookup(doc, p.CursorPath)
		switch x := v.(type) {
		case nil:
			return "", nil
		case string:
			return x, nil
		case json.Number:
			return x.String(), nil
		}
		return "", errors.New("the next cursor in the response is not text")
	case "link":
		return nextLink(r.base, resp.Header.Values("Link"))
	case "odata":
		link, _ := domain.Lookup(doc, "@odata.nextLink").(string)
		if link == "" {
			return "", nil
		}
		if !domain.SameOrigin(r.base, link) {
			return "", errors.New("the next-page link points to another host; it was not followed")
		}
		return link, nil
	case "skiptoken":
		link, _ := domain.Lookup(doc, "@odata.nextLink").(string)
		if link == "" {
			return "", nil
		}
		u, err := url.Parse(link)
		if err != nil || !domain.SameOrigin(r.base, link) {
			return "", errors.New("the next-page link points to another host; it was not followed")
		}
		tok := u.Query().Get("$skiptoken")
		if tok == "" {
			return "", errors.New("the next-page link has no $skiptoken")
		}
		return tok, nil
	}
	return "", nil
}

// nextLink finds rel="next" among Link headers (RFC 8288) and resolves it against base.
func nextLink(base string, headers []string) (string, error) {
	for _, h := range headers {
		for _, part := range splitLinks(h) {
			lt := strings.Index(part, "<")
			gt := strings.Index(part, ">")
			if lt < 0 || gt < lt {
				continue
			}
			params := strings.ToLower(part[gt+1:])
			if !strings.Contains(params, `rel="next"`) && !strings.Contains(params, "rel=next") {
				continue
			}
			ref, err := url.Parse(strings.TrimSpace(part[lt+1 : gt]))
			if err != nil {
				return "", errors.New("the Link header is malformed")
			}
			b, err := url.Parse(base + "/")
			if err != nil {
				return "", errors.New("invalid base URL")
			}
			abs := b.ResolveReference(ref).String()
			if !domain.SameOrigin(base, abs) {
				return "", errors.New("the next-page link points to another host; it was not followed")
			}
			return abs, nil
		}
	}
	return "", nil
}

// splitLinks splits a Link header at the commas between links, not those inside <...>.
func splitLinks(h string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range h {
		switch c {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, h[start:i])
				start = i + 1
			}
		}
	}
	return append(out, h[start:])
}

// records finds the record array: the document itself when it is an array, otherwise the array at recordsPath.
func records(doc any, path string) ([]any, error) {
	v := doc
	if path != "" {
		v = domain.Lookup(doc, path)
	}
	arr, ok := v.([]any)
	if !ok {
		if path == "" {
			return nil, errors.New("the response is not an array; set recordsPath to where the records are")
		}
		return nil, errors.New("recordsPath " + path + " does not lead to an array in the response")
	}
	return arr, nil
}
