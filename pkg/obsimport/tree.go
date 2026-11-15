package obsimport

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/openSUSE/bsgo/pkg/buildconfig"
)

var (
	ifStartRe  = regexp.MustCompile(`^\s*%if`)
	elseLikeRe = regexp.MustCompile(`^\s*%(?:else|elif|elifarch|elifos)\b`)
	endifRe    = regexp.MustCompile(`^\s*%endif\b`)
)

// condNode is one %if ... %endif block. body holds the nodes of the first
// branch; other is set when an %else or %elif branch exists.
type condNode struct {
	head  buildconfig.Item
	tail  buildconfig.Item
	body  []treeNode
	other bool
	// raw is the verbatim text of the whole block, nested blocks included.
	raw string
}

// treeNode is a leaf item or a conditional block.
type treeNode struct {
	item *buildconfig.Item
	cond *condNode
}

type frame struct {
	cond  *condNode
	parts []string
	inOld bool // inside an %else/%elif branch: nodes are not kept
	out   *[]treeNode
}

// A stray %endif or %else outside any conditional is kept as a leaf: the
// parser ignores it, and so does the converter (it stays verbatim).

// buildTree groups logical items into leaves and nested conditional blocks.
func buildTree(items []buildconfig.Item) ([]treeNode, error) {
	var root []treeNode
	stack := []*frame{{out: &root}}
	for i := range items {
		it := items[i]
		for _, f := range stack[1:] {
			f.parts = append(f.parts, it.Raw)
		}
		top := stack[len(stack)-1]
		var err error
		stack, err = step(stack, top, &it)
		if err != nil {
			return nil, err
		}
	}
	if len(stack) > 1 {
		return nil, fmt.Errorf("line %d: %%if without matching %%endif", stack[len(stack)-1].cond.head.Line)
	}
	return root, nil
}

func step(stack []*frame, top *frame, it *buildconfig.Item) ([]*frame, error) {
	switch {
	case it.Macros:
		top.add(treeNode{item: it})
	case ifStartRe.MatchString(it.Text):
		c := &condNode{head: *it}
		top.add(treeNode{cond: c})
		stack = append(stack, &frame{cond: c, parts: []string{it.Raw}, out: &c.body, inOld: top.inOld})
	case elseLikeRe.MatchString(it.Text) && len(stack) > 1:
		top.cond.other = true
		top.inOld = true
	case endifRe.MatchString(it.Text) && len(stack) > 1:
		top.cond.tail = *it
		top.cond.raw = strings.Join(top.parts, "\n")
		stack = stack[:len(stack)-1]
	default:
		top.add(treeNode{item: it})
	}
	return stack, nil
}

func (f *frame) add(n treeNode) {
	if f.inOld {
		return
	}
	*f.out = append(*f.out, n)
}
