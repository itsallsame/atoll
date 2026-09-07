package recruiting

import "testing"

func TestCommonContractIsVersionedAndCursorIsOpaque(t *testing.T) {
	if ContractVersion == "" {
		t.Fatal("contract version is empty")
	}
	if err := (PageRequest{Cursor: "opaque:/+=", Limit: 100}).Validate(100); err != nil {
		t.Fatalf("valid opaque cursor rejected: %v", err)
	}
	for _, p := range []PageRequest{{Limit: -1}, {Limit: 101}, {Cursor: "bad\nvalue"}} {
		if err := p.Validate(100); err == nil {
			t.Fatalf("invalid page request accepted: %+v", p)
		}
	}
}
