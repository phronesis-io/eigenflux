package main

import (
	"errors"
	"testing"

	"eigenflux_server/pkg/config"
)

func TestValidateConfigurationRequiresCommissionIndex(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     *config.Config
		wantErr bool
	}{
		{name: "nil", wantErr: true},
		{name: "disabled", cfg: &config.Config{}, wantErr: true},
		{name: "enabled", cfg: &config.Config{EnableCommissionIndex: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfiguration(tc.cfg)
			if tc.wantErr && !errors.Is(err, errCommissionIndexDisabled) {
				t.Fatalf("validateConfiguration() error=%v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateConfiguration() error=%v", err)
			}
		})
	}
}
