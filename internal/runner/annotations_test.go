package runner

import "testing"

func TestParseAnnotation(t *testing.T) {
	cases := []struct {
		name string
		line string
		want *annotation
	}{
		{"bare error", "::error::something broke\n",
			&annotation{Level: "error", Message: "something broke"}},
		{"warning with props", "::warning file=app.js,line=10,col=5::unused var\n",
			&annotation{Level: "warning", Message: "unused var", File: "app.js", Line: 10, Col: 5}},
		{"full span + title", "::error file=a.ts,line=1,col=2,endLine=3,endColumn=4,title=Bad::oops\n",
			&annotation{Level: "error", Message: "oops", Title: "Bad", File: "a.ts", Line: 1, Col: 2, EndLine: 3, EndCol: 4}},
		{"message escapes", "::notice::100%25 done%0Aline2\n",
			&annotation{Level: "notice", Message: "100% done\nline2"}},
		{"literal percent-escape survives", "::notice::see %250A here\n",
			&annotation{Level: "notice", Message: "see %0A here"}},
		{"prop comma escape", "::warning title=a%2Cb::msg\n",
			&annotation{Level: "warning", Message: "msg", Title: "a,b"}},
		{"leading whitespace ok", "  ::debug::trace\n",
			&annotation{Level: "debug", Message: "trace"}},
		{"unknown command ignored", "::set-env FOO=bar::x\n", nil},
		{"plain output ignored", "hello world\n", nil},
		{"colon but not command", "::notacommand::x\n", nil},
		{"command without separator", "::error file=x\n", nil},
		{"empty message ignored", "::error::\n", nil},
		{"mid-line command ignored", "prefix ::error::x\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAnnotation([]byte(tc.line))
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %+v, got nil", tc.want)
			}
			if *got != *tc.want {
				t.Fatalf("got %+v, want %+v", *got, *tc.want)
			}
		})
	}
}

func TestAnnotationSinkCap(t *testing.T) {
	var s annotationSink
	for i := 0; i < maxStepAnnotations+10; i++ {
		s.add(annotation{Level: "notice", Message: "x"})
	}
	if n := len(s.list()); n != maxStepAnnotations {
		t.Fatalf("cap: got %d, want %d", n, maxStepAnnotations)
	}
	s.reset()
	if n := len(s.list()); n != 0 {
		t.Fatalf("reset: got %d items", n)
	}
}
