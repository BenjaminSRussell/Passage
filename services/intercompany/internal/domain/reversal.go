package domain

// ReverseJournal returns the journal that reverses j leg by leg (BR-40.11):
// when a centralized bank payment fails (wave 19 return), both entities'
// postings reverse in one saga under the original pair id. Every leg keeps its
// entity, account and amount and flips its side, so the reversal balances in
// each entity whenever j does.
func ReverseJournal(j Journal) (Journal, error) {
	switch {
	case j.PairID == "":
		return Journal{}, invalid("pair id is required to reverse a journal")
	case len(j.Legs) == 0:
		return Journal{}, invalid("journal has no legs to reverse")
	}
	if err := j.Balanced(); err != nil {
		return Journal{}, err
	}
	out := Journal{PairID: j.PairID, Legs: make([]Leg, 0, len(j.Legs))}
	for _, l := range j.Legs {
		out.Legs = append(out.Legs, Leg{Entity: l.Entity, Account: l.Account, Side: l.Side.Opposite(), Amount: l.Amount})
	}
	return out, nil
}
