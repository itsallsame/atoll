package model

import "fmt"

type TransactionStatus string

const (
	TransactionCommitted TransactionStatus = "committed"
	TransactionRejected  TransactionStatus = "rejected"
)

type Transaction struct {
	ID     string            `json:"id"`
	Day    int               `json:"day"`
	Kind   string            `json:"kind"`
	From   string            `json:"from"`
	To     string            `json:"to"`
	Amount Money             `json:"amount"`
	Status TransactionStatus `json:"status"`
	Reason string            `json:"reason,omitempty"`
}

type Ledger struct {
	balances     map[string]Money
	transactions []Transaction
	seen         map[string]int
	initialTotal Money
	frozen       bool
}

func newLedger() *Ledger {
	return &Ledger{balances: map[string]Money{}, seen: map[string]int{}}
}

func (l *Ledger) AddAccount(id string, balance Money) error {
	if l.frozen || id == "" || balance < 0 {
		return ErrInvalidTransfer
	}
	if _, exists := l.balances[id]; exists {
		return fmt.Errorf("%w: duplicate account %q", ErrInvalidTransfer, id)
	}
	l.balances[id] = balance
	return nil
}

func (l *Ledger) OpenZeroAccount(id string) error {
	if id == "" {
		return ErrInvalidTransfer
	}
	if _, exists := l.balances[id]; exists {
		return fmt.Errorf("%w: duplicate account %q", ErrInvalidTransfer, id)
	}
	l.balances[id] = 0
	return nil
}

func (l *Ledger) Freeze() {
	l.initialTotal = l.Total()
	l.frozen = true
}

func (l *Ledger) Balance(id string) Money { return l.balances[id] }

func (l *Ledger) InitialTotal() Money { return l.initialTotal }

func (l *Ledger) Total() Money {
	var total Money
	for _, balance := range l.balances {
		total += balance
	}
	return total
}

func (l *Ledger) Transfer(id string, day int, kind, from, to string, amount Money) (Transaction, bool, error) {
	if index, exists := l.seen[id]; exists {
		return l.transactions[index], true, nil
	}
	tx := Transaction{ID: id, Day: day, Kind: kind, From: from, To: to, Amount: amount}
	if id == "" || from == "" || to == "" || from == to || amount <= 0 {
		tx.Status, tx.Reason = TransactionRejected, ErrInvalidTransfer.Error()
		l.record(tx)
		return tx, false, ErrInvalidTransfer
	}
	fromBalance, fromExists := l.balances[from]
	_, toExists := l.balances[to]
	if !fromExists || !toExists {
		tx.Status, tx.Reason = TransactionRejected, ErrUnknownAccount.Error()
		l.record(tx)
		return tx, false, ErrUnknownAccount
	}
	if fromBalance < amount {
		tx.Status, tx.Reason = TransactionRejected, ErrInsufficient.Error()
		l.record(tx)
		return tx, false, ErrInsufficient
	}
	l.balances[from] -= amount
	l.balances[to] += amount
	tx.Status = TransactionCommitted
	l.record(tx)
	return tx, false, nil
}

func (l *Ledger) record(tx Transaction) {
	l.seen[tx.ID] = len(l.transactions)
	l.transactions = append(l.transactions, tx)
}

func (l *Ledger) balancesCopy() map[string]Money {
	out := make(map[string]Money, len(l.balances))
	for id, balance := range l.balances {
		out[id] = balance
	}
	return out
}

func (l *Ledger) transactionsCopy() []Transaction {
	return append([]Transaction(nil), l.transactions...)
}
