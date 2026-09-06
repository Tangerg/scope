package html_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl/html"
)

func TestBlockWordBoundaries(t *testing.T) {
	for _, test := range []struct{ name, source, want string }{
		{name: "paragraphs", source: "<p>alpha</p><p>beta</p>", want: "alpha beta"},
		{name: "inline", source: "<p>inter<strong>oper</strong>able</p>", want: "interoperable"},
		{name: "line breaks", source: "alpha<br>beta", want: "alpha beta"},
		{name: "table cells", source: "<table><tr><td>alpha</td><td>beta</td></tr></table>", want: "alpha beta"},
		{name: "non-content", source: "<p>alpha<script>hidden</script><style>hidden</style></p><p>beta</p>", want: "alpha beta"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, err := html.NewReader(strings.NewReader(test.source), html.ReaderConfig{})
			if err != nil {
				t.Fatal(err)
			}
			docs, err := r.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(docs) != 1 || docs[0].Text != test.want {
				t.Fatalf("documents = %#v, want one with text %q", docs, test.want)
			}
		})
	}
}
