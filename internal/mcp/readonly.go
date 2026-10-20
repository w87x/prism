package mcp

import (
	"strings"
	"unicode"
)

// Many MCP servers never send the readOnlyHint annotation, so every one of their tools was treated as "exec": a plain search or listing asked
// for approval, and kept asking once a result had tainted the turn. A tool whose name is only reading verbs (list, search, status…) and has no
// verb that changes anything is read-only for PRISM too. An explicit annotation still wins; a name with any mutating word stays exec.
var readVerbs = map[string]bool{
	"list": true, "search": true, "get": true, "read": true, "find": true, "status": true, "describe": true, "info": true, "show": true,
	"query": true, "check": true, "lookup": true, "categories": true, "browse": true, "view": true, "count": true, "stat": true, "stats": true,
	"indexers": true, "tree": true, "history": true, "log": true, "logs": true, "capabilities": true, "version": true, "ping": true,
}

var mutatingVerbs = map[string]bool{
	"add": true, "create": true, "set": true, "update": true, "delete": true, "remove": true, "clear": true, "complete": true, "pause": true,
	"resume": true, "start": true, "stop": true, "run": true, "exec": true, "execute": true, "write": true, "move": true, "rename": true,
	"edit": true, "send": true, "post": true, "put": true, "push": true, "kill": true, "restart": true, "reset": true, "install": true,
	"uninstall": true, "upload": true, "download": true, "enable": true, "disable": true, "grant": true, "revoke": true, "format": true,
	"wipe": true, "drop": true, "pay": true, "buy": true, "transfer": true, "publish": true, "save": true, "import": true, "apply": true,
	"commit": true, "merge": true, "cancel": true, "approve": true, "reject": true, "schedule": true, "trigger": true, "toggle": true,
}

func nameWords(name string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	rs := []rune(name)
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && i > 0 && unicode.IsLower(rs[i-1]): // camelCase
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

// looksReadOnly reports whether a tool's own name says it only reads.
func looksReadOnly(name string) bool {
	read := false
	for _, w := range nameWords(name) {
		if mutatingVerbs[w] {
			return false
		}
		if readVerbs[w] {
			read = true
		}
	}
	return read
}
