package keys

import (
	"errors"
	"strings"
	"testing"
)

type resourceNameTest struct {
	Name string
	In   string

	// Want is a substring of the error, or empty when the name is valid.
	Want string
}

var resourceNameTests = []resourceNameTest{
	{Name: "plain", In: "cpu:heavy"},
	{Name: "every allowed character", In: "a.b_c-1:x.y_z-9"},
	{Name: "exactly the limit", In: "cpu:" + strings.Repeat("a", MaxResourceName-4)},
	{Name: "one byte over the limit", In: "cpu:" + strings.Repeat("a", MaxResourceName-3), Want: "limit is 64"},
	{Name: "empty", In: "", Want: "empty"},
	{Name: "no colon", In: "cpuheavy", Want: "exactly one colon"},
	{Name: "two colons", In: "a:b:c", Want: "exactly one colon"},
	{Name: "empty namespace", In: ":heavy", Want: "non-empty"},
	{Name: "empty name", In: "cpu:", Want: "non-empty"},
	{Name: "a lone colon", In: ":", Want: "non-empty"},
	{Name: "a space", In: "cpu:heavy ", Want: "lowercase letters"},
	{Name: "a newline", In: "cpu:heavy\n", Want: "lowercase letters"},
	{Name: "a slash", In: "db:pg/main", Want: "lowercase letters"},
	{Name: "non-ascii", In: "cpu:héavy", Want: "lowercase letters"},
	{Name: "uppercase names the lowercase spelling", In: "CPU:Heavy", Want: `Did you mean "cpu:heavy"?`},
	{Name: "uppercase in one part", In: "cpu:Heavy", Want: `Did you mean "cpu:heavy"?`},
	{Name: "the reserved namespace", In: "repo:main", Want: "reserved"},
	{Name: "the reserved namespace in capitals", In: "Repo:main", Want: "reserved"},
	{Name: "a namespace that only starts with the reserved one", In: "repository:main"},
}

func TestValidateResource(t *testing.T) {
	for _, test := range resourceNameTests {
		t.Run(test.Name, func(t *testing.T) {
			err := ValidateResource(test.In)

			if test.Want == "" {
				if err != nil {
					t.Fatalf("ValidateResource(%q) = %v, want nil", test.In, err)
				}

				return
			}

			if !errors.Is(err, ErrResourceName) {
				t.Fatalf("ValidateResource(%q) = %v, want ErrResourceName", test.In, err)
			}

			if !strings.Contains(err.Error(), test.Want) {
				t.Errorf("ValidateResource(%q) = %q, want it to contain %q", test.In, err, test.Want)
			}
		})
	}
}

func TestReservedNamespaceNeverGetsALowercaseSuggestion(t *testing.T) {
	err := ValidateResource("REPO:main")
	if err == nil || strings.Contains(err.Error(), "Did you mean") {
		t.Errorf("err = %v, want a refusal with no suggestion", err)
	}
}
