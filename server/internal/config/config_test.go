package config

import "testing"

func TestForwardPorts(t *testing.T) {
	for _, tc := range []struct {
		value string
		set   bool
		want  int
	}{
		{set: false, want: 10}, // default 7100-7109
		{value: "", set: true, want: 0},
		{value: "off", set: true, want: 0},
		{value: "8000-8001", set: true, want: 2},
	} {
		if tc.set {
			t.Setenv("WEBSSH_FORWARD_PORTS", tc.value)
		}
		c, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if len(c.ForwardPorts) != tc.want {
			t.Errorf("WEBSSH_FORWARD_PORTS=%q (set %v): %d ports, want %d", tc.value, tc.set, len(c.ForwardPorts), tc.want)
		}
	}
}
