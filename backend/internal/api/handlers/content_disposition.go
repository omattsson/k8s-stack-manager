package handlers

import (
	"fmt"
	"strings"
)

// attachmentDisposition returns a Content-Disposition value for a download:
//
//	attachment; filename="<ascii name>"; filename*=UTF-8''<percent-encoded name>
//
// The quoted filename holds an ASCII-safe copy (characters outside
// [A-Za-z0-9._-] become "_"), so quotes, semicolons and line breaks in an
// instance or chart name cannot break the header. The RFC 5987 filename*
// form keeps the exact name for clients that support it.
func attachmentDisposition(filename string) string {
	var safe strings.Builder
	for _, r := range filename {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			safe.WriteRune(r)
		default:
			safe.WriteByte('_')
		}
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, safe.String(), rfc5987Encode(filename))
}

// rfc5987Encode percent-encodes s for an RFC 5987 ext-value (attr-char
// characters stay as they are).
func rfc5987Encode(s string) string {
	const attrChars = "!#$&+-.^_`|~"
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte(attrChars, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
