package probe

import "testing"

func TestReady(t *testing.T) {
	cases := []struct {
		name       string
		registered bool
		streams    int
		draining   bool
		wantReady  bool
		wantReason string
	}{
		{"draining 优先于一切", true, 1, true, false, "draining"},
		{"未注册", false, 1, false, false, "not registered"},
		{"无网关流", true, 0, false, false, "no gateway stream"},
		{"就绪", true, 1, false, true, "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Checker{
				Registered: func() bool { return tc.registered },
				Streams:    func() int { return tc.streams },
				IsDraining: func() bool { return tc.draining },
			}
			got, reason := c.Ready()
			if got != tc.wantReady || reason != tc.wantReason {
				t.Fatalf("Ready() = (%v, %q), want (%v, %q)", got, reason, tc.wantReady, tc.wantReason)
			}
			if got := c.Draining(); got != tc.draining {
				t.Fatalf("Draining() = %v, want %v", got, tc.draining)
			}
		})
	}
}
