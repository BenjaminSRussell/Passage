package ebics

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
)

const (
	defaultSegmentSize = 1 << 20
	defaultHTTPTimeout = 60 * time.Second
	minHTTPTimeout     = time.Second
	maxHTTPTimeout     = 10 * time.Minute
)

// LoadHTTPTimeout reads TREASURY_EBICS_HTTP_TIMEOUT, the time one request to a
// bank may take (default 60s, between 1s and 10m).
func LoadHTTPTimeout(getenv func(string) string) (time.Duration, error) {
	v := getenv("TREASURY_EBICS_HTTP_TIMEOUT")
	if v == "" {
		return defaultHTTPTimeout, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < minHTTPTimeout || d > maxHTTPTimeout {
		return 0, fmt.Errorf("TREASURY_EBICS_HTTP_TIMEOUT must be a duration between %s and %s, got %s", minHTTPTimeout, maxHTTPTimeout, v)
	}
	return d, nil
}

var (
	uploadOrders   = map[string]bool{"CCT": true, "CDD": true, "CDB": true, "CIP": true}
	downloadOrders = map[string]bool{"C52": true, "C53": true, "C54": true, "HAC": true, "PTK": true, "STA": true, "VMK": true}

	ErrUnsupportedOrder = errors.New("ebics: order type is not one of the H004 orders this client supports (BTU and BTD exist only in H005)")
)

// Client speaks EBICS H004 to a bank for the subscribers in Store.
type Client struct {
	Store       SubscriberStore
	Keys        func(Subscriber) (Keys, error)
	HTTPTimeout time.Duration
	SegmentSize int
	Backoff     BackoffPolicy
	Sleep       func(context.Context, time.Duration) error
	Now         func() time.Time

	// AllowLoopback lets requests go to a loopback bank over plain http (local development).
	AllowLoopback bool

	bankOnce   sync.Once
	bankClient *bankconnect.BankHTTP
}

func (c *Client) bankHTTP() *bankconnect.BankHTTP {
	c.bankOnce.Do(func() {
		timeout := c.HTTPTimeout
		if timeout <= 0 {
			timeout = defaultHTTPTimeout
		}
		c.bankClient = bankconnect.NewBankHTTP(timeout, c.AllowLoopback)
	})
	return c.bankClient
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) policy() BackoffPolicy {
	if c.Backoff.Attempts > 0 {
		return c.Backoff
	}
	return DefaultBackoff
}

func (c *Client) segmentSize() int {
	if c.SegmentSize > 0 {
		return c.SegmentSize
	}
	return defaultSegmentSize
}

// post sends one request. reached is false when the request cannot have arrived
// (context cancelled first, or the connection was never made).
func (c *Client) post(ctx context.Context, sub Subscriber, body []byte) (raw []byte, reached bool, err error) {
	hc := c.bankHTTP()
	if err := hc.Check(sub.URL); err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		var opErr *net.OpError
		return nil, !(errors.As(err, &opErr) && opErr.Op == "dial"), err
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, maxMessageLen+1))
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, true, fmt.Errorf("ebics: bank answered HTTP %d", resp.StatusCode)
	}
	if len(raw) > maxMessageLen {
		return nil, true, errors.New("ebics: bank response too large")
	}
	return raw, true, nil
}

func (c *Client) load(ctx context.Context, id string) (Subscriber, Keys, error) {
	sub, err := c.Store.GetSubscriber(ctx, id)
	if err != nil {
		return Subscriber{}, nil, err
	}
	if err := sub.Validate(); err != nil {
		return Subscriber{}, nil, err
	}
	keys, err := c.Keys(sub)
	return sub, keys, err
}

func (s Subscriber) requireState(want State) error {
	if s.State != want {
		return fmt.Errorf("%w: state is %s, want %s", ErrBadState, s.State, want)
	}
	return nil
}

// Letter is the printed initialisation letter a person signs and sends to the
// bank, with the hash the bank checks against what it received by INI or HIA.
type Letter struct {
	Text   string
	Hashes map[KeyUsage]string
}

// FormatDigest renders a key hash the way letters print it: uppercase hex in
// pairs separated by spaces.
func FormatDigest(d []byte) string {
	h := strings.ToUpper(hex.EncodeToString(d))
	var parts []string
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, " ")
}

func (c *Client) keyOrder(ctx context.Context, sub Subscriber, orderType, orderData string) error {
	packed, err := compress([]byte(orderData))
	if err != nil {
		return err
	}
	raw, _, err := c.post(ctx, sub, []byte(unsecuredRequest(sub, orderType, "DZNNN", b64(packed))))
	if err != nil {
		return fmt.Errorf("ebics %s: %w", orderType, err)
	}
	root, err := parseXML(raw)
	if err != nil {
		return fmt.Errorf("ebics %s: %w", orderType, err)
	}
	r, err := parseReply(root, "ebicsKeyManagementResponse")
	if err != nil {
		return fmt.Errorf("ebics %s: %w", orderType, err)
	}
	return checkCodes(r)
}

func checkCodes(r reply) error {
	if err := returnErr(r.tech, r.techText); err != nil {
		return err
	}
	if r.biz != "" && r.biz != CodeOK {
		if r.biz == CodeNoDownloadData {
			return ErrNoData
		}
		if err := returnErr(r.biz, r.techText); err != nil {
			return err
		}
	}
	return nil
}

// SendINI sends the A006 signature key (order INI) and returns the letter to
// print. NEW -> INI_SENT.
func (c *Client) SendINI(ctx context.Context, id string) (Letter, error) {
	sub, keys, err := c.load(ctx, id)
	if err != nil {
		return Letter{}, err
	}
	if err := sub.requireState(StateNew); err != nil {
		return Letter{}, err
	}
	a006, version, err := keys.Public(ctx, UsageA006)
	if err != nil {
		return Letter{}, err
	}
	if err := c.keyOrder(ctx, sub, "INI", iniOrderData(sub, a006, c.now())); err != nil {
		return Letter{}, err
	}
	sub.State, sub.A006Version = StateINISent, version
	if _, err := c.Store.UpdateSubscriber(ctx, sub); err != nil {
		return Letter{}, err
	}
	return letter(sub, "INI", map[KeyUsage]*rsa.PublicKey{UsageA006: a006}), nil
}

// SendHIA sends the X002 authentication and E002 encryption keys (order HIA)
// and returns the letter to print. INI_SENT -> HIA_SENT.
func (c *Client) SendHIA(ctx context.Context, id string) (Letter, error) {
	sub, keys, err := c.load(ctx, id)
	if err != nil {
		return Letter{}, err
	}
	if err := sub.requireState(StateINISent); err != nil {
		return Letter{}, err
	}
	x002, version, err := keys.Public(ctx, UsageX002)
	if err != nil {
		return Letter{}, err
	}
	e002, _, err := keys.Public(ctx, UsageE002)
	if err != nil {
		return Letter{}, err
	}
	if err := c.keyOrder(ctx, sub, "HIA", hiaOrderData(sub, x002, e002)); err != nil {
		return Letter{}, err
	}
	sub.State, sub.X002Version = StateHIASent, version
	if _, err := c.Store.UpdateSubscriber(ctx, sub); err != nil {
		return Letter{}, err
	}
	return letter(sub, "HIA", map[KeyUsage]*rsa.PublicKey{UsageX002: x002, UsageE002: e002}), nil
}

// ErrUnknownLetter is returned for a letter other than INI or HIA.
var ErrUnknownLetter = errors.New("ebics: letters exist for the INI and HIA orders only")

// Letter rebuilds the initialisation letter of the INI or HIA order from the
// subscriber's public keys, so it can be printed again without sending anything.
func (c *Client) Letter(ctx context.Context, id, order string) (Letter, error) {
	var usages []KeyUsage
	switch order {
	case "INI":
		usages = []KeyUsage{UsageA006}
	case "HIA":
		usages = []KeyUsage{UsageX002, UsageE002}
	default:
		return Letter{}, ErrUnknownLetter
	}
	sub, keys, err := c.load(ctx, id)
	if err != nil {
		return Letter{}, err
	}
	pubs := map[KeyUsage]*rsa.PublicKey{}
	for _, u := range usages {
		if pubs[u], _, err = keys.Public(ctx, u); err != nil {
			return Letter{}, err
		}
	}
	return letter(sub, order, pubs), nil
}

func letter(sub Subscriber, order string, pubs map[KeyUsage]*rsa.PublicKey) Letter {
	l := Letter{Hashes: map[KeyUsage]string{}}
	var b strings.Builder
	fmt.Fprintf(&b, "%s letter\nHost ID: %s\nPartner ID: %s\nUser ID: %s\n", order, sub.HostID, sub.PartnerID, sub.UserID)
	for _, u := range []KeyUsage{UsageA006, UsageX002, UsageE002} {
		if pub, ok := pubs[u]; ok {
			l.Hashes[u] = FormatDigest(KeyDigest(pub))
			fmt.Fprintf(&b, "%s public key\n  exponent (hex): %s\n  modulus (hex): %s\n  hash (SHA-256): %s\n", u, fmt.Sprintf("%x", pub.E), pub.N.Text(16), l.Hashes[u])
		}
	}
	b.WriteString("Date, signature of the subscriber:\n")
	l.Text = b.String()
	return l
}

// BankKeyHashes are the hashes of the bank keys just downloaded. A person
// compares them with the bank's own letter before ConfirmBankKeys.
type BankKeyHashes struct{ X002, E002 string }

// FetchBankKeys downloads the bank's X002 and E002 keys (order HPB) and stores
// them unconfirmed. They are not used for anything until ConfirmBankKeys passes,
// and the person who fetched them (actor) cannot be the one who confirms them.
// HIA_SENT or BANK_KEYS_PENDING -> BANK_KEYS_PENDING.
func (c *Client) FetchBankKeys(ctx context.Context, id, actor string) (BankKeyHashes, error) {
	if actor == "" {
		return BankKeyHashes{}, errors.New("ebics: who downloaded the bank keys must be recorded")
	}
	sub, keys, err := c.load(ctx, id)
	if err != nil {
		return BankKeyHashes{}, err
	}
	if sub.State != StateHIASent && sub.State != StateKeysPending {
		return BankKeyHashes{}, fmt.Errorf("%w: state is %s, want %s or %s", ErrBadState, sub.State, StateHIASent, StateKeysPending)
	}
	n, err := nonce()
	if err != nil {
		return BankKeyHashes{}, err
	}
	body, err := signRequest(ctx, keys, hpbRequest(sub, n, c.now()))
	if err != nil {
		return BankKeyHashes{}, err
	}
	raw, _, err := c.post(ctx, sub, body)
	if err != nil {
		return BankKeyHashes{}, fmt.Errorf("ebics HPB: %w", err)
	}
	root, err := parseXML(raw)
	if err != nil {
		return BankKeyHashes{}, fmt.Errorf("ebics HPB: %w", err)
	}
	r, err := parseReply(root, "ebicsKeyManagementResponse")
	if err != nil {
		return BankKeyHashes{}, fmt.Errorf("ebics HPB: %w", err)
	}
	if err := checkCodes(r); err != nil {
		return BankKeyHashes{}, err
	}
	plain, err := decryptBody(ctx, keys, root)
	if err != nil {
		return BankKeyHashes{}, fmt.Errorf("ebics HPB: %w", err)
	}
	data, err := parseXML(plain)
	if err != nil || data.local != "HPBResponseOrderData" {
		return BankKeyHashes{}, errors.New("ebics HPB: response order data is not HPBResponseOrderData")
	}
	x002, err := parseRSAKeyValue(data.child("AuthenticationPubKeyInfo"))
	if err != nil {
		return BankKeyHashes{}, fmt.Errorf("ebics HPB: bank X002 key: %w", err)
	}
	e002, err := parseRSAKeyValue(data.child("EncryptionPubKeyInfo"))
	if err != nil {
		return BankKeyHashes{}, fmt.Errorf("ebics HPB: bank E002 key: %w", err)
	}
	if sub.BankX002, err = x509.MarshalPKIXPublicKey(x002); err != nil {
		return BankKeyHashes{}, err
	}
	if sub.BankE002, err = x509.MarshalPKIXPublicKey(e002); err != nil {
		return BankKeyHashes{}, err
	}
	sub.State, sub.KeysConfirmed, sub.ConfirmedBy, sub.ConfirmedAt, sub.FetchedBy = StateKeysPending, false, "", nil, actor
	if _, err := c.Store.UpdateSubscriber(ctx, sub); err != nil {
		return BankKeyHashes{}, err
	}
	return BankKeyHashes{X002: FormatDigest(KeyDigest(x002)), E002: FormatDigest(KeyDigest(e002))}, nil
}

func normalizeHash(s string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", ":", "", "\n", "", "\t", "").Replace(s))
}

// ConfirmBankKeys is the explicit step that activates a subscriber: the hashes
// the bank printed on its own letter must equal the hashes of the keys HPB
// delivered, recomputed here from the stored keys. BANK_KEYS_PENDING -> READY.
func (c *Client) ConfirmBankKeys(ctx context.Context, id, x002Hash, e002Hash, confirmedBy string, version int) (Subscriber, error) {
	sub, err := c.Store.GetSubscriber(ctx, id)
	if err != nil {
		return Subscriber{}, err
	}
	if sub.Version != version {
		return Subscriber{}, ErrVersionConflict
	}
	if err := sub.requireState(StateKeysPending); err != nil {
		return Subscriber{}, err
	}
	if confirmedBy == "" {
		return Subscriber{}, errors.New("ebics: who confirmed the bank key hashes must be recorded")
	}
	if confirmedBy == sub.FetchedBy {
		return Subscriber{}, ErrFourEyes
	}
	if sub.A006Version < 1 || sub.X002Version < 1 {
		return Subscriber{}, fmt.Errorf("%w: the transit key versions were not pinned at INI and HIA", ErrBadState)
	}
	x002, e002, err := sub.bankKeys()
	if err != nil {
		return Subscriber{}, err
	}
	okX := normalizeHash(x002Hash) == hex.EncodeToString(KeyDigest(x002))
	okE := normalizeHash(e002Hash) == hex.EncodeToString(KeyDigest(e002))
	if !okX || !okE {
		return Subscriber{}, ErrKeyHashMismatch
	}
	now := c.now().UTC()
	sub.State, sub.KeysConfirmed, sub.ConfirmedBy, sub.ConfirmedAt = StateReady, true, confirmedBy, &now
	return c.Store.UpdateSubscriber(ctx, sub)
}

// Unpark lets a parked subscriber transact again after a person (actor) has
// dealt with the refusal that parked it; the store records who and when in the
// same transaction. version is the subscriber version the person saw. A
// subscriber that is not parked is ErrBadState.
func (c *Client) Unpark(ctx context.Context, id, actor string, version int) (Subscriber, error) {
	if actor == "" {
		return Subscriber{}, errors.New("ebics: who unparked the subscriber must be recorded")
	}
	sub, err := c.Store.GetSubscriber(ctx, id)
	if err != nil {
		return Subscriber{}, err
	}
	if sub.Version != version {
		return Subscriber{}, ErrVersionConflict
	}
	if sub.ParkedCode == "" {
		return Subscriber{}, fmt.Errorf("%w: subscriber is not parked", ErrBadState)
	}
	slog.WarnContext(ctx, "ebics subscriber unparked", "subscriber", id, "actor", actor, "code", sub.ParkedCode)
	return c.Store.UnparkSubscriber(ctx, sub, actor)
}

func (c *Client) ready(ctx context.Context, id string) (Subscriber, Keys, *rsa.PublicKey, *rsa.PublicKey, error) {
	sub, keys, err := c.load(ctx, id)
	if err != nil {
		return Subscriber{}, nil, nil, nil, err
	}
	if err := sub.requireState(StateReady); err != nil {
		return Subscriber{}, nil, nil, nil, err
	}
	if !sub.KeysConfirmed {
		return Subscriber{}, nil, nil, nil, fmt.Errorf("%w: bank key hashes are not confirmed", ErrBadState)
	}
	if sub.ParkedCode != "" {
		return Subscriber{}, nil, nil, nil, fmt.Errorf("%w (%s %s)", ErrParked, sub.ParkedCode, sub.ParkedText)
	}
	bx, be, err := sub.bankKeys()
	return sub, keys, bx, be, err
}

// withBackoff retries fn while the bank answers with back-off codes, waiting
// longer each time, and parks the subscriber on a refusal that needs a person.
func (c *Client) withBackoff(ctx context.Context, sub Subscriber, fn func() error) error {
	p := c.policy()
	for attempt := 0; ; attempt++ {
		err := fn()
		var re *ReturnError
		if err == nil || errors.Is(err, bankconnect.ErrAmbiguous) || !errors.As(err, &re) {
			return err
		}
		switch {
		case re.Disposition == Park:
			slog.ErrorContext(ctx, "ebics subscriber parked", "subscriber", sub.ID, "code", re.Code)
			return errors.Join(err, c.park(ctx, sub.ID, re))
		case re.Disposition == BackOff && attempt+1 < p.Attempts:
			slog.WarnContext(ctx, "ebics backing off", "subscriber", sub.ID, "code", re.Code, "attempt", attempt+1)
			if serr := c.sleep(ctx, p.delay(attempt)); serr != nil {
				return errors.Join(err, serr)
			}
		default:
			return err
		}
	}
}

const parkAttempts = 3

func (c *Client) park(ctx context.Context, id string, re *ReturnError) error {
	ctx = context.WithoutCancel(ctx)
	var err error
	for range parkAttempts {
		var sub Subscriber
		if sub, err = c.Store.GetSubscriber(ctx, id); err != nil {
			return err
		}
		sub.ParkedCode, sub.ParkedText = re.Code, re.Text
		if _, err = c.Store.UpdateSubscriber(ctx, sub); !errors.Is(err, ErrVersionConflict) {
			return err
		}
	}
	return err
}

// UploadReceipt names the bank's transaction and order for an accepted upload.
type UploadReceipt struct{ TransactionID, OrderID string }

// Upload sends one payment file (for example a pain.001 for CCT) as an upload
// order: initialisation with the A006 signature and the encrypted, compressed
// data, then the segments. A transport failure once the last segment is on the
// wire, or a back-off code answering it, is ErrAmbiguous and is not retried.
func (c *Client) Upload(ctx context.Context, id, orderType string, data []byte) (UploadReceipt, error) {
	if !uploadOrders[orderType] {
		return UploadReceipt{}, fmt.Errorf("%w: %q", ErrUnsupportedOrder, orderType)
	}
	if len(data) == 0 || len(data) > bankconnect.MaxFileBytes {
		return UploadReceipt{}, fmt.Errorf("ebics upload refused: file of %d bytes is empty or exceeds %d", len(data), bankconnect.MaxFileBytes)
	}
	sub, keys, bx, be, err := c.ready(ctx, id)
	if err != nil {
		return UploadReceipt{}, err
	}
	var out UploadReceipt
	err = c.withBackoff(ctx, sub, func() error {
		var err error
		out, err = c.uploadOnce(ctx, sub, keys, bx, be, orderType, data)
		return err
	})
	return out, err
}

func (c *Client) uploadOnce(ctx context.Context, sub Subscriber, keys Keys, bx, be *rsa.PublicKey, orderType string, data []byte) (UploadReceipt, error) {
	a006, err := keys.SignA006(ctx, orderDigest(data))
	if err != nil {
		return UploadReceipt{}, fmt.Errorf("sign order (A006): %w", err)
	}
	cipherText, wrappedKey, aesKey, err := encryptOrderData(data, be)
	if err != nil {
		return UploadReceipt{}, err
	}
	sigData, err := sealWithKey(aesKey, []byte(userSignatureData(sub, a006)))
	if err != nil {
		return UploadReceipt{}, err
	}
	segs := segments(b64(cipherText), c.segmentSize())
	n, err := nonce()
	if err != nil {
		return UploadReceipt{}, err
	}
	spec := orderSpec{Type: orderType, Attribute: "OZHNN", Segments: len(segs)}
	r, err := c.exchange(ctx, sub, keys, bx, signable{doc: initRequest(sub, spec, n, c.now(), bx, be, uploadInitBody(be, wrappedKey, sigData))})
	if err != nil {
		return UploadReceipt{}, err
	}
	if r.txID == "" {
		return UploadReceipt{}, errors.New("ebics upload: bank gave no transaction id")
	}
	receipt := UploadReceipt{TransactionID: r.txID, OrderID: r.orderID}
	for i, seg := range segs {
		last := i == len(segs)-1
		doc := phaseRequest(sub, r.txID, "Transfer", i+1, last, "<DataTransfer><OrderData>"+seg+"</OrderData></DataTransfer>")
		reply, err := c.exchange(ctx, sub, keys, bx, signable{doc: doc, lastOfUpload: last})
		if err != nil {
			return UploadReceipt{}, err
		}
		if reply.txID != "" && reply.txID != r.txID {
			mismatch := fmt.Errorf("ebics upload: %w: transfer reply carries transaction %q, expected %q", ErrProtocol, reply.txID, r.txID)
			if last {
				return UploadReceipt{}, fmt.Errorf("%w: %w", bankconnect.ErrAmbiguous, mismatch)
			}
			return UploadReceipt{}, mismatch
		}
		if reply.orderID != "" {
			receipt.OrderID = reply.orderID
		}
	}
	return receipt, nil
}

type signable struct {
	doc          string
	lastOfUpload bool
}

// exchange signs and posts a standard request and checks the bank's answer:
// its AuthSignature, then the technical and business return codes.
func (c *Client) exchange(ctx context.Context, sub Subscriber, keys Keys, bx *rsa.PublicKey, s signable) (reply, error) {
	body, err := signRequest(ctx, keys, s.doc)
	if err != nil {
		return reply{}, err
	}
	ambiguous := func(err error) error {
		if s.lastOfUpload {
			return fmt.Errorf("ebics upload: %w: %w", bankconnect.ErrAmbiguous, err)
		}
		return err
	}
	raw, reached, err := c.post(ctx, sub, body)
	if err != nil {
		if reached {
			return reply{}, ambiguous(err)
		}
		return reply{}, err
	}
	root, err := parseXML(raw)
	if err == nil {
		err = verifyAuthSignature(root, bx)
	}
	if err != nil {
		return reply{}, ambiguous(fmt.Errorf("ebics: unusable bank response: %w", err))
	}
	r, err := parseReply(root, "ebicsResponse")
	if err != nil {
		return reply{}, ambiguous(err)
	}
	if err := checkCodes(r); err != nil {
		var re *ReturnError
		if s.lastOfUpload && errors.As(err, &re) && re.Disposition == BackOff {
			return reply{}, ambiguous(err)
		}
		return reply{}, err
	}
	return r, nil
}

// DownloadedFile is one file from a download order.
type DownloadedFile struct {
	Name string
	Data []byte
}

const maxArchiveBytes = 1 << 30

// ErrLocal marks a download that failed after the bank delivered it: the data
// could not be decrypted, unpacked, size-checked or stored here. Retrying cannot
// help until a person acts, so the poller counts these and parks the subscriber.
var ErrLocal = errors.New("ebics: the delivered data could not be processed here")

type fileSink func(context.Context, DownloadedFile) error

// Download fetches the files of a download order (C53, C52, C54, HAC, PTK, STA,
// VMK) for an optional date range (YYYY-MM-DD), reassembling the segments,
// decrypting and decompressing them, and acknowledging with a transfer receipt.
// A ZIP container is unpacked into its files; their total is capped at
// bankconnect.MaxFileBytes. ErrNoData means the bank has nothing new.
func (c *Client) Download(ctx context.Context, id, orderType, from, to string) ([]DownloadedFile, error) {
	var files []DownloadedFile
	err := c.downloadWith(ctx, id, orderType, from, to, func() fileSink {
		files = nil
		total := 0
		return func(_ context.Context, f DownloadedFile) error {
			if total += len(f.Data); total > bankconnect.MaxFileBytes {
				return fmt.Errorf("the files total more than %d bytes; use FetchStatements to take them one by one", bankconnect.MaxFileBytes)
			}
			files = append(files, f)
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// downloadWith unpacks the download file by file into the sink newSink makes for
// each attempt, before the receipt is sent: when a file is refused, the bank is
// told the transfer failed (ReceiptCode 1) and keeps the data, so a download is
// acknowledged only once every file is stored.
func (c *Client) downloadWith(ctx context.Context, id, orderType, from, to string, newSink func() fileSink) error {
	if !downloadOrders[orderType] {
		return fmt.Errorf("%w: %q", ErrUnsupportedOrder, orderType)
	}
	if (from == "") != (to == "") {
		return errors.New("ebics download: give both ends of the date range or neither")
	}
	sub, keys, bx, be, err := c.ready(ctx, id)
	if err != nil {
		return err
	}
	return c.withBackoff(ctx, sub, func() error {
		return c.downloadOnce(ctx, sub, keys, bx, be, orderType, from, to, newSink())
	})
}

func (c *Client) downloadOnce(ctx context.Context, sub Subscriber, keys Keys, bx, be *rsa.PublicKey, orderType, from, to string, sink fileSink) error {
	n, err := nonce()
	if err != nil {
		return err
	}
	r, err := c.exchange(ctx, sub, keys, bx, signable{doc: initRequest(sub, orderSpec{Type: orderType, Attribute: "DZHNN", From: from, To: to}, n, c.now(), bx, be, "")})
	if err != nil {
		return err
	}
	if r.txID == "" || r.numSegments < 1 {
		return fmt.Errorf("ebics download: %w: bank gave no transaction id or segment count", ErrProtocol)
	}
	own, _, err := keys.Public(ctx, UsageE002)
	if err != nil {
		return err
	}
	wrapped, err := transactionKeyOf(r.root, KeyDigest(own))
	if err != nil {
		return fmt.Errorf("ebics download: %w", err)
	}
	encoded, err := orderDataOf(r.root.child("body"))
	if err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString(encoded)
	for seg := 1; ; seg++ {
		if r.segment != seg || (seg == r.numSegments) != r.last {
			return fmt.Errorf("ebics download: %w: expected segment %d of %d, bank sent %d (last=%t)", ErrProtocol, seg, r.numSegments, r.segment, r.last)
		}
		if seg == r.numSegments {
			break
		}
		if sb.Len() > maxOrderDataSize/3*4+maxMessageLen {
			return fmt.Errorf("%w: order data exceeds the size limit", ErrLocal)
		}
		doc := phaseRequest(sub, r.txID, "Transfer", seg+1, seg+1 == r.numSegments, "")
		next, err := c.exchange(ctx, sub, keys, bx, signable{doc: doc})
		if err != nil {
			return err
		}
		if next.txID != "" && next.txID != r.txID {
			return fmt.Errorf("ebics download: %w: transaction id changed mid-transfer", ErrProtocol)
		}
		next.numSegments, next.txID = r.numSegments, r.txID
		more, err := orderDataOf(next.root.child("body"))
		if err != nil {
			return err
		}
		sb.WriteString(more)
		r = next
	}
	plain, procErr := openDownload(ctx, keys, wrapped, sb.String())
	if procErr == nil {
		procErr = unpack(ctx, plain, sink)
	}
	code := "0"
	if procErr != nil {
		code = "1"
	}
	receipt := phaseRequest(sub, r.txID, "Receipt", 0, false, `<TransferReceipt authenticate="true"><ReceiptCode>`+code+`</ReceiptCode></TransferReceipt>`)
	_, rerr := c.exchange(ctx, sub, keys, bx, signable{doc: receipt})
	if procErr != nil {
		// Processing that stopped because the call's own deadline or cancellation ran out (a slow or unreachable bank ate
		// the budget) is not a fault of the delivered data: it is retried at the next tick and never counted toward parking.
		if ctx.Err() != nil {
			return errors.Join(fmt.Errorf("ebics download: interrupted: %w", procErr), rerr)
		}
		return errors.Join(fmt.Errorf("ebics download: %w: %w", ErrLocal, procErr), rerr)
	}
	if rerr != nil {
		return fmt.Errorf("ebics download receipt: %w", rerr)
	}
	return nil
}

func openDownload(ctx context.Context, keys Keys, wrappedKey []byte, encoded string) ([]byte, error) {
	cipherText, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("order data is not base64: %w", err)
	}
	aesKey, err := keys.DecryptE002(ctx, wrappedKey)
	if err != nil {
		return nil, fmt.Errorf("unwrap transaction key (E002): %w", err)
	}
	return openWithKey(aesKey, cipherText)
}

func decryptBody(ctx context.Context, keys Keys, root *xnode) ([]byte, error) {
	info := root.path("body", "DataTransfer", "DataEncryptionInfo")
	if info == nil || info.attr("authenticate") != "true" {
		return nil, errors.New("response encryption info is not authenticated")
	}
	wrapped, err := base64.StdEncoding.DecodeString(info.child("TransactionKey").content())
	if err != nil {
		return nil, err
	}
	encoded, err := orderDataOf(root.child("body"))
	if err != nil {
		return nil, err
	}
	return openDownload(ctx, keys, wrapped, encoded)
}

// unpack hands a ZIP container's files, or the data as one file, to sink one at a
// time, so only one unpacked file is held at once. A file is capped at
// bankconnect.MaxFileBytes and the files together at maxArchiveBytes, so a small
// archive cannot expand without bound. Every file is tried: a sink refusal does
// not stop the next file, and the refusals are returned together.
func unpack(ctx context.Context, data []byte, sink fileSink) error {
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return sink(ctx, DownloadedFile{Name: "order-data", Data: data})
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	var errs []error
	budget := int64(maxArchiveBytes)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(rc, bankconnect.MaxFileBytes+1))
		rc.Close()
		if err != nil {
			return err
		}
		if len(b) > bankconnect.MaxFileBytes {
			return fmt.Errorf("%s expands beyond %d bytes", f.Name, bankconnect.MaxFileBytes)
		}
		if budget -= int64(len(b)); budget < 0 {
			return fmt.Errorf("archive expands beyond %d bytes", maxArchiveBytes)
		}
		if err := sink(ctx, DownloadedFile{Name: path.Base(f.Name), Data: b}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path.Base(f.Name), err))
		}
	}
	return errors.Join(errs...)
}

// StatementSink takes one downloaded statement file for a bank account; the
// treasury store's ImportStatementFile is the production sink.
type StatementSink func(ctx context.Context, bankAccountID, fileName string, data []byte) error

// FetchStatements downloads end-of-day statements (C53 camt.053, or STA MT940)
// and hands each file to the sink before the bank is told the transfer arrived.
// Every file is tried; if any is refused (or the archive is corrupt) the bank is
// sent a negative receipt, keeps the data and redelivers, and the refusals are
// returned together. The sink replays what it has already stored, so a
// redelivery is harmless. It returns how many files the sink took.
func (c *Client) FetchStatements(ctx context.Context, id, orderType, from, to string, sink StatementSink) (int, error) {
	if orderType != "C53" && orderType != "STA" {
		return 0, fmt.Errorf("%w: %q carries no statements the import reads", ErrUnsupportedOrder, orderType)
	}
	sub, err := c.Store.GetSubscriber(ctx, id)
	if err != nil {
		return 0, err
	}
	taken := 0
	err = c.downloadWith(ctx, id, orderType, from, to, func() fileSink {
		taken = 0
		return func(ctx context.Context, f DownloadedFile) error {
			if err := sink(ctx, sub.BankAccountID, f.Name, f.Data); err != nil {
				return err
			}
			taken++
			return nil
		}
	})
	return taken, err
}
