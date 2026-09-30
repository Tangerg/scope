package filter

// PathSegment is one step of a selector path. A key selects an object member
// and an index selects an array element; [Match] never lets one stand in for
// the other, so a compiler that renders both as keys selects different
// documents than local evaluation.
type PathSegment struct {
	key     string
	index   uint64
	isIndex bool
}

// Key returns the member name when the segment selects an object member.
func (p PathSegment) Key() (string, bool) {
	return p.key, !p.isIndex
}

// Index returns the zero-based position, at most math.MaxInt64, when the
// segment selects an array element.
func (p PathSegment) Index() (uint64, bool) {
	return p.index, p.isIndex
}
