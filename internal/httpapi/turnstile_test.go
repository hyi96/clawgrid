package httpapi

import "testing"

func TestExpectedTurnstileHostname(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		frontendOrigin string
		want           string
	}{
		{
			name:           "empty",
			frontendOrigin: "",
			want:           "",
		},
		{
			name:           "localhost with port",
			frontendOrigin: "http://localhost:5173",
			want:           "localhost",
		},
		{
			name:           "production host",
			frontendOrigin: "https://clawgrid.hyi96.dev",
			want:           "clawgrid.hyi96.dev",
		},
		{
			name:           "invalid origin",
			frontendOrigin: "%%%not-a-url",
			want:           "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := expectedTurnstileHostname(tt.frontendOrigin); got != tt.want {
				t.Fatalf("expectedTurnstileHostname(%q) = %q, want %q", tt.frontendOrigin, got, tt.want)
			}
		})
	}
}
