package access

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetPolicyGroup returns policy.yaml with access group name set to exactly
// servers (servers == nil removes the group). Only the top-level "groups:"
// block is rewritten; every other line stays byte for byte, so the pull request
// shows just this change. The result is parsed again, so an invalid edit
// (unknown server, bad name) is refused.
func SetPolicyGroup(data []byte, name string, servers []string) ([]byte, error) {
	p, err := parsePolicyLoose(data)
	if err != nil {
		return nil, err
	}
	groups := map[string][]string{}
	for k, v := range p.Groups {
		groups[k] = v
	}
	if servers == nil {
		delete(groups, name)
	} else {
		groups[name] = append([]string{}, servers...)
	}
	block, err := renderGroups(groups)
	if err != nil {
		return nil, err
	}

	lines := strings.SplitAfter(string(data), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "groups:") {
			start = i
			break
		}
	}
	var out string
	if start < 0 {
		out = strings.TrimRight(string(data), "\n") + "\n\n" +
			"# Access groups: a grant for a group covers its servers, including servers added\n" +
			"# later (by a reviewed change to this file). Managed from the dashboard's group pages.\n" + block
	} else {
		end := start + 1 // the block: following lines that are indented (entries, nested comments)
		for end < len(lines) && len(lines[end]) > 0 && (lines[end][0] == ' ' || lines[end][0] == '\t') {
			end++
		}
		out = strings.Join(lines[:start], "") + block + strings.Join(lines[end:], "")
	}
	if _, err := ParsePolicy([]byte(out)); err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// parsePolicyLoose reads the groups of a policy without the usability checks,
// so a policy can be edited even while some other part is being fixed.
func parsePolicyLoose(data []byte) (*Policy, error) {
	var p Policy
	if err := strictDecode(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", PolicyPath, err)
	}
	return &p, nil
}

func renderGroups(groups map[string][]string) (string, error) {
	if len(groups) == 0 {
		return "groups: {}\n", nil
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("groups:\n")
	for _, n := range names {
		key, err := yaml.Marshal(n) // quotes the name when YAML needs it
		if err != nil {
			return "", err
		}
		srv := append([]string(nil), groups[n]...)
		sort.Strings(srv)
		fmt.Fprintf(&b, "  %s: [%s]\n", strings.TrimSpace(string(key)), strings.Join(srv, ", "))
	}
	return b.String(), nil
}
