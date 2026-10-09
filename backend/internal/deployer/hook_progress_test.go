package deployer

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestHookProgress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		lines       []string
		maxLen      int
		want        string
		wantPrefix  string
		wantSuffix  string
		wantMaxBody int
	}{
		{name: "empty", want: ""},
		{name: "fits", lines: []string{"a", "bb"}, maxLen: 10, want: "a\nbb\n"},
		{
			name: "keeps the tail at a line start", lines: []string{"line1", "line2", "line3"}, maxLen: 12,
			want: "[hook output truncated: first 6 bytes not kept]\nline2\nline3\n",
		},
		{
			name: "one line longer than the limit is cut", lines: []string{strings.Repeat("x", 20)}, maxLen: 8,
			wantPrefix: "[hook output truncated: first 13 bytes not kept]\n", wantSuffix: "xxxxxxx\n", wantMaxBody: 8,
		},
		{
			// "é" is 2 bytes. The byte cut (21 - 7 = 14) falls on the
			// second byte of an "é"; the cut moves to the next rune start.
			name: "cut moves to a UTF-8 rune start", lines: []string{"xxx" + strings.Repeat("é", 8) + "y"}, maxLen: 7,
			want: "[hook output truncated: first 15 bytes not kept]\nééy\n",
		},
		{
			name: "compaction keeps the tail", lines: []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8"}, maxLen: 6,
			want: "[hook output truncated: first 18 bytes not kept]\nl7\nl8\n",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			maxLen := tt.maxLen
			if maxLen == 0 {
				maxLen = 10
			}
			p := newHookProgress(maxLen)
			for _, l := range tt.lines {
				p.add(l)
			}
			got := p.String()
			if tt.wantPrefix == "" {
				assert.Equal(t, tt.want, got)
				assert.True(t, utf8.ValidString(got), "no character is cut in half")
				return
			}
			assert.True(t, strings.HasPrefix(got, tt.wantPrefix), got)
			assert.True(t, strings.HasSuffix(got, tt.wantSuffix), got)
			assert.LessOrEqual(t, len(got)-len(tt.wantPrefix), tt.wantMaxBody)
		})
	}
}

func TestHookProgress_ConcurrentAddKeepsLimit(t *testing.T) {
	t.Parallel()
	p := newHookProgress(maxHookProgressLen)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				p.add(fmt.Sprintf("goroutine %d line %d %s", g, i, strings.Repeat("y", 40)))
			}
		}(g)
	}
	wg.Wait()
	got := p.String()
	assert.True(t, strings.HasPrefix(got, "[hook output truncated"))
	assert.LessOrEqual(t, len(got), maxHookProgressLen+100)
}
