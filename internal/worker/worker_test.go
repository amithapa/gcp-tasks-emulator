package worker

import (
	"testing"
)

func TestBackoff(t *testing.T) {
	tests := []struct {
		initial, max, retryCount int
		want                     int
	}{
		{1, 60, 1, 1},
		{1, 60, 2, 2},
		{1, 60, 3, 4},
		{1, 60, 5, 16},
		{1, 60, 10, 60},
		{2, 30, 2, 4},
	}
	for _, tt := range tests {
		got := backoff(tt.initial, tt.max, tt.retryCount)
		if got != tt.want {
			t.Errorf("backoff(%d, %d, %d) = %d, want %d", tt.initial, tt.max, tt.retryCount, got, tt.want)
		}
	}
}
