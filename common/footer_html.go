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
	"bytes"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// The footer is administrator-controlled, but it is rendered in every user's
// browser.  Keep the server-side policy deliberately small: basic text
// formatting and links are useful in a footer, while embedded documents,
// active content, and arbitrary attributes are not.
var footerAllowedTags = map[string]struct{}{
	"a":          {},
	"b":          {},
	"blockquote": {},
	"br":         {},
	"code":       {},
	"del":        {},
	"dd":         {},
	"dl":         {},
	"dt":         {},
	"em":         {},
	"h1":         {},
	"h2":         {},
	"h3":         {},
	"h4":         {},
	"h5":         {},
	"h6":         {},
	"hr":         {},
	"i":          {},
	"kbd":        {},
	"li":         {},
	"mark":       {},
	"ol":         {},
	"p":          {},
	"pre":        {},
	"q":          {},
	"s":          {},
	"small":      {},
	"span":       {},
	"strong":     {},
	"sub":        {},
	"sup":        {},
	"u":          {},
	"ul":         {},
}

// These elements can contain active content or change the parsing context.
// Drop their complete subtree instead of retaining their text children.  The
// latter matters for <script> and <style>, where the source itself may contain
// confusing markup that should never be reflected into the page.
var footerDropTags = map[string]struct{}{
	"audio":     {},
	"base":      {},
	"button":    {},
	"canvas":    {},
	"embed":     {},
	"form":      {},
	"iframe":    {},
	"input":     {},
	"link":      {},
	"math":      {},
	"meta":      {},
	"object":    {},
	"script":    {},
	"select":    {},
	"source":    {},
	"style":     {},
	"svg":       {},
	"template":  {},
	"textarea":  {},
	"track":     {},
	"video":     {},
	"xmp":       {},
	"noscript":  {},
	"plaintext": {},
}

// SanitizeFooterHTML applies the server-side footer policy.  It is safe to
// call this function more than once; the output contains only nodes and
// attributes accepted by the policy.  A parser or renderer error fails closed
// and returns an empty footer rather than reflecting untrusted input.
func SanitizeFooterHTML(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	// A nil context selects the normal HTML fragment tokenizer.  Supplying a
	// hand-built context requires a matching DataAtom and can otherwise make
	// the parser reject otherwise valid input.
	nodes, err := html.ParseFragment(strings.NewReader(raw), nil)
	if err != nil {
		return ""
	}

	var output bytes.Buffer
	for _, node := range nodes {
		for _, clean := range sanitizeFooterNodes(node) {
			if err := html.Render(&output, clean); err != nil {
				return ""
			}
		}
	}
	return output.String()
}

// sanitizeFooterNodes returns a slice because unknown, non-active elements
// are unwrapped while their safe descendants are retained.
func sanitizeFooterNodes(node *html.Node) []*html.Node {
	switch node.Type {
	case html.TextNode:
		return []*html.Node{{Type: html.TextNode, Data: node.Data}}
	case html.ElementNode:
		tag := strings.ToLower(node.Data)
		if _, drop := footerDropTags[tag]; drop {
			return nil
		}

		children := make([]*html.Node, 0, 1)
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			children = append(children, sanitizeFooterNodes(child)...)
		}

		if _, allowed := footerAllowedTags[tag]; !allowed {
			return children
		}

		clean := &html.Node{
			Type: html.ElementNode,
			Data: tag,
			Attr: sanitizeFooterAttributes(tag, node.Attr),
		}
		for _, child := range children {
			clean.AppendChild(child)
		}
		return []*html.Node{clean}
	default:
		// Comments, doctypes, and any node type not produced by the fragment
		// parser are not needed in a footer.
		return nil
	}
}

func sanitizeFooterAttributes(tag string, attrs []html.Attribute) []html.Attribute {
	clean := make([]html.Attribute, 0, len(attrs))
	seen := make(map[string]struct{}, len(attrs))
	hasHref := false
	targetBlank := false
	var relIndex = -1

	for _, attr := range attrs {
		name := strings.ToLower(strings.TrimSpace(attr.Key))
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}

		switch {
		case tag == "a" && name == "href":
			if !isSafeFooterURL(attr.Val) {
				continue
			}
			clean = append(clean, html.Attribute{Key: "href", Val: strings.TrimSpace(attr.Val)})
			hasHref = true
		case tag == "a" && name == "target":
			target := strings.ToLower(strings.TrimSpace(attr.Val))
			if target != "_blank" && target != "_self" && target != "_parent" && target != "_top" {
				continue
			}
			clean = append(clean, html.Attribute{Key: "target", Val: target})
			targetBlank = target == "_blank"
		case tag == "a" && name == "rel":
			rel := sanitizeFooterRel(attr.Val)
			if rel == "" {
				continue
			}
			relIndex = len(clean)
			clean = append(clean, html.Attribute{Key: "rel", Val: rel})
		case name == "class" || name == "title" || name == "role" || name == "lang" || name == "dir":
			clean = append(clean, html.Attribute{Key: name, Val: attr.Val})
		case strings.HasPrefix(name, "aria-"):
			// ARIA attributes do not execute code and preserve accessibility
			// labels supplied by the administrator.
			clean = append(clean, html.Attribute{Key: name, Val: attr.Val})
		}
	}

	if tag == "a" && targetBlank && hasHref {
		if relIndex < 0 {
			relIndex = len(clean)
			clean = append(clean, html.Attribute{Key: "rel", Val: "noopener noreferrer"})
		} else {
			rel := strings.Fields(clean[relIndex].Val)
			seenRel := make(map[string]struct{}, len(rel))
			for _, token := range rel {
				seenRel[token] = struct{}{}
			}
			for _, required := range []string{"noopener", "noreferrer"} {
				if _, exists := seenRel[required]; !exists {
					rel = append(rel, required)
				}
			}
			clean[relIndex].Val = strings.Join(rel, " ")
		}
	}

	// A target without a surviving link is meaningless and can be surprising
	// to keyboard and assistive-technology users.
	if tag == "a" && !hasHref {
		filtered := clean[:0]
		for _, attr := range clean {
			if attr.Key != "target" && attr.Key != "rel" {
				filtered = append(filtered, attr)
			}
		}
		clean = filtered
	}
	return clean
}

func sanitizeFooterRel(raw string) string {
	allowed := make([]string, 0, 4)
	seen := make(map[string]struct{})
	for _, token := range strings.Fields(strings.ToLower(raw)) {
		// rel tokens are inert, but keeping simple tokens avoids reflecting
		// control characters or malformed attribute data.
		if token == "" || strings.ContainsAny(token, "<>\"'") {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		allowed = append(allowed, token)
	}
	return strings.Join(allowed, " ")
}

func isSafeFooterURL(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" || strings.Contains(value, "\\") {
		return false
	}

	// Browsers ignore ASCII controls around a scheme (for example
	// "java\tscript:").  Normalize them before inspecting the URL.
	normalized := strings.Map(func(r rune) rune {
		if r <= 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	if normalized == "" || strings.HasPrefix(normalized, "//") {
		return false
	}

	parsed, err := url.Parse(normalized)
	if err != nil {
		return false
	}
	if parsed.Scheme == "" {
		return true
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != ""
	case "mailto", "tel":
		return parsed.Opaque != "" || parsed.Path != ""
	default:
		return false
	}
}
