package api

import "testing"

func TestLastForwardedFor(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"203.0.113.9"}, "203.0.113.9"},
		{[]string{"1.1.1.1, 203.0.113.9"}, "203.0.113.9"}, // spoofed first entry is ignored
		{[]string{"1.1.1.1", "2001:db8::1"}, "2001:db8::1"},
		{[]string{"203.0.113.9, garbage"}, ""},
	}
	for _, c := range cases {
		if got := lastForwardedFor(c.in); got != c.want {
			t.Errorf("lastForwardedFor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
