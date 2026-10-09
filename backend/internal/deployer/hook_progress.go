package deployer

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// maxHookProgressLen limits the hook progress lines (LOG: lines of a
// pre-deploy or pre-rollback hook) kept for the deployment log output, so
// that the Helm output and the final ERROR line always fit in maxOutputLen.
const maxHookProgressLen = 16 * 1024

// hookProgress collects the progress lines of a hook for the deployment log
// output. It keeps the last maxLen bytes (the newest lines) and counts the
// bytes it dropped. It is safe for concurrent use.
type hookProgress struct {
	mu      sync.Mutex
	buf     strings.Builder
	maxLen  int
	dropped int
}

func newHookProgress(maxLen int) *hookProgress {
	return &hookProgress{maxLen: maxLen}
}

// add appends one line. The buffer is compacted to the tail only when it is
// larger than 2×maxLen, so a chatty hook does not copy the buffer per line.
func (p *hookProgress) add(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf.WriteString(line)
	p.buf.WriteByte('\n')
	if p.buf.Len() <= 2*p.maxLen {
		return
	}
	s := p.buf.String()
	cut := tailCut(s, p.maxLen)
	p.dropped += cut
	p.buf.Reset()
	p.buf.WriteString(s[cut:])
}

// String returns the kept lines (at most maxLen bytes). When lines were
// dropped, a first line says how many bytes are missing.
func (p *hookProgress) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.buf.String()
	cut := tailCut(s, p.maxLen)
	dropped := p.dropped + cut
	if dropped == 0 {
		return s
	}
	return fmt.Sprintf("[hook output truncated: first %d bytes not kept]\n", dropped) + s[cut:]
}

// tailCut returns the start index of the last maxLen bytes of s. The cut
// moves forward to the next line start, so no line is cut in half; a single
// line longer than maxLen is cut at a UTF-8 rune start, so no character is
// cut in half.
func tailCut(s string, maxLen int) int {
	cut := len(s) - maxLen
	if cut <= 0 {
		return 0
	}
	if s[cut-1] == '\n' {
		return cut
	}
	if i := strings.IndexByte(s[cut:], '\n'); i >= 0 && cut+i+1 < len(s) {
		return cut + i + 1
	}
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return cut
}
