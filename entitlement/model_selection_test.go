package entitlement

import "testing"

func TestModelTierForSet(t *testing.T) {
	cases := []struct {
		name string
		ents []string
		want ModelTier
	}{
		{"empty", nil, ModelTierHaiku},
		{"basic only", []string{string(EveBasic)}, ModelTierHaiku},
		{"full", []string{string(EveFull)}, ModelTierSonnet},
		{"full + max", []string{string(EveFull), string(EveMaxQuota)}, ModelTierOpus},
		{"max without full", []string{string(EveMaxQuota)}, ModelTierHaiku},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := NewSet(c.ents)
			got := ModelTierForSet(set)
			if got != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
}

func TestSelectModelID(t *testing.T) {
	full := NewSet([]string{string(EveFull)})
	if SelectModelID(full) != "claude-sonnet-4-6" {
		t.Fatalf("expected sonnet, got %s", SelectModelID(full))
	}
	max := NewSet([]string{string(EveFull), string(EveMaxQuota)})
	if SelectModelID(max) != "claude-opus-4-7" {
		t.Fatalf("expected opus, got %s", SelectModelID(max))
	}
}

func TestFilterTools(t *testing.T) {
	tools := []ToolDescriptor{
		{Name: "search.text"},                                  // unrestricted
		{Name: "search.semantic", RequiredEntitlement: SearchSemantic},
		{Name: "genesis.propose", RequiredEntitlement: GenesisEngine},
	}
	free := NewSet([]string{string(SearchInverted), string(EveBasic)})
	got := FilterTools(free, tools)
	if len(got) != 1 || got[0].Name != "search.text" {
		t.Fatalf("free tier should only get unrestricted tool, got %v", got)
	}

	paid := NewSet([]string{string(SearchSemantic), string(GenesisEngine)})
	got = FilterTools(paid, tools)
	if len(got) != 3 {
		t.Fatalf("paid tier should get all 3, got %d", len(got))
	}
}

func TestFilterTools_NilSet(t *testing.T) {
	tools := []ToolDescriptor{
		{Name: "open"},
		{Name: "gated", RequiredEntitlement: GenesisEngine},
	}
	got := FilterTools(nil, tools)
	if len(got) != 1 || got[0].Name != "open" {
		t.Fatalf("nil set should only get unrestricted tools, got %v", got)
	}
}
