package main

import (
	"strings"
	"testing"

	"github.com/joelkehle/pinakes/pkg/bus"
)

func TestParseNamespaceConfig(t *testing.T) {
	tests := []struct {
		name            string
		mode            string
		legacyScope     string
		wantMode        bus.NamespaceMode
		wantLegacyScope bus.Scope
		wantErr         string
	}{
		{
			name:            "defaults",
			wantMode:        bus.NamespaceModeCompat,
			wantLegacyScope: bus.ScopeUCLA,
		},
		{
			name:            "explicit personal compatibility",
			mode:            " compat ",
			legacyScope:     " personal ",
			wantMode:        bus.NamespaceModeCompat,
			wantLegacyScope: bus.ScopePersonal,
		},
		{
			name:            "strict ucla",
			mode:            "strict",
			legacyScope:     "ucla",
			wantMode:        bus.NamespaceModeStrict,
			wantLegacyScope: bus.ScopeUCLA,
		},
		{
			name:        "invalid mode fails closed",
			mode:        "compatible",
			legacyScope: "ucla",
			wantErr:     "BUS_NAMESPACE_MODE",
		},
		{
			name:        "invalid legacy scope fails closed",
			mode:        "compat",
			legacyScope: "shared",
			wantErr:     "BUS_LEGACY_SCOPE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMode, gotLegacyScope, err := parseNamespaceConfig(tt.mode, tt.legacyScope)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseNamespaceConfig() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseNamespaceConfig() error = %v", err)
			}
			if gotMode != tt.wantMode || gotLegacyScope != tt.wantLegacyScope {
				t.Fatalf("parseNamespaceConfig() = (%q, %q), want (%q, %q)", gotMode, gotLegacyScope, tt.wantMode, tt.wantLegacyScope)
			}
		})
	}
}
