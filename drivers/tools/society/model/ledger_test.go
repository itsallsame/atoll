package model

import (
	"errors"
	"testing"
)

func TestLedgerTransferIsConservativeAndIdempotent(t *testing.T) {
	ledger := newLedger()
	if err := ledger.AddAccount("a", 100); err != nil {
		t.Fatal(err)
	}
	if err := ledger.AddAccount("b", 20); err != nil {
		t.Fatal(err)
	}
	ledger.Freeze()

	first, replayed, err := ledger.Transfer("tx-1", 1, "test", "a", "b", 30)
	if err != nil || replayed || first.Status != TransactionCommitted {
		t.Fatalf("first transfer=(%+v,%v,%v)", first, replayed, err)
	}
	second, replayed, err := ledger.Transfer("tx-1", 1, "test", "a", "b", 30)
	if err != nil || !replayed || second != first {
		t.Fatalf("replay=(%+v,%v,%v)", second, replayed, err)
	}
	if got := ledger.Balance("a"); got != 70 {
		t.Fatalf("a balance=%d", got)
	}
	if got := ledger.Balance("b"); got != 50 {
		t.Fatalf("b balance=%d", got)
	}
	if got := ledger.Total(); got != ledger.InitialTotal() {
		t.Fatalf("money total=%d initial=%d", got, ledger.InitialTotal())
	}
	if got := len(ledger.transactions); got != 1 {
		t.Fatalf("transactions=%d", got)
	}
}

func TestLedgerRejectsInsufficientTransferWithoutMutation(t *testing.T) {
	ledger := newLedger()
	_ = ledger.AddAccount("a", 10)
	_ = ledger.AddAccount("b", 20)
	ledger.Freeze()
	tx, replayed, err := ledger.Transfer("tx-poor", 1, "test", "a", "b", 11)
	if !errors.Is(err, ErrInsufficient) || replayed || tx.Status != TransactionRejected {
		t.Fatalf("transfer=(%+v,%v,%v)", tx, replayed, err)
	}
	if ledger.Balance("a") != 10 || ledger.Balance("b") != 20 {
		t.Fatal("rejected transfer mutated balances")
	}
	if ledger.Total() != ledger.InitialTotal() {
		t.Fatal("rejected transfer changed money supply")
	}
}
