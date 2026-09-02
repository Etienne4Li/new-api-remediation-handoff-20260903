/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeFooterHTMLRemovesActiveContentAndDangerousURLs(t *testing.T) {
	raw := `<p onclick="alert(1)">Welcome</p><script>alert(2)</script><iframe src="https://evil.example"></iframe><a href="javascript:alert(3)">bad</a><a href="https://example.com" target="_blank">good</a>`

	got := SanitizeFooterHTML(raw)

	assert.Contains(t, got, "<p>Welcome</p>")
	assert.Contains(t, got, `<a href="https://example.com" target="_blank" rel="noopener noreferrer">good</a>`)
	assert.NotContains(t, strings.ToLower(got), "script")
	assert.NotContains(t, strings.ToLower(got), "iframe")
	assert.NotContains(t, strings.ToLower(got), "onclick")
	assert.NotContains(t, strings.ToLower(got), "javascript:")
}

func TestSanitizeFooterHTMLKeepsSafeFormattingAndRelativeLinks(t *testing.T) {
	raw := `<strong>Important</strong><br><span class="muted">text</span><a href="/terms" title="Terms">Terms</a><a href="mailto:help@example.com">Email</a>`

	got := SanitizeFooterHTML(raw)

	assert.Contains(t, got, `<strong>Important</strong>`)
	assert.Contains(t, got, `<br/>`)
	assert.Contains(t, got, `<span class="muted">text</span>`)
	assert.Contains(t, got, `<a href="/terms" title="Terms">Terms</a>`)
	assert.Contains(t, got, `<a href="mailto:help@example.com">Email</a>`)
}

func TestSanitizeFooterHTMLIsIdempotent(t *testing.T) {
	raw := `<a href="https://example.com" target="_blank" rel="noopener">link</a>`
	first := SanitizeFooterHTML(raw)
	second := SanitizeFooterHTML(first)

	require.NotEmpty(t, first)
	assert.Equal(t, first, second)
}

func TestSanitizeFooterHTMLDropsUnknownWrapperButKeepsSafeDescendants(t *testing.T) {
	got := SanitizeFooterHTML(`<marquee><em>hello</em><style>body{display:none}</style></marquee>`)

	assert.Equal(t, `<em>hello</em>`, got)
}
