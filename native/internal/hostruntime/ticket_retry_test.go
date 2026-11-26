package hostruntime

import "testing"

func TestTicketRejectedCodes(t *testing.T) {
	// Runtime ticket rejection is no longer a blind refresh signal. The host
	// reports manual_login_required and the manager mints lazily for that state.
	cases := map[int]bool{
		-10: false,
		33:  false,
		0:   false,
		-1:  false,
		1:   false,
		100: false,
	}
	for code, want := range cases {
		if got := ticketRejected(code); got != want {
			t.Fatalf("ticketRejected(%d)=%v want %v", code, got, want)
		}
	}
}
