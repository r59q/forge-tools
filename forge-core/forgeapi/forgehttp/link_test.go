package forgehttp

import (
	"net/http"
	"reflect"
	"testing"
)

func TestParseLinks(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   Links
	}{
		{"none", nil, nil},
		{"empty", []string{""}, nil},
		{
			"github",
			[]string{`<https://api.github.com/user/repos?page=3&per_page=100>; rel="next", <https://api.github.com/user/repos?page=50&per_page=100>; rel="last"`},
			Links{
				{URL: "https://api.github.com/user/repos?page=3&per_page=100", Rel: "next"},
				{URL: "https://api.github.com/user/repos?page=50&per_page=100", Rel: "last"},
			},
		},
		{
			"gitlab four rels",
			[]string{`<https://gitlab.example/api/v4/projects?page=1>; rel="prev", <https://gitlab.example/api/v4/projects?page=3>; rel="next", <https://gitlab.example/api/v4/projects?page=1>; rel="first", <https://gitlab.example/api/v4/projects?page=9>; rel="last"`},
			Links{
				{URL: "https://gitlab.example/api/v4/projects?page=1", Rel: "prev"},
				{URL: "https://gitlab.example/api/v4/projects?page=3", Rel: "next"},
				{URL: "https://gitlab.example/api/v4/projects?page=1", Rel: "first"},
				{URL: "https://gitlab.example/api/v4/projects?page=9", Rel: "last"},
			},
		},
		{
			"several header lines",
			[]string{`<https://f.example/a?page=2>; rel="next"`, `<https://f.example/a?page=5>; rel="last"`},
			Links{{URL: "https://f.example/a?page=2", Rel: "next"}, {URL: "https://f.example/a?page=5", Rel: "last"}},
		},
		{
			"comma in url and quoted param",
			[]string{`<https://f.example/a?q=x,y>; rel="next"; title="a, \"b\""`},
			Links{{URL: "https://f.example/a?q=x,y", Rel: "next", Params: map[string]string{"title": `a, "b"`}}},
		},
		{
			"unquoted rel and odd spacing",
			[]string{` <https://f.example/2> ;rel=next ,<https://f.example/1>;REL="prev"`},
			Links{{URL: "https://f.example/2", Rel: "next"}, {URL: "https://f.example/1", Rel: "prev"}},
		},
		{
			"multiple rel types",
			[]string{`<https://f.example/9>; rel="next last"`},
			Links{{URL: "https://f.example/9", Rel: "next last"}},
		},
		{
			"first rel wins",
			[]string{`<https://f.example/2>; rel="next"; rel="last"`},
			Links{{URL: "https://f.example/2", Rel: "next"}},
		},
		{
			"relative url",
			[]string{`</api/v1/repos?page=2>; rel="next"`},
			Links{{URL: "/api/v1/repos?page=2", Rel: "next"}},
		},
		{
			"malformed entries skipped",
			[]string{`garbage, <https://f.example/2>; rel="next", <https://f.example/3; rel="last"`},
			Links{{URL: "https://f.example/2", Rel: "next"}},
		},
		{
			"unterminated quote",
			[]string{`<https://f.example/2>; rel="next`},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for _, v := range tt.values {
				h.Add("Link", v)
			}
			got := ParseLinks(h)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseLinks() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNextPage(t *testing.T) {
	tests := []struct {
		name   string
		link   string
		want   string
		wantOK bool
	}{
		{"none", "", "", false},
		{"next", `<https://f.example/2>; rel="next", <https://f.example/9>; rel="last"`, "https://f.example/2", true},
		{"last page", `<https://f.example/1>; rel="first", <https://f.example/8>; rel="prev"`, "", false},
		{"case insensitive", `<https://f.example/2>; rel="NEXT"`, "https://f.example/2", true},
		{"one of many types", `<https://f.example/9>; rel="last next"`, "https://f.example/9", true},
		{"no substring match", `<https://f.example/9>; rel="nextish"`, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.link != "" {
				h.Set("Link", tt.link)
			}
			got, ok := NextPage(h)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("NextPage() = %q, %v, want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestLinksString(t *testing.T) {
	links := Links{
		{URL: "https://proxy.example/c/1/repos?page=2", Rel: "next"},
		{URL: "https://proxy.example/c/1/repos?page=9", Rel: "last", Params: map[string]string{"title": `say "hi"`, "anchor": "#x"}},
	}
	want := `<https://proxy.example/c/1/repos?page=2>; rel="next", <https://proxy.example/c/1/repos?page=9>; rel="last"; anchor="#x"; title="say \"hi\""`
	if got := links.String(); got != want {
		t.Errorf("String() = %s\nwant %s", got, want)
	}
	// Formatting then parsing gives the same links.
	h := http.Header{"Link": {links.String()}}
	if got := ParseLinks(h); !reflect.DeepEqual(got, links) {
		t.Errorf("round trip = %#v, want %#v", got, links)
	}
}
