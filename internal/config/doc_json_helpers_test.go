package config

import "strings"

// extractJSONBlockContaining returns the body of the first ```json fenced code block
// whose contents contain needle.
func extractJSONBlockContaining(doc, needle string) (string, bool) {
	const fence = "```"
	rest := doc
	for {
		i := strings.Index(rest, fence+"json")
		if i < 0 {
			return "", false
		}
		rest = rest[i+len(fence+"json"):]
		nl := strings.IndexByte(rest, '\n')
		if nl < 0 {
			return "", false
		}
		rest = rest[nl+1:]
		end := strings.Index(rest, fence)
		if end < 0 {
			return "", false
		}
		body := rest[:end]
		if strings.Contains(body, needle) {
			return body, true
		}
		rest = rest[end+len(fence):]
	}
}
