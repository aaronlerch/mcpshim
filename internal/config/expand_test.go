package config

import "testing"

func TestExpandEnv(t *testing.T) {
	t.Setenv("MCPSHIM_TEST_SET", "hello")
	t.Setenv("MCPSHIM_TEST_EMPTY", "")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"braced set", "${MCPSHIM_TEST_SET}", "hello"},
		{"braced unset", "${MCPSHIM_TEST_UNSET}", ""},
		{"braced empty no default", "${MCPSHIM_TEST_EMPTY}", ""},
		{"default unset", "${MCPSHIM_TEST_UNSET:-fallback}", "fallback"},
		{"default empty", "${MCPSHIM_TEST_EMPTY:-fallback}", "fallback"},
		{"default set", "${MCPSHIM_TEST_SET:-fallback}", "hello"},
		{"empty default", "${MCPSHIM_TEST_UNSET:-}", ""},
		{"default with spaces", "${MCPSHIM_TEST_UNSET:-with spaces}", "with spaces"},
		{"bare var set", "$MCPSHIM_TEST_SET", "hello"},
		{"bare var unset", "$MCPSHIM_TEST_UNSET", ""},
		{"escape literal", "literal $$ stays", "literal $ stays"},
		{"trailing dollar", "trail$", "trail$"},
		{"unclosed brace", "${UNCLOSED", "${UNCLOSED"},
		{"prefix and suffix", "before-${MCPSHIM_TEST_SET}-after", "before-hello-after"},
		{"two vars", "${MCPSHIM_TEST_SET}/${MCPSHIM_TEST_SET}", "hello/hello"},
		{"dollar then symbol", "price: $5.00", "price: $5.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandEnv(tc.in)
			if got != tc.want {
				t.Fatalf("expandEnv(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
