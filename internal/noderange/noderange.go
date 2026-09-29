// Package noderange implements xCAT noderange expansion without a database.
//
// Grammar (ported from xCAT perl-xCAT/xCAT/NodeRange.pm):
//
//	noderange := term ((',' | ',-' | '@') term)*
//	term      := ['-'] atom | '@' atom | '^' filepath | '(' noderange ')'
//	atom      := nodename (any string, returned as-is)
//	           | '/' regex        (anchored against a known node list, if provided)
//	           | prefix '[' item (',' item)* ']' suffix   (suffix may hold more brackets)
//	           | name ('-'|':') name  (exactly one numeric field may differ; padding kept)
//	           | name num '+' N     (node10+5 => node10..node15)
//	           | digits             ("node" prefix implied via XCAT_NODE_PREFIX/SUFFIX)
//	item      := num | num ('-'|':') num
//
// Exclusions (,-atom / -(...)) are delayed to the end so they always beat
// inclusions. '@' intersects with everything accumulated so far. The result
// is sorted and deduplicated, matching NodeRange.pm:756.
package noderange

// Expand expands a noderange expression into a sorted, unique list of node
// names without consulting any database. Unknown atoms are passed through
// as literal node names (verify=0 semantics); missed returns atoms that
// failed to parse.
func Expand(rangeExpr string) (nodes []string, missed []string) {
	return expandOpts(rangeExpr, nil)
}

// ExpandWithKnown additionally evaluates /regex/ atoms against a
// known-node set.
func ExpandWithKnown(rangeExpr string, known map[string]bool) (nodes []string, missed []string) {
	return expandOpts(rangeExpr, known)
}
