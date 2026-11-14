package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/sdegroot/portablevps/internal/secrets"
)

// opRunner fakes the `op` CLI: it returns out, or fails with err.
type opRunner struct {
	out string
	err error
}

func (f opRunner) Run(_, _ string, _ ...string) (string, error) { return f.out, f.err }

func TestResolveSecretStrict(t *testing.T) {
	env := map[string]string{"SET": "s3cret"}
	getenv := func(k string) string { return env[k] }
	cases := []struct {
		name    string
		runner  opRunner
		value   string
		want    string
		wantErr string
	}{
		{"literal passes through", opRunner{}, "plain-password", "plain-password", ""},
		{"op reference resolves", opRunner{out: "from-op\n"}, "op://Vault/item/root password", "from-op", ""},
		// The regression: a failed `op read` must not yield the reference text.
		{"failed op read is an error", opRunner{err: errors.New("account is not signed in")}, "op://Vault/item/root password", "", "op read"},
		{"empty op value is an error", opRunner{out: "\n"}, "op://Vault/item/root password", "", "empty value"},
		{"env reference resolves", opRunner{}, "env://SET", "s3cret", ""},
		{"unset env is an error", opRunner{}, "env://UNSET", "", "empty or unset"},
	}
	for _, c := range cases {
		r := secrets.Resolver{Runner: c.runner, Getenv: getenv}
		got, err := resolveSecretStrict(r, c.value)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want one containing %q", c.name, err, c.wantErr)
			}
			if got != "" {
				t.Errorf("%s: got %q alongside an error, want empty", c.name, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}
