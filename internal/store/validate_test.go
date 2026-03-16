package store

import (
	"strings"
	"testing"
)

func TestValidateUserID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"empty", "", false},
		{"normal", "user@example.com", false},
		{"max_length", strings.Repeat("a", 255), false},
		{"too_long", strings.Repeat("a", 256), true},
		{"way_too_long", strings.Repeat("x", 1000), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUserID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateUserID(%d chars) error = %v, wantErr %v", len(tt.id), err, tt.wantErr)
			}
		})
	}
}
