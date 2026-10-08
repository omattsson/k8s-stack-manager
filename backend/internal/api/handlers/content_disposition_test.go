package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAttachmentDisposition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain name", in: "stack-a-values.zip", want: `attachment; filename="stack-a-values.zip"; filename*=UTF-8''stack-a-values.zip`},
		{name: "quote and semicolon", in: `a"b;c.yaml`, want: `attachment; filename="a_b_c.yaml"; filename*=UTF-8''a%22b%3Bc.yaml`},
		{name: "non-ASCII", in: "stäck.zip", want: `attachment; filename="st_ck.zip"; filename*=UTF-8''st%C3%A4ck.zip`},
		{name: "line break", in: "a\r\nb.zip", want: `attachment; filename="a__b.zip"; filename*=UTF-8''a%0D%0Ab.zip`},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, attachmentDisposition(tt.in))
		})
	}
}
