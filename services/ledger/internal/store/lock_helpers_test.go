package store

// Current entity tags of configuration rows, for tests that replace a row they created earlier (a replace
// must send If-Match). A missing row has tag 0.

func nameTag(e env, code string) int {
	n, err := e.store.GetJournalName(e.ctx, code)
	if err != nil {
		return 0
	}
	return n.LockVersion
}

func structTag(e env, ledgerID, id string) int {
	s, err := e.store.GetStructure(e.ctx, ledgerID, id)
	if err != nil {
		return 0
	}
	return s.LockVersion
}

func accountTag(e env, chart, number string) int {
	list, err := e.store.ListAccounts(e.ctx, chart, "", 1000)
	if err != nil {
		return 0
	}
	for _, a := range list {
		if a.Number == number {
			return a.LockVersion
		}
	}
	return 0
}

func dimTag(e env, ledgerID, attribute, value string) int {
	list, err := e.store.ListDimensionValues(e.ctx, ledgerID)
	if err != nil {
		return 0
	}
	for _, v := range list {
		if v.Attribute == attribute && v.Value == value {
			return v.LockVersion
		}
	}
	return 0
}

func templateTag(e env, id string) int {
	d, err := e.store.GetDescriptionTemplate(e.ctx, id)
	if err != nil {
		return 0
	}
	return d.LockVersion
}
