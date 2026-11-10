package ebics

import (
	"context"

	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
)

// Transport sends bank files through a subscriber as EBICS upload orders, so a
// bankconnect.Sender audits them. The detached signature the Sender made is not
// used: A006 is made over the order inside Upload, with the subscriber's own key.
type Transport struct {
	Client       *Client
	SubscriberID string
}

func (Transport) Protocol() string { return bankconnect.ProtocolEBICS }

func (t Transport) Send(ctx context.Context, f bankconnect.File, _ bankconnect.Signature) (bankconnect.Receipt, error) {
	r, err := t.Client.Upload(ctx, t.SubscriberID, f.OrderType, f.Data)
	if err != nil {
		return bankconnect.Receipt{}, err
	}
	return bankconnect.Receipt{Reference: r.TransactionID}, nil
}
