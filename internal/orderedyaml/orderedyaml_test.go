package orderedyaml

import (
	"testing"

	"sigs.k8s.io/yaml"
)

type doc struct {
	Zeta  string            `json:"zeta"`
	Alpha int               `json:"alpha"`
	Flag  string            `json:"flag"`
	Map   map[string]string `json:"map"`
	List  []string          `json:"list"`
}

func TestMarshal_KeepsFieldOrderAndTypes(t *testing.T) {
	in := doc{Zeta: "z", Alpha: 1, Flag: "true", Map: map[string]string{"b": "2", "a": "x: y"}, List: []string{}}
	got, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := "zeta: z\nalpha: 1\nflag: \"true\"\nmap:\n  a: 'x: y'\n  b: \"2\"\nlist: []\n"
	if string(got) != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", got, want)
	}
	var back doc
	if err := yaml.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	if back.Flag != "true" || back.Map["b"] != "2" || back.Map["a"] != "x: y" {
		t.Errorf("round trip changed values: %+v", back)
	}
}
