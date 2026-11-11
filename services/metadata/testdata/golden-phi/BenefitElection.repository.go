package generated

import "context"

type BenefitElectionRepository interface {
	List(context.Context, int) ([]BenefitElection, error)
	Get(context.Context, string) (BenefitElection, error)
	Create(context.Context, BenefitElection) (BenefitElection, error)
	Update(context.Context, string, map[string]any, int) (BenefitElection, error)
	Delete(context.Context, string, int) error
}
