package enrichment

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDropDuplicateDescription(t *testing.T) {
	cases := map[string]struct {
		title       string
		description string
		want        *string
	}{
		"an exact repeat is dropped": {
			title:       "Amazon.com: A Thing",
			description: "Amazon.com: A Thing",
		},
		"a case difference is still the same text": {
			title:       "Amazon.com: A Thing",
			description: "amazon.com: a thing",
		},
		"a whitespace difference is still the same text": {
			title:       "Amazon.com:   A Thing",
			description: " Amazon.com: A Thing ",
		},
		"a different description is kept": {
			title:       "A Thing",
			description: "A longer description of the thing.",
			want:        optionalText("A longer description of the thing."),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metadata := dropDuplicateDescription(Metadata{
				Title:       optionalText(tc.title),
				Description: optionalText(tc.description),
			})

			if tc.want == nil {
				require.Nil(t, metadata.Description)
			} else {
				require.Equal(t, *tc.want, *metadata.Description)
			}
		})
	}
}

func TestDropDuplicateDescription_NoTitleLeavesTheDescription(t *testing.T) {
	description := "A description without a title."
	metadata := dropDuplicateDescription(Metadata{
		Description: &description,
	})

	require.Equal(t, description, *metadata.Description)
}
